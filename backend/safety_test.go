package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicDestructiveRPCsCannotBypassPreview(t *testing.T) {
	p := testAutomationPlugin(t)
	p.sessions["guard"] = &session{conn: connectionPayload{ID: "guard"}}
	for _, method := range []string{"docker/removeContainer", "docker/removeImage", "docker/removeVolume", "docker/removeNetwork", "docker/prune", "docker/applyCompose", "docker/tagImage", "docker/untagImage", "docker/disconnectNetwork", "docker/fileOperation"} {
		t.Run(method, func(t *testing.T) {
			_, err := p.dispatch(method, params{ConnectionID: "guard"}, jsonBytes(map[string]any{"operation": "delete"}), nil)
			if err == nil || !strings.Contains(err.Error(), "confirmed preview") {
				t.Fatalf("guard missing for %s: %v", method, err)
			}
		})
	}
	_, err := p.startFileUpload(p.sessions["guard"], jsonBytes(map[string]any{"containerId": "c", "path": "/home/file", "sessionId": "s", "size": 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "confirmed") {
		t.Fatalf("upload lacks backend gate: %v", err)
	}
	for _, name := range []string{"delete_file", "upload_file", "untag_image"} {
		if _, ok := findTool(name); ok {
			t.Fatalf("workbench-only operation exposed to AI: %s", name)
		}
	}
}

func TestWorkbenchUsesFreshHostReadOnlyFlags(t *testing.T) {
	p := testAutomationPlugin(t)
	var writes atomic.Int32
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) { writes.Add(1); w.WriteHeader(204) })
	c.ReadOnly = true
	_, err := p.dispatch("docker/containerAction", params{ConnectionID: c.ID, Connection: c}, jsonBytes(map[string]any{"containerId": "c", "action": "stop"}), nil)
	if err == nil || !strings.Contains(err.Error(), "read-only") || writes.Load() != 0 {
		t.Fatalf("cached writable policy survived host update: %v", err)
	}
}

func TestRevokedAutomationIsCheckedAfterAcquiringWriteLock(t *testing.T) {
	p := testAutomationPlugin(t)
	sess := &session{conn: connectionPayload{ID: "revoked"}, automated: true}
	task, _ := findTool("container_action")
	if _, err := p.executeMutation(sess, task, map[string]any{"containerId": "c", "action": "stop"}); err == nil {
		t.Fatal("revoked queued mutation executed")
	}
}

func TestTagPreviewBindsExistingTargetAndLegacyExecution(t *testing.T) {
	p := testAutomationPlugin(t)
	var generation, writes atomic.Int32
	c, sess := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
			w.WriteHeader(201)
			return
		}
		switch r.URL.Path {
		case "/images/source/json":
			fmt.Fprint(w, `{"Id":"source"}`)
		case "/images/repo:latest/json":
			fmt.Fprintf(w, `{"Id":"old-%d"}`, generation.Load())
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	args := map[string]any{"imageId": "source", "repository": "repo", "tag": "latest"}
	task, _ := findTool("tag_image")
	preview, err := p.prepareMutation(sess, task, args)
	if err != nil {
		t.Fatal(err)
	}
	token := preview.(map[string]any)["previewToken"]
	generation.Add(1)
	raw := map[string]any{"imageId": "source", "repository": "repo", "tag": "latest", "previewToken": token}
	if _, err = p.dispatch("docker/tagImage", params{ConnectionID: c.ID}, jsonBytes(raw), nil); err == nil || writes.Load() != 0 {
		t.Fatal("changed target tag was overwritten")
	}
	preview, err = p.prepareMutation(sess, task, args)
	if err != nil {
		t.Fatal(err)
	}
	raw["previewToken"] = preview.(map[string]any)["previewToken"]
	if _, err = p.dispatch("docker/tagImage", params{ConnectionID: c.ID}, jsonBytes(raw), nil); err != nil || writes.Load() != 1 {
		t.Fatalf("confirmed tag failed: %v", err)
	}
	if _, err = p.dispatch("docker/tagImage", params{ConnectionID: c.ID}, jsonBytes(raw), nil); err == nil || writes.Load() != 1 {
		t.Fatal("legacy endpoint replayed token")
	}
}

func TestNetworkDisconnectRequiresBoundPreview(t *testing.T) {
	p := testAutomationPlugin(t)
	var writes atomic.Int32
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
			w.WriteHeader(200)
			return
		}
		switch r.URL.Path {
		case "/networks/net":
			fmt.Fprint(w, `{"Id":"net","Name":"custom","Driver":"bridge","Containers":{"c":{"Name":"web"}}}`)
		case "/containers/c/json":
			fmt.Fprint(w, `{"Id":"c","Name":"/web","State":{"Status":"running"}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	grant(t, p, c.ID, "disconnect_network")
	args := map[string]any{"networkId": "net", "containerId": "c"}
	preview := resultMap(t, call(p, c, "disconnect_network", args))
	if writes.Load() != 0 {
		t.Fatal("preview disconnected network")
	}
	args["previewToken"] = preview["previewToken"]
	if r := call(p, c, "disconnect_network", args); r.IsError || writes.Load() != 1 {
		t.Fatal("confirmed disconnect failed", r)
	}
}

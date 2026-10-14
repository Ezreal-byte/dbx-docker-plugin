package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testAutomationPlugin(t *testing.T) *plugin {
	t.Helper()
	p := newPlugin()
	p.dataDir = t.TempDir()
	return p
}
func grant(t *testing.T, p *plugin, id string, names ...string) {
	t.Helper()
	if err := p.lockedState(true, func(s *automationState) error { s.Policies[id] = automationPolicy{true, names, 1}; return nil }); err != nil {
		t.Fatal(err)
	}
}
func call(p *plugin, c connectionPayload, name string, args map[string]any) callToolResult {
	return p.mcpCall(jsonBytes(map[string]any{"tool": name, "arguments": args, "lifecycle": params{Connection: c, ConnectionID: c.ID}}))
}
func resultMap(t *testing.T, r callToolResult) map[string]any {
	t.Helper()
	if r.IsError {
		t.Fatal(r.Content)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.Content[0]["text"].(string)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func engineFixture(t *testing.T, p *plugin, handler http.HandlerFunc) (connectionPayload, *session) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.47")
			fmt.Fprint(w, "OK")
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1.47")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	c := connectionPayload{ID: "fixture", PluginID: pluginID, Host: host, Port: n, ExternalConfig: map[string]any{"protocol": "http", "api_version": "1.47"}}
	sess, err := p.sessionFor(c.ID, c, runtimeEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.client.close)
	return c, sess
}

func TestAutomationDefaultOffAndDiscoveryWithoutDial(t *testing.T) {
	p := testAutomationPlugin(t)
	if len(p.mcpTools("").(map[string]any)["tools"].([]toolDefinition)) != 0 {
		t.Fatal("default catalog must be empty")
	}
	c := connectionPayload{ID: "test", Host: "never-resolve.invalid", Port: 2375}
	if r := call(p, c, "list_containers", nil); !r.IsError {
		t.Fatal("default enabled")
	}
	grant(t, p, "test", "list_containers")
	if len(p.mcpTools("").(map[string]any)["tools"].([]toolDefinition)) != 1 || len(p.sessions) != 0 {
		t.Fatal("discovery must not connect")
	}
	if r := call(p, connectionPayload{ID: "production"}, "list_containers", nil); !r.IsError {
		t.Fatal("permissions leaked across connections")
	}
	if err := os.WriteFile(filepath.Join(p.dataDir, "automation.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.policyAllows("test", "list_containers") == nil || len(p.mcpTools("").(map[string]any)["tools"].([]toolDefinition)) != 0 {
		t.Fatal("corruption must fail closed")
	}
}
func TestSettingsIsolationRevisionAndCrossProcessReload(t *testing.T) {
	p := testAutomationPlugin(t)
	p.sessions["a"] = &session{conn: connectionPayload{ID: "a"}}
	if _, err := p.settingsSet(jsonBytes(map[string]any{"connectionId": "a", "enabled": true, "allowedTools": []string{"remove_volume"}})); err != nil {
		t.Fatal(err)
	}
	if p.policyAllows("a", "remove_volume") == nil || p.policyAllows("a", "list_containers") != nil {
		t.Fatal("first enable must be queries only")
	}
	if _, err := p.settingsSet(jsonBytes(map[string]any{"connectionId": "a", "enabled": true, "revision": 0})); err == nil {
		t.Fatal("stale revision accepted")
	}
	p2 := newPlugin()
	p2.dataDir = p.dataDir
	if p2.policyAllows("a", "list_containers") != nil {
		t.Fatal("other sidecar cannot read settings")
	}
	if _, err := p.settingsSet(jsonBytes(map[string]any{"connectionId": "a", "enabled": false, "revision": 1})); err != nil {
		t.Fatal(err)
	}
	if p2.policyAllows("a", "list_containers") == nil {
		t.Fatal("cached authorization survived revocation")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- p2.lockedState(true, func(s *automationState) error { s.Policies[fmt.Sprint(i)] = automationPolicy{Revision: 1}; return nil })
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := p.lockedState(false, func(s *automationState) error {
		if len(s.Policies) != 13 {
			return fmt.Errorf("lost updates: %d", len(s.Policies))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestToolValidationReadOnlyAndRedaction(t *testing.T) {
	p := testAutomationPlugin(t)
	var mutations atomic.Int32
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
		}
		fmt.Fprint(w, `[{"Id":"c1","Names":["/web"],"State":"running","Command":"secret-command","Labels":{"password":"secret-label"}}]`)
	})
	grant(t, p, c.ID, "list_containers", "container_action", "inspect_container")
	r := call(p, c, "list_containers", map[string]any{"limit": 1})
	v := resultMap(t, r)
	if v["total"] != float64(1) || strings.Contains(r.Content[0]["text"].(string), "secret-") {
		t.Fatal("missing pagination or redaction", r)
	}
	for _, args := range []map[string]any{{"host": "evil"}, {"limit": 201}, {"limit": 1.5}, {"connectionId": "production"}, {"rpc": "docker/startExec"}} {
		if !call(p, c, "list_containers", args).IsError {
			t.Fatal("invalid args accepted", args)
		}
	}
	c.ReadOnly = true
	if !call(p, c, "container_action", map[string]any{"containerId": "c1", "action": "stop"}).IsError {
		t.Fatal("host readonly bypassed")
	}
	c.ReadOnly = false
	c.ExternalConfig["read_only"] = true
	if !call(p, c, "container_action", map[string]any{"containerId": "c1", "action": "stop"}).IsError || mutations.Load() != 0 {
		t.Fatal("plugin readonly bypassed")
	}
	for _, tool := range toolCatalog() {
		if slices.Contains([]string{"mcp/settings/set", "docker/startExec", "docker/startFileUpload"}, tool.Method) {
			t.Fatal("forbidden MCP method")
		}
	}
}
func TestPreviewBindingExpiryReplayAndChangedState(t *testing.T) {
	p := testAutomationPlugin(t)
	var deleted atomic.Int32
	var generation atomic.Int32
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted.Add(1)
			w.WriteHeader(204)
			return
		}
		switch r.URL.Path {
		case "/volumes/data":
			fmt.Fprintf(w, `{"Name":"data","Driver":"local","CreatedAt":"version-%d"}`, generation.Load())
		case "/containers/json":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	grant(t, p, c.ID, "remove_volume")
	preview := func() string {
		return resultMap(t, call(p, c, "remove_volume", map[string]any{"name": "data"}))["previewToken"].(string)
	}
	token := preview()
	if deleted.Load() != 0 {
		t.Fatal("preview mutated engine")
	}
	if !call(p, c, "remove_volume", map[string]any{"name": "other", "previewToken": token}).IsError {
		t.Fatal("parameter substitution accepted")
	}
	other := c
	other.ID = "other"
	grant(t, p, other.ID, "remove_volume")
	if !call(p, other, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError {
		t.Fatal("cross-connection token accepted")
	}
	if call(p, c, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError || deleted.Load() != 1 {
		t.Fatal("valid execution failed")
	}
	if !call(p, c, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError || deleted.Load() != 1 {
		t.Fatal("token replay")
	}
	token = preview()
	generation.Add(1)
	if !call(p, c, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError || deleted.Load() != 1 {
		t.Fatal("stale target deleted")
	}
	token = preview()
	_ = p.lockedState(true, func(s *automationState) error {
		v := s.Previews[token]
		v.ExpiresAt = time.Now().Add(-time.Second)
		s.Previews[token] = v
		return nil
	})
	if !call(p, c, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError {
		t.Fatal("expired preview accepted")
	}
	token = preview()
	grant(t, p, c.ID, "list_volumes")
	if !call(p, c, "remove_volume", map[string]any{"name": "data", "previewToken": token}).IsError {
		t.Fatal("preview substituted for authorization")
	}
}
func TestTunnelCannotDialUnresolvedEndpoint(t *testing.T) {
	p := testAutomationPlugin(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "OK") }))
	defer server.Close()
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	c := connectionPayload{ID: "tunnel", Host: host, Port: n, TransportLayers: []map[string]any{{"type": "ssh"}}}
	_, err := p.automationSession(params{Connection: c, Runtime: runtimeEndpoint{Host: host, Port: n}})
	if err != errTunnelSession || hits.Load() != 0 {
		t.Fatal("unresolved tunnel dialed target", err, hits.Load())
	}
	grant(t, p, c.ID, "list_containers")
	r := call(p, c, "list_containers", map[string]any{"_dbxDockerForwarded": true})
	if !r.IsError || hits.Load() != 0 {
		t.Fatal("forwarded call recursed or direct connected")
	}
}
func TestLogsAreBoundedAndStripPartialFrame(t *testing.T) {
	p := testAutomationPlugin(t)
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") == "1" || r.URL.Query().Get("follow") == "true" {
			t.Error("logs must not follow")
		}
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], 256*1024)
		w.Write(header)
		fmt.Fprint(w, strings.Repeat("x", 256*1024))
	})
	grant(t, p, c.ID, "container_logs")
	out := resultMap(t, call(p, c, "container_logs", map[string]any{"containerId": "c1", "tail": 20}))
	data := out["text"].(string)
	if out["truncated"] != true || len(data) > 128*1024 || strings.ContainsRune(data, 0) {
		t.Fatal("unbounded or framed logs")
	}
}
func TestTaskCancellationRestartAndConnectionIsolation(t *testing.T) {
	p := testAutomationPlugin(t)
	started := make(chan struct{})
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/create" {
			t.Errorf("unexpected: %s", r.URL.Path)
			return
		}
		fmt.Fprintln(w, `{"status":"pulling"}`)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	grant(t, p, c.ID, "pull_image", "task_status", "cancel_task")
	id := resultMap(t, call(p, c, "pull_image", map[string]any{"image": "fixture:latest"}))["taskId"].(string)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	if _, err := p.taskRPC("docker/taskStatus", "other", jsonBytes(map[string]any{"taskId": id})); err == nil {
		t.Fatal("task leaked to other connection")
	}
	if r := call(p, c, "cancel_task", map[string]any{"taskId": id}); r.IsError {
		t.Fatal(r)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		v, err := p.taskRPC("docker/taskStatus", c.ID, jsonBytes(map[string]any{"taskId": id}))
		if err != nil {
			t.Fatal(err)
		}
		if v.(taskRecord).Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not cancel", v)
		}
		time.Sleep(30 * time.Millisecond)
	}
	orphan := randomID()
	_ = p.lockedState(true, func(s *automationState) error {
		s.Tasks[orphan] = taskRecord{ID: orphan, ConnectionID: c.ID, Status: "running"}
		return nil
	})
	v, err := p.taskRPC("docker/taskStatus", c.ID, jsonBytes(map[string]any{"taskId": orphan}))
	if err != nil || v.(taskRecord).Status != "interrupted" {
		t.Fatal("orphan task not interrupted", v, err)
	}
}
func TestComposeRejectsUnsupportedFields(t *testing.T) {
	for _, content := range []string{"services:\n  app:\n    image: alpine\n    privileged: true", "services:\n  app:\n    image: alpine\n    depends_on: [db]", "services:\n  app:\n    image: ${IMAGE}", "services:\n  app:\n    image: alpine\nnetworks:\n  default:\n    driver: overlay"} {
		if _, err := buildComposePlan(DockerComposeApplyRequest{ProjectName: "test", Content: content}); err == nil {
			t.Fatal("unsupported Compose accepted", content)
		}
	}
}
func TestProductionFlags(t *testing.T) {
	if !(connectionPayload{IsProduction: true}).effectiveProduction() || !(connectionPayload{ExternalConfig: map[string]any{"is_production": true}}).effectiveProduction() {
		t.Fatal("production flag lost")
	}
}

func TestProtectedResourcesAndExactPrune(t *testing.T) {
	p := testAutomationPlugin(t)
	var deletes atomic.Int32
	var deleted sync.Map
	c, _ := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes.Add(1)
			deleted.Store(r.URL.Path, true)
			w.WriteHeader(204)
			return
		}
		switch r.URL.Path {
		case "/containers/json":
			fmt.Fprint(w, `[{"Id":"stopped","State":"exited","Mounts":[{"Type":"volume","Name":"used","Destination":"/data"}]}]`)
		case "/volumes":
			fmt.Fprint(w, `{"Volumes":[{"Name":"named","Driver":"local"},{"Name":"anon","Driver":"local","Labels":{"com.docker.volume.anonymous":""}},{"Name":"used","Driver":"local"}]}`)
		case "/volumes/anon":
			fmt.Fprint(w, `{"Name":"anon","Driver":"local","CreatedAt":"fixed"}`)
		case "/networks/bridge":
			fmt.Fprint(w, `{"Id":"bridge-id","Name":"bridge","Driver":"bridge"}`)
		case "/networks/attached":
			fmt.Fprint(w, `{"Id":"attached-id","Name":"custom","Driver":"bridge","Containers":{"c1":{"Name":"web"}}}`)
		case "/networks":
			fmt.Fprint(w, `[{"Id":"bridge-id","Name":"bridge","Driver":"bridge"}]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	sess := p.sessions[c.ID]
	for _, params := range []struct {
		method string
		args   map[string]any
	}{{"docker/removeVolume", map[string]any{"name": "used"}}, {"docker/removeNetwork", map[string]any{"networkId": "bridge"}}, {"docker/removeNetwork", map[string]any{"networkId": "attached"}}} {
		if _, err := p.resourceOperation(params.method, sess, jsonBytes(params.args)); err == nil {
			t.Fatal("protected resource removed", params)
		}
	}
	if deletes.Load() != 0 {
		t.Fatal("protected resources reached DELETE")
	}
	preview, err := p.prunePreview(sess, jsonBytes(map[string]any{"target": "networks"}))
	if err != nil || preview.(DockerPrunePreview).Count != 0 {
		t.Fatal("built-in network included", preview, err)
	}
	tool, _ := findTool("prune_resources")
	args := map[string]any{"target": "volumes"}
	bound, err := p.prepareMutation(sess, tool, args)
	if err != nil {
		t.Fatal(err)
	}
	summary := bound.(map[string]any)["preview"].(DockerPrunePreview)
	if summary.Count != 1 || summary.Items[0].ID != "anon" {
		t.Fatal("named or used volume in anonymous prune", summary)
	}
	if err = p.consumePreview(sess, tool, args, bound.(map[string]any)["previewToken"].(string)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.executeMutation(sess, tool, args); err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted.Load("/volumes/anon"); !ok || deletes.Load() != 1 {
		t.Fatal("exact prune scope failed")
	}
}

func TestDesktopBridgeUsesCachedTunnelAndOnlyOneHop(t *testing.T) {
	p := testAutomationPlugin(t)
	c, sess := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	c.Host = "logical.invalid"
	c.Port = 2375
	c.TransportLayers = []map[string]any{{"type": "ssh"}}
	sess.client.runtime = runtimeEndpoint{Host: sess.conn.Host, Port: sess.conn.Port}
	sess.conn = c
	grant(t, p, c.ID, "list_containers")
	actual, err := p.automationSession(params{Connection: c, Runtime: runtimeEndpoint{Host: c.Host, Port: c.Port}})
	if err != nil || actual.client != sess.client {
		t.Fatal("desktop tunnel not reused", err)
	}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/call-plugin-tool" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("bridge protocol")
		}
		var in struct {
			PluginID     string         `json:"plugin_id"`
			ConnectionID string         `json:"connection_id"`
			Tool         string         `json:"tool"`
			Arguments    map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.PluginID != pluginID || in.ConnectionID != c.ID || in.Arguments["_dbxDockerForwarded"] != true {
			t.Error("missing forward scope")
		}
		json.NewEncoder(w).Encode(call(p, c, in.Tool, in.Arguments))
	}))
	defer bridge.Close()
	_, port, _ := net.SplitHostPort(bridge.Listener.Addr().String())
	root := t.TempDir()
	t.Setenv("DBX_APP_DATA_DIR", root)
	_ = os.WriteFile(filepath.Join(root, "mcp-bridge-port"), []byte(port), 0600)
	out, err := p.forwardToDesktop(c.ID, "list_containers", map[string]any{})
	if err != nil || out.IsError {
		t.Fatal("bridge failed", out, err)
	}
}

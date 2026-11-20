package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestClonePreviewAndConfigurationSafety(t *testing.T) {
	p := testAutomationPlugin(t)
	var generation, writes atomic.Int32
	c, sess := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["Image"] != "sha256:pinned" {
				t.Error("copy did not pin the image")
			}
			if _, ok := request["HostConfig"]; !ok {
				t.Error("missing host configuration")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"Id":"copy"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": "source", "Image": "sha256:pinned", "Name": "/source", "Config": map[string]any{"Image": "moving:latest", "Env": []string{"PASSWORD=private"}, "Labels": map[string]string{labelProject: "test"}, "Cmd": []string{"sleep", "180"}}, "HostConfig": map[string]any{"Privileged": true, "Binds": []string{"/data:/data"}, "NetworkMode": "host", "Memory": generation.Load()}})
	})
	tool, _ := findMutation("clone_container")
	args := map[string]any{"containerId": "source", "name": "copy"}
	preview, err := p.prepareMutation(sess, tool, args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonBytes(preview)), "PASSWORD") || writes.Load() != 0 {
		t.Fatal("preview leaked data or wrote")
	}
	token := preview.(map[string]any)["previewToken"].(string)
	generation.Add(1)
	if _, err = p.dispatch("docker/cloneContainer", params{ConnectionID: c.ID}, jsonBytes(map[string]any{"containerId": "source", "name": "copy", "previewToken": token}), nil); err == nil || writes.Load() != 0 {
		t.Fatal("stale copy preview executed")
	}
	preview, err = p.prepareMutation(sess, tool, args)
	if err != nil {
		t.Fatal(err)
	}
	token = preview.(map[string]any)["previewToken"].(string)
	if _, err = p.dispatch("docker/cloneContainer", params{ConnectionID: c.ID}, jsonBytes(map[string]any{"containerId": "source", "name": "copy", "previewToken": token}), nil); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("copy was not created exactly once")
	}
	if _, err = p.dispatch("docker/cloneContainer", params{ConnectionID: c.ID}, jsonBytes(map[string]any{"containerId": "source", "name": "copy", "previewToken": token}), nil); err == nil {
		t.Fatal("replayed token accepted")
	}
	var source container.InspectResponse
	_ = json.Unmarshal([]byte(`{"Id":"source","Image":"sha256:pinned","Config":{"Image":"old","Hostname":"source","Volumes":{"/data":{}},"Labels":{"com.docker.compose.project":"demo","custom":"yes"}},"HostConfig":{"Privileged":true,"Binds":["/data:/data"],"VolumesFrom":["source"],"NetworkMode":"container:source","PortBindings":{"80/tcp":[{"HostPort":"8080"}]},"RestartPolicy":{"Name":"always"}}}`), &source)
	config, host, _, err := cloneConfiguration(source)
	if err != nil {
		t.Fatal(err)
	}
	if host.Privileged || len(host.Binds) > 0 || len(host.PortBindings) > 0 || len(host.VolumesFrom) > 0 || host.NetworkMode.IsContainer() || host.RestartPolicy.Name != "no" || config.Volumes != nil || config.Labels[labelProject] != "" || config.Labels["custom"] != "yes" {
		t.Fatal("unsafe copied settings")
	}
	if source.Config.Labels[labelProject] != "demo" || !source.HostConfig.Privileged {
		t.Fatal("source configuration was mutated")
	}
}

func TestComposeDiscoveryOriginalTextAndRetainedMetadata(t *testing.T) {
	p := testAutomationPlugin(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	original := "# 原始注释\nservices:\n  app:\n    image: ${APP_IMAGE}\n"
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var phase atomic.Int32
	_, sess := engineFixture(t, p, func(w http.ResponseWriter, r *http.Request) {
		if phase.Load() == 1 {
			w.Write([]byte(`[]`))
			return
		}
		labels := map[string]string{labelProject: "example", "com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": file}
		rows := []any{map[string]any{"Id": "c", "Labels": labels, "State": "running"}}
		if phase.Load() == 2 {
			other := map[string]string{labelProject: "example", "com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": filepath.Join(dir, "other.yml")}
			rows = append(rows, map[string]any{"Id": "other", "Labels": other})
		}
		json.NewEncoder(w).Encode(rows)
	})
	value, err := p.composeSource(sess, jsonBytes(map[string]any{"project": "example"}))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["files"].([]map[string]string)[0]["content"] != original || result["sourceHash"] != hashValue(original) {
		t.Fatal("original YAML was not preserved")
	}
	phase.Store(1)
	projects, err := p.composeProjects(sess)
	if err != nil || len(projects) != 1 || projects[0].Containers != 0 {
		t.Fatal("removed project was forgotten", err)
	}
	phase.Store(2)
	if _, err = p.findComposeProject(sess, "example"); err == nil {
		t.Fatal("conflicting source labels accepted")
	}
	if _, err = p.composeSource(sess, jsonBytes(map[string]any{"project": "missing", "path": file})); err == nil {
		t.Fatal("arbitrary source path accepted")
	}
	if err = writeComposeSource(context.Background(), sess, composeProject{Files: []string{file}}, "# edited\nservices: {}\n"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(body), "# edited") {
		t.Fatal("atomic save failed")
	}
	if err = os.WriteFile(file, []byte(strings.Repeat("x", composeSourceLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readComposeHostFile(context.Background(), sess, composeProject{}, file); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestComposeLocalConnectionAndCommandBounds(t *testing.T) {
	b := &boundedOutput{max: 3}
	if n, err := b.Write([]byte("123456")); n != 6 || err != nil || !b.truncated || b.data.String() != "123" {
		t.Fatal("output cap failed")
	}
	if validComposeHostPath("relative.yml", true) || validComposeHostPath("/safe\ncommand", true) || !validComposeHostPath("/srv/app/compose.yml", true) {
		t.Fatal("host path validation")
	}
}

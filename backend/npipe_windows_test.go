//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestNamedPipeConnection(t *testing.T) {
	name := "dbx-docker-test-" + randomID()
	listener, err := winio.ListenPipe(`\\.\pipe\`+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		w.Header().Set("API-Version", "1.47")
		fmt.Fprint(w, "OK")
	})}
	go server.Serve(listener)
	defer server.Close()
	c := connectionPayload{ID: "npipe-test", ExternalConfig: map[string]any{"protocol": "npipe", "pipe_path": "//./pipe/" + name}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dc, err := connect(ctx, c, runtimeEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	dc.close()
	for _, path := range []string{"//server/pipe/docker_engine", "//./pipe/../other", "//./pipe/", "//./pipe/a\\b"} {
		c.ExternalConfig["pipe_path"] = path
		if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil {
			t.Fatal("unsafe pipe path", path)
		}
	}
	c.ExternalConfig["pipe_path"] = "//./pipe/docker_engine"
	c.TransportLayers = []map[string]any{{"type": "ssh"}}
	if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil || !strings.Contains(err.Error(), "Tunnel") {
		t.Fatal("npipe tunnel must fail", err)
	}
}

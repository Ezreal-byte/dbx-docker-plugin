//go:build !windows

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixSocketConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.47")
		fmt.Fprint(w, "OK")
	})}
	go server.Serve(listener)
	defer server.Close()
	c := connectionPayload{ID: "unix", ExternalConfig: map[string]any{"protocol": "unix", "socket_path": path}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dc, err := connect(ctx, c, runtimeEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	dc.close()
	c.ExternalConfig = map[string]any{"protocol": "npipe"}
	if validateConfig(c, c.config(), runtimeEndpoint{}) == nil {
		t.Fatal("npipe must be rejected on non-Windows platforms")
	}
}

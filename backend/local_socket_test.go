package main

import (
	"strings"
	"testing"
)

func TestLocalModeUsesOSDefaultWithoutStaleHiddenPaths(t *testing.T) {
	c := connectionPayload{ExternalConfig: map[string]any{
		"protocol": "local", "socket_path": "/previous/docker.sock", "pipe_path": "//./pipe/previous",
	}}
	cfg := c.config()
	if namedPipeSupported() {
		if cfg.Protocol != "npipe" || cfg.PipePath != "//./pipe/docker_engine" {
			t.Fatalf("local Windows default: %+v", cfg)
		}
	} else if cfg.Protocol != "unix" || cfg.SocketPath != "/var/run/docker.sock" {
		t.Fatalf("local Unix default: %+v", cfg)
	}
	if err := validateConfig(c, cfg, runtimeEndpoint{}); err != nil {
		t.Fatal(err)
	}
	c.TransportLayers = []map[string]any{{"type": "ssh", "enabled": true}}
	if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil || !strings.Contains(err.Error(), "Tunnel / Proxy") {
		t.Fatalf("local mode must reject a host tunnel instead of silently dialing locally: %v", err)
	}
}

func TestExistingSocketModesAndConnectionDefaultsArePreserved(t *testing.T) {
	if cfg := (connectionPayload{}).config(); cfg.Protocol != "http" {
		t.Fatalf("existing default changed to %s", cfg.Protocol)
	}
	for _, protocol := range []string{"unix", "unix-over-nc", "unix-over-nc-sudo"} {
		for _, path := range []string{"", "/custom/docker.sock"} {
			c := connectionPayload{ExternalConfig: map[string]any{
				"protocol": protocol, "socket_path": path, "ssh_host": "host", "ssh_username": "docker",
			}}
			cfg := c.config()
			want := path
			if want == "" {
				want = "/var/run/docker.sock"
			}
			if cfg.Protocol != protocol || cfg.SocketPath != want {
				t.Fatalf("saved %s path changed: %+v", protocol, cfg)
			}
		}
	}
}

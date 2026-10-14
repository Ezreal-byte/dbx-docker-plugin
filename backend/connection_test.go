package main

import (
	"context"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The logical Docker address deliberately cannot reach this local fixture.
// Only the DBX runtime endpoint can serve the ping; TLS must still verify the
// logical server name, and HTTP must keep its original Host header.
func TestConnectUsesDBXRuntimeEndpoint(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		t.Run(protocol, func(t *testing.T) {
			type request struct{ host, sni string }
			seen := make(chan request, 4)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := request{host: r.Host}
				if r.TLS != nil {
					got.sni = r.TLS.ServerName
				}
				seen <- got
				if r.URL.Path != "/_ping" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				w.Header().Set("API-Version", "1.47")
				_, _ = w.Write([]byte("OK"))
			})
			server := httptest.NewUnstartedServer(handler)
			cfg := map[string]any{"protocol": protocol}
			host := "docker.invalid"
			if protocol == "https" {
				server.StartTLS()
				host = server.Certificate().DNSNames[0]
				caPath := filepath.Join(t.TempDir(), "ca.pem")
				if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
					t.Fatal(err)
				}
				cfg["ca_cert_path"] = caPath
			} else {
				server.Start()
			}
			defer server.Close()
			runtimeHost, runtimePort, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, _ := strconv.Atoi(runtimePort)
			c := connectionPayload{Host: host, Port: 2376, ExternalConfig: cfg, TransportLayers: []map[string]any{{"type": "ssh"}}}
			dc, err := connect(context.Background(), c, runtimeEndpoint{Host: runtimeHost, Port: port})
			if err != nil {
				t.Fatal(err)
			}
			defer dc.close()
			got := <-seen
			if want := net.JoinHostPort(host, "2376"); got.host != want {
				t.Errorf("HTTP Host = %q, want %q", got.host, want)
			}
			if protocol == "https" && got.sni != host {
				t.Errorf("TLS SNI = %q, want %q", got.sni, host)
			}
		})
	}
}

func TestTunnelCannotFallBackToDirectConnection(t *testing.T) {
	c := connectionPayload{Host: "127.0.0.1", Port: 2375, TransportLayers: []map[string]any{{"type": "ssh"}}}
	for _, endpoint := range []runtimeEndpoint{{}, {Host: "127.0.0.1"}, {Port: 12345}, {Host: "127.0.0.1", Port: 65536}} {
		t.Run(fmt.Sprint(endpoint), func(t *testing.T) {
			_, err := connect(context.Background(), c, endpoint)
			if err == nil || !strings.Contains(err.Error(), "runtime endpoint") {
				t.Fatalf("missing runtime endpoint should fail before dialing: %v", err)
			}
		})
	}
}

func TestSocketModesRejectHostTransportLayers(t *testing.T) {
	for _, protocol := range []string{"unix", "unix-over-nc", "unix-over-nc-sudo"} {
		c := connectionPayload{
			ExternalConfig:  map[string]any{"protocol": protocol, "ssh_host": "ssh.example.com", "ssh_username": "docker"},
			TransportLayers: []map[string]any{{"type": "ssh"}},
		}
		if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil || !strings.Contains(err.Error(), "Clear Tunnel / Proxy") {
			t.Fatalf("%s must reject ignored host tunnel: %v", protocol, err)
		}
		c.TransportLayers[0]["enabled"] = false
		if err := validateConfig(c, c.config(), runtimeEndpoint{}); err != nil {
			t.Fatalf("%s with disabled host tunnel: %v", protocol, err)
		}
	}
}

func TestRemoteHTTPGuardWithDisabledTunnel(t *testing.T) {
	c := connectionPayload{Host: "docker.example.com", Port: 2375, TransportLayers: []map[string]any{{"type": "ssh", "enabled": false}}}
	if err := validateConfig(c, c.config(), runtimeEndpoint{Host: "127.0.0.1", Port: 12345}); err == nil || !strings.Contains(err.Error(), "Remote Docker HTTP") {
		t.Fatalf("disabled tunnel must not bypass remote HTTP guard: %v", err)
	}
}

func TestSSHConnectionValidation(t *testing.T) {
	for _, port := range []float64{0, -1, 65536, 22.5} {
		c := connectionPayload{ExternalConfig: map[string]any{"protocol": "unix-over-nc", "ssh_host": "ssh.example.com", "ssh_username": "docker", "ssh_port": port}}
		if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil || !strings.Contains(err.Error(), "SSH port") {
			t.Errorf("SSH port %v should fail: %v", port, err)
		}
	}
	c := connectionPayload{ExternalConfig: map[string]any{"protocol": "unix-over-nc", "ssh_host": "ssh.example.com"}}
	if err := validateConfig(c, c.config(), runtimeEndpoint{}); err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("missing SSH user should fail: %v", err)
	}
}

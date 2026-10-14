package main

import "testing"

func TestPublishedPortURL(t *testing.T) {
	cases := []struct {
		name, protocol, host, ssh, ip string
		public, private               int
		want                          string
	}{
		{"local TCP", "http", "127.0.0.1", "", "0.0.0.0", 8080, 80, "http://127.0.0.1:8080"},
		{"named pipe", "npipe", "", "", "0.0.0.0", 8080, 80, "http://127.0.0.1:8080"},
		{"Unix", "unix", "", "", "::", 8443, 443, "https://127.0.0.1:8443"},
		{"SSH", "unix-over-nc", "", "nas.example.test", "0.0.0.0", 8080, 80, "http://nas.example.test:8080"},
		{"IPv6", "https", "2001:db8::1", "", "::", 8080, 80, "http://[2001:db8::1]:8080"},
		{"remote loopback", "http", "nas.example.test", "", "127.0.0.1", 8080, 80, ""},
		{"invalid host", "http", "example.test/?cmd=x", "", "0.0.0.0", 8080, 80, ""},
		{"invalid port", "http", "127.0.0.1", "", "0.0.0.0", 0, 80, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := connectionPayload{Host: tc.host, ExternalConfig: map[string]any{"protocol": tc.protocol, "ssh_host": tc.ssh}}
			got, err := containerPortURL(conn, tc.ip, tc.public, tc.private)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected unavailable port error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

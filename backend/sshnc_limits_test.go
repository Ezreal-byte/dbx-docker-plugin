package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"golang.org/x/crypto/ssh"
)

// A real SSH transport with an sshd-style session quota. Each exec channel
// serves a small Docker HTTP fixture instead of executing a local nc process.
type limitedSSHServer struct {
	cfg       pluginConfig
	active    atomic.Int32
	peak      atomic.Int32
	opens     atomic.Int32
	rejected  atomic.Int32
	images    atomic.Int32
	commands  atomic.Int32
	rejectFor int32
	limit     int32
}

func startLimitedSSHServer(t *testing.T, limit, rejectFor int32) *limitedSSHServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	f := &limitedSSHServer{limit: limit, rejectFor: rejectFor, cfg: pluginConfig{
		SSHHost: host, SSHPort: port, SSHUsername: "fixture",
		SSHKnownHostsPath: writeKnownHosts(t, listener.Addr().String(), signer.PublicKey()),
	}}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for pending := range channels {
					attempt := f.opens.Add(1)
					if attempt <= f.rejectFor || f.active.Load() >= f.limit {
						f.rejected.Add(1)
						_ = pending.Reject(ssh.ConnectionFailed, "open failed")
						continue
					}
					channel, requests, err := pending.Accept()
					if err != nil {
						continue
					}
					active := f.active.Add(1)
					for peak := f.peak.Load(); active > peak && !f.peak.CompareAndSwap(peak, active); peak = f.peak.Load() {
					}
					go f.serve(channel, requests)
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-stopped })
	return f
}

func (f *limitedSSHServer) serve(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { f.active.Add(-1); _ = channel.Close() }()
	request, ok := <-requests
	if !ok {
		return
	}
	var exec struct{ Command string }
	if request.Type != "exec" || ssh.Unmarshal(request.Payload, &exec) != nil ||
		(exec.Command != "nc -U '/var/run/docker.sock'" && exec.Command != "sudo -n -- nc -U '/var/run/docker.sock'") {
		_ = request.Reply(false, nil)
		return
	}
	f.commands.Add(1)
	_ = request.Reply(true, nil)
	go ssh.DiscardRequests(requests)
	reader := bufio.NewReader(channel)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		body := "OK"
		if strings.HasSuffix(req.URL.Path, "/images/json") {
			f.images.Add(1)
			time.Sleep(10 * time.Millisecond)
			body = "[]"
		}
		_, err = fmt.Fprintf(channel, "HTTP/1.1 200 OK\r\nAPI-Version: 1.40\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
		if err == nil && req.Method != http.MethodHead {
			_, err = io.WriteString(channel, body)
		}
		if err != nil {
			return
		}
	}
}

func (f *limitedSSHServer) bridge(t *testing.T) *sshNCBridge {
	t.Helper()
	b, err := dialSSHNC(context.Background(), f.cfg, "fixture", "/var/run/docker.sock", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	return b
}

func TestSSHNCConcurrentDockerRequestsRespectDefaultMaxSessions(t *testing.T) {
	for _, protocol := range []string{"unix-over-nc", "unix-over-nc-sudo"} {
		t.Run(protocol, func(t *testing.T) {
			f := startLimitedSSHServer(t, 10, 0)
			c := connectionPayload{ExternalConfig: map[string]any{
				"protocol": protocol, "api_version": "1.40", "ssh_host": f.cfg.SSHHost,
				"ssh_port": float64(f.cfg.SSHPort), "ssh_username": f.cfg.SSHUsername,
				"ssh_known_hosts_path": f.cfg.SSHKnownHostsPath,
			}, Secrets: map[string]string{"ssh_password": "fixture"}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dc, err := connect(ctx, c, runtimeEndpoint{})
			if err != nil {
				t.Fatal(err)
			}
			defer dc.close()
			// Docker exec/file hijacks bypass the HTTP connection pool.
			for range 2 {
				stream, err := dc.cli.Dialer()(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
			}
			var wg sync.WaitGroup
			for range 60 {
				wg.Go(func() {
					if _, err := dc.cli.ImageList(ctx, image.ListOptions{All: true}); err != nil {
						t.Errorf("concurrent Docker request: %v", err)
					}
				})
			}
			wg.Wait()
			if f.images.Load() != 60 || f.rejected.Load() != 0 || f.peak.Load() > sshNCMaxSessions {
				t.Fatalf("images=%d rejected=%d peak=%d", f.images.Load(), f.rejected.Load(), f.peak.Load())
			}
		})
	}
}

func TestSSHNCSlotWaitCancellationReleaseAndBridgeClose(t *testing.T) {
	f := startLimitedSSHServer(t, 10, 0)
	b := f.bridge(t)
	var held []net.Conn
	for range sshNCMaxSessions {
		conn, err := b.dial(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		defer conn.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.dial(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full quota must honor cancellation: %v", err)
	}
	if f.opens.Load() != sshNCMaxSessions {
		t.Fatalf("queued dial reached SSH server: %d", f.opens.Load())
	}
	_ = held[0].Close()
	_ = held[0].Close()
	replacement, err := b.dial(context.Background())
	if err != nil {
		t.Fatalf("closed slot was not returned: %v", err)
	}
	defer replacement.Close()
	waiter := make(chan error, 1)
	go func() { _, err := b.dial(context.Background()); waiter <- err }()
	b.close()
	select {
	case err := <-waiter:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("queued request on closed bridge: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing bridge did not release queued request")
	}
}

func TestSSHNCSessionRejectionsHaveBoundedRetries(t *testing.T) {
	t.Run("temporary rejection", func(t *testing.T) {
		f := startLimitedSSHServer(t, 10, 2)
		conn, err := f.bridge(t).dial(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if f.opens.Load() != 3 || f.commands.Load() != 1 {
			t.Fatalf("retry must precede command execution: opens=%d commands=%d", f.opens.Load(), f.commands.Load())
		}
	})
	t.Run("persistent rejection", func(t *testing.T) {
		f := startLimitedSSHServer(t, 0, 0)
		b := f.bridge(t)
		_, err := b.dial(context.Background())
		if err == nil || !strings.Contains(err.Error(), "MaxSessions") {
			t.Fatalf("expected actionable session-limit error: %v", err)
		}
		if f.opens.Load() != 4 || f.commands.Load() != 0 || len(b.slots) != 0 {
			t.Fatalf("unbounded retry, command replay or leaked slot: opens=%d commands=%d slots=%d", f.opens.Load(), f.commands.Load(), len(b.slots))
		}
	})
}

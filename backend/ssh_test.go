package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// startSSHServer 起一个同时提供 ecdsa 与 ed25519 主机密钥的最小 SSH 服务端。
// RFC 4253 规定主机密钥算法取「客户端偏好列表 ∩ 服务端列表」的首项，因此服务端
// 同时提供两种密钥就足以复现真实故障：协商到的类型与 known_hosts 记录的类型错开。
func startSSHServer(t *testing.T) (addr string, ed25519Key ssh.PublicKey) {
	t.Helper()

	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 host key: %v", err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPrivate)
	if err != nil {
		t.Fatalf("ed25519 signer: %v", err)
	}
	ecPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa host key: %v", err)
	}
	ecSigner, err := ssh.NewSignerFromKey(ecPrivate)
	if err != nil {
		t.Fatalf("ecdsa signer: %v", err)
	}

	serverCfg := &ssh.ServerConfig{NoClientAuth: true}
	serverCfg.AddHostKey(ecSigner)
	serverCfg.AddHostKey(edSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				serverConn, channels, requests, err := ssh.NewServerConn(conn, serverCfg)
				if err != nil {
					return
				}
				defer serverConn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.UnknownChannelType, "not supported in tests")
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})

	return listener.Addr().String(), edSigner.PublicKey()
}

func writeKnownHosts(t *testing.T, addr string, keys ...ssh.PublicKey) string {
	t.Helper()
	addresses := []string{knownhosts.Normalize(addr)}
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(knownhosts.Line(addresses, key))
		builder.WriteString("\n")
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// TestDialPrefersKeyTypeRecordedInKnownHosts 锁定真实故障（NAS/fnOS 场景）：
// 服务端同时提供 ecdsa 与 ed25519，known_hosts 里只有 ed25519（OpenSSH 生成的常见形态）。
// x/crypto 默认偏好 ECDSA（defaultHostKeyAlgos 中 ECDSA256/384/521 排在 ED25519 之前），
// 协商结果便与文件记录的 ed25519 错开，knownhosts.Check 返回 KeyError，连接直接失败。
// dialWithHostKeyFallback 必须能连上。
func TestDialPrefersKeyTypeRecordedInKnownHosts(t *testing.T) {
	addr, ed25519Key := startSSHServer(t)
	knownHostsPath := writeKnownHosts(t, addr, ed25519Key)

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		t.Fatalf("knownhosts.New: %v", err)
	}
	cfg := pluginConfig{SSHUsername: "tester"}

	// 1. 默认算法顺序应当失败——这正是修复前的线上表现。
	defaultOrder := &ssh.ClientConfig{
		User:            cfg.SSHUsername,
		HostKeyCallback: callback,
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, defaultOrder)
	if err == nil {
		client.Close()
		t.Fatal("expected the default host key preference to reject an ed25519-only known_hosts entry")
	}
	if !isHostKeyError(err) {
		t.Fatalf("expected a host key error from the default order, got: %v", err)
	}

	// 2. 带回退的顺序必须成功。
	client, err = dialWithHostKeyFallback(addr, cfg, nil, callback)
	if err != nil {
		t.Fatalf("dialWithHostKeyFallback should have matched the recorded ed25519 key, got: %v", err)
	}
	defer client.Close()
}

// TestDialStillFailsOnUnknownHostKey 保证回退不会削弱安全性：known_hosts 里没有该主机
// 任何密钥时，必须依然拒绝连接。
func TestDialStillFailsOnUnknownHostKey(t *testing.T) {
	addr, _ := startSSHServer(t)
	knownHostsPath := writeKnownHosts(t, addr)

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		t.Fatalf("knownhosts.New: %v", err)
	}
	client, err := dialWithHostKeyFallback(addr, pluginConfig{SSHUsername: "tester"}, nil, callback)
	if err == nil {
		client.Close()
		t.Fatal("expected a host with no known_hosts entry to be rejected")
	}
	if !isHostKeyError(err) {
		t.Fatalf("expected a host key error, got: %v", err)
	}
}

func TestDescribeSSHFailureIsActionable(t *testing.T) {
	sshHost := "192.168.31.2"
	addr := sshHost + ":22"
	path := "/home/user/.ssh/known_hosts"

	unknown := describeSSHFailure(&knownhosts.KeyError{}, sshHost, addr, path)
	if !strings.Contains(unknown.Error(), "ssh-keyscan 192.168.31.2") {
		t.Fatalf("unknown host key message should point at ssh-keyscan, got: %v", unknown)
	}
	if !strings.Contains(unknown.Error(), path) {
		t.Fatalf("unknown host key message should name the known_hosts file, got: %v", unknown)
	}

	other := &knownhosts.KeyError{}
	plain := describeSSHFailure(other, sshHost, addr, path)
	if !strings.Contains(plain.Error(), path) {
		t.Fatalf("mismatch message should name the known_hosts file, got: %v", plain)
	}

	// 非主机密钥类错误保持原样，不应被误报成 known_hosts 问题。
	network := describeSSHFailure(errText("dial tcp: i/o timeout"), sshHost, addr, path)
	if !strings.Contains(network.Error(), "Failed to connect SSH server "+addr) {
		t.Fatalf("network errors should keep the original wording, got: %v", network)
	}
	if strings.Contains(network.Error(), "ssh-keyscan") {
		t.Fatalf("network errors must not suggest ssh-keyscan, got: %v", network)
	}
}

// errText 让用例可以构造任意错误文案。
type errText string

func (e errText) Error() string { return string(e) }

func TestIsTunnelReset(t *testing.T) {
	cases := map[string]bool{
		// 用户实际看到的 Windows 报错：隧道本地端点重置连接。
		`error during connect: Head "http://127.0.0.1:2375/_ping": read tcp 127.0.0.1:56634->127.0.0.1:16657: wsarecv: An existing connection was forcibly closed by the remote host.`: true,
		"read tcp 127.0.0.1:1->127.0.0.1:2: read: connection reset by peer": true,
		"write tcp 127.0.0.1:1->127.0.0.1:2: write: broken pipe":            true,
		"unexpected EOF": true,
		// 超时不加隧道提示：可能是远端不可达/防火墙，断言 reset 会误导用户。
		"dial tcp 192.168.31.2:2375: i/o timeout":         false,
		"context deadline exceeded":                       false,
		"Failed to reach Docker daemon: 401 Unauthorized": false,
	}
	for message, want := range cases {
		if got := isTunnelReset(errText(message)); got != want {
			t.Errorf("isTunnelReset(%q) = %v, want %v", message, got, want)
		}
	}
}

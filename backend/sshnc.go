package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// sshNCBridge 在远程主机上执行 `nc -U <socket>`（可前缀 sudo -n），
// 把每一条到 Docker daemon 的连接桥接为一次独立的 SSH 会话。
// 等价于 dbx docker 分支的 unix-over-nc / unix-over-nc-sudo transport，
// 但 SSH 连接由插件后端直接建立（dbx 插件 Host API 无远程命令能力）。
type sshNCBridge struct {
	client *ssh.Client
	socket string
	sudo   bool
}

func dialSSHNC(ctx context.Context, cfg pluginConfig, password, socketPath string, sudo bool) (*sshNCBridge, error) {
	auth := []ssh.AuthMethod{}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	if cfg.SSHPrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.SSHPrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("Failed to read SSH private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			if password != "" {
				if s, err2 := ssh.ParsePrivateKeyWithPassphrase(key, []byte(password)); err2 == nil {
					signer = s
				} else {
					return nil, fmt.Errorf("Failed to parse SSH private key: %w", err)
				}
			} else {
				return nil, fmt.Errorf("Failed to parse SSH private key: %w", err)
			}
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if len(auth) == 0 {
		return nil, errors.New("Unix-over-NC requires an SSH password or private key")
	}

	knownHostsPath := cfg.SSHKnownHostsPath
	if knownHostsPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("Cannot locate SSH known_hosts: %w", err)
		}
		knownHostsPath = home + string(os.PathSeparator) + ".ssh" + string(os.PathSeparator) + "known_hosts"
	}
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("Cannot load SSH known_hosts %s: %w", knownHostsPath, err)
	}
	addr := net.JoinHostPort(cfg.SSHHost, fmt.Sprintf("%d", cfg.SSHPort))
	client, err := dialWithHostKeyFallback(addr, cfg, auth, hostKeyCallback)
	if err != nil {
		return nil, describeSSHFailure(err, cfg.SSHHost, addr, knownHostsPath)
	}
	return &sshNCBridge{client: client, socket: socketPath, sudo: sudo}, nil
}

// hostKeyAlgoOrders 是握手时依次尝试的主机密钥算法优先级。
//
// x/crypto 的默认顺序是 ECDSA 优先（defaultHostKeyAlgos 里 KeyAlgoECDSA256/384/521 排在
// KeyAlgoED25519 之前），而 OpenSSH 与它写进 known_hosts 的条目是 ed25519 优先。服务器
// 同时提供多种主机密钥时，协商结果就会与 known_hosts 实际记录的类型错开，
// knownhosts.Check 随即返回 KeyError（"knownhosts: key mismatch"）——即使记录本身完全正确。
// NAS（fnOS 等）上很常见：ssh-keyscan 能列出 rsa/ecdsa/ed25519 三种，用户文件里只有一种。
// 因此按「ed25519 优先 → x/crypto 默认 → RSA 优先」逐个尝试，覆盖三类常见记录。
var hostKeyAlgoOrders = [][]string{
	{ssh.KeyAlgoED25519, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521},
	nil, // nil 表示沿用 x/crypto 默认顺序
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoED25519},
}

func dialWithHostKeyFallback(addr string, cfg pluginConfig, auth []ssh.AuthMethod, hostKeyCallback ssh.HostKeyCallback) (*ssh.Client, error) {
	var lastErr error
	for _, algorithms := range hostKeyAlgoOrders {
		sshCfg := &ssh.ClientConfig{
			User:              cfg.SSHUsername,
			Auth:              auth,
			HostKeyCallback:   hostKeyCallback,
			HostKeyAlgorithms: algorithms,
			Timeout:           15 * time.Second,
		}
		client, err := ssh.Dial("tcp", addr, sshCfg)
		if err == nil {
			return client, nil
		}
		lastErr = err
		// 只有主机密钥校验失败才值得换算法重试；密码错误、网络不通换算法无用，
		// 而且重复认证可能触发对端 fail2ban。
		if !isHostKeyError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isHostKeyError(err error) bool {
	var keyErr *knownhosts.KeyError
	return errors.As(err, &keyErr) || strings.Contains(err.Error(), "knownhosts")
}

// describeSSHFailure 把主机密钥校验失败翻译成可操作的提示，而不是只丢一句 key mismatch。
func describeSSHFailure(err error, sshHost, addr, knownHostsPath string) error {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return fmt.Errorf("Failed to connect SSH server %s: %w", addr, err)
	}
	recorded := make([]string, 0, len(keyErr.Want))
	for _, known := range keyErr.Want {
		if known.Key != nil {
			recorded = append(recorded, known.Key.Type())
		}
	}
	if len(recorded) == 0 {
		return fmt.Errorf("SSH host key for %s is not in %s. Add it with \"ssh-keyscan %s >> %s\", or set \"SSH known_hosts path\" to a file that contains it", addr, knownHostsPath, sshHost, knownHostsPath)
	}
	return fmt.Errorf("SSH host key verification failed for %s: the key offered by the server does not match %s (which records %s). Refresh it with \"ssh-keyscan %s >> %s\", or set \"SSH known_hosts path\"", addr, knownHostsPath, strings.Join(recorded, ", "), sshHost, knownHostsPath)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (b *sshNCBridge) command() string {
	quoted := shellQuote(b.socket)
	if b.sudo {
		return "sudo -n -- nc -U " + quoted
	}
	return "nc -U " + quoted
}

// pipeConn 把 SSH 会话的 stdin/stdout 适配成 net.Conn。
type pipeConn struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	done    chan struct{}
	once    sync.Once
}

func (c *pipeConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *pipeConn) Close() error {
	var err error
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.session.Close()
		<-c.done
	})
	return err
}

type fakeAddr string

func (a fakeAddr) Network() string { return "ssh-nc" }
func (a fakeAddr) String() string  { return string(a) }

func (c *pipeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c *pipeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

func (b *sshNCBridge) dial(_ context.Context) (net.Conn, error) {
	session, err := b.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("Failed to open SSH session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.Stderr = io.Discard
	if err := session.Start(b.command()); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("Failed to start nc on remote host (needs nc -U support): %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = session.Wait()
		close(done)
	}()
	return &pipeConn{session: session, stdin: stdin, stdout: stdout, done: done}, nil
}

func (b *sshNCBridge) close() {
	_ = b.client.Close()
}

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/client"
)

// dockerClient 包装一条到 Docker Engine 的连接及其配置。
type dockerClient struct {
	cli     *client.Client
	conn    connectionPayload
	cfg     pluginConfig
	closer  func() // 关闭 nc-over-ssh 等拨号器持有的资源
	runtime runtimeEndpoint
}

func (d *dockerClient) close() {
	if d.closer != nil {
		d.closer()
	}
	if d.cli != nil {
		_ = d.cli.Close()
	}
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(h, "localhost") || h == "" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// hasEnabledTunnel 判断 dbx 宿主是否为该连接建立了传输层隧道；
// 有隧道时 runtime 字段携带本地回环端点。
func hasEnabledTunnel(c connectionPayload) bool {
	for _, layer := range c.TransportLayers {
		t, _ := layer["type"].(string)
		enabled, ok := layer["enabled"].(bool)
		if t != "" && (!ok || enabled) {
			return true
		}
	}
	return false
}

// validateConfig 在拨号前校验逻辑目标和宿主提供的实际端点。
func validateConfig(c connectionPayload, cfg pluginConfig, runtime runtimeEndpoint) error {
	switch cfg.Protocol {
	case "npipe":
		if !namedPipeSupported() {
			return errors.New("Docker Named Pipe connections are only supported on Windows")
		}
		if hasEnabledTunnel(c) {
			return errors.New("Named Pipe connections cannot use Tunnel / Proxy")
		}
		if !strings.HasPrefix(cfg.PipePath, "//./pipe/") || len(cfg.PipePath) <= len("//./pipe/") || strings.ContainsAny(strings.TrimPrefix(cfg.PipePath, "//./pipe/"), "/\\\x00\r\n") {
			return errors.New("Named Pipe path must be //./pipe/<name>")
		}
	case "http", "https":
		if strings.TrimSpace(c.Host) == "" {
			return errors.New("Docker HTTP/HTTPS connections require a host")
		}
		if c.Port < 0 || c.Port > 65535 {
			return errors.New("Docker port must be between 1 and 65535")
		}
		// 有隧道时缺少 runtime 不能回退直连；部分端点也不能静默忽略。
		if hasEnabledTunnel(c) || runtime.Host != "" || runtime.Port != 0 {
			if strings.TrimSpace(runtime.Host) == "" || runtime.Port < 1 || runtime.Port > 65535 {
				return errors.New("DBX did not provide a valid runtime endpoint. Check Tunnel / Proxy settings and reconnect; direct fallback is disabled")
			}
		}
		if cfg.Protocol == "http" && !isLoopbackHost(c.Host) && !hasEnabledTunnel(c) && !cfg.AllowInsecureRemoteHTTP {
			return errors.New("Remote Docker HTTP is disabled. Enable insecure remote HTTP explicitly, or use HTTPS or an SSH tunnel.")
		}
	case "unix", "unix-over-nc", "unix-over-nc-sudo":
		if hasEnabledTunnel(c) {
			return errors.New("Socket connections cannot use DBX TCP transport layers. Clear Tunnel / Proxy for this connection, or select HTTP/HTTPS to use a DBX tunnel")
		}
		if !strings.HasPrefix(cfg.SocketPath, "/") || strings.ContainsAny(cfg.SocketPath, "\x00\n\r") {
			return fmt.Errorf("Invalid Docker socket path: %q", cfg.SocketPath)
		}
		if cfg.Protocol == "unix" {
			// Unix 直连依赖 docker SDK 的 unix transport。
		} else {
			if strings.TrimSpace(cfg.SSHHost) == "" {
				return errors.New("Unix-over-NC requires an SSH host (see the connection form)")
			}
			if strings.TrimSpace(cfg.SSHUsername) == "" {
				return errors.New("SSH connections require a username")
			}
			if cfg.SSHPort < 1 || cfg.SSHPort > 65535 {
				return errors.New("SSH port must be between 1 and 65535")
			}
		}
	default:
		return fmt.Errorf("Unsupported Docker protocol: %s", cfg.Protocol)
	}
	if cfg.APIVersion != "" && cfg.APIVersion != "auto" {
		v := strings.TrimPrefix(cfg.APIVersion, "v")
		var major, minor int
		if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil || major <= 0 {
			return fmt.Errorf("Invalid Docker API version: %q", cfg.APIVersion)
		}
		if major == 1 && minor < 24 {
			return errors.New("Docker API versions older than 1.24 are not supported")
		}
	}
	return nil
}

func buildTLSConfig(cfg pluginConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPath != "" {
		pem, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("Failed to read Docker CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("Docker CA certificate is not a valid PEM file")
		}
		tlsCfg.RootCAs = pool
	}
	if (cfg.ClientCertPath == "") != (cfg.ClientKeyPath == "") {
		return nil, errors.New("Docker HTTPS requires both a client certificate and private key")
	}
	if cfg.ClientCertPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertPath, cfg.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("Failed to load Docker client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

// connect 按协议构造 docker client 并完成版本协商 + ping。
func connect(ctx context.Context, c connectionPayload, runtime runtimeEndpoint) (*dockerClient, error) {
	cfg := c.config()
	if err := validateConfig(c, cfg, runtime); err != nil {
		return nil, err
	}

	opts := []client.Opt{}
	var closer func()

	// 自定义拨号集合：隧道与 nc 桥都会改写拨号目标，最终用统一的
	// WithHTTPClient(Transport{DialContext}) 组装。官方 SDK 的 Dialer()（hijack
	// 流：exec attach、日志 follow 都用它）在 baseTransport.DialContext 为空时
	// 退化为 net.Dial(cli.proto,…)，而 http/https 协议的 proto 是 "http"/"https"，
	// 直接报 "dial http: unknown network http"。因此所有 TCP 类协议都必须显式
	// 提供拨号器，不能只依赖 WithHost 的默认传输配置。
	var dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	var tlsCfg *tls.Config

	switch cfg.Protocol {
	case "npipe":
		opts = append(opts, client.WithHost("npipe://"+cfg.PipePath))
	case "http", "https":
		host := strings.TrimSpace(c.Host)
		port := c.Port
		// dbx 隧道激活时拨本地回环端点，HTTP Host/TLS SNI 保持原始主机。
		if runtime.Host != "" && runtime.Port > 0 {
			dialTarget := net.JoinHostPort(runtime.Host, fmt.Sprintf("%d", runtime.Port))
			dialer := &net.Dialer{Timeout: 15 * time.Second}
			dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", dialTarget)
			}
		}
		if port == 0 {
			if cfg.Protocol == "https" {
				port = 2376
			} else {
				port = 2375
			}
		}
		scheme := cfg.Protocol
		hostURL := fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, fmt.Sprintf("%d", port)))
		opts = append(opts, client.WithHost(hostURL))
		if scheme == "https" {
			cfgTLS, err := buildTLSConfig(cfg)
			if err != nil {
				return nil, err
			}
			tlsCfg = cfgTLS
		}
		if dialContext == nil {
			dialer := &net.Dialer{Timeout: 15 * time.Second}
			dialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", addr)
			}
		}

	case "unix":
		opts = append(opts, client.WithHost("unix://"+cfg.SocketPath))

	case "unix-over-nc", "unix-over-nc-sudo":
		bridge, err := dialSSHNC(ctx, cfg, c.secret("ssh_password"), cfg.SocketPath, cfg.Protocol == "unix-over-nc-sudo")
		if err != nil {
			return nil, err
		}
		closer = bridge.close
		dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return bridge.dial(ctx)
		}
		opts = append(opts, client.WithHost("http://docker-over-ssh"))
	}

	if dialContext != nil || tlsCfg != nil {
		transport := &http.Transport{TLSClientConfig: tlsCfg, DialContext: dialContext}
		opts = append(opts, client.WithHTTPClient(&http.Client{Transport: transport}))
	}

	if cfg.APIVersion != "" && cfg.APIVersion != "auto" {
		opts = append(opts, client.WithVersion(strings.TrimPrefix(cfg.APIVersion, "v")))
	} else {
		opts = append(opts, client.WithAPIVersionNegotiation())
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		if closer != nil {
			closer()
		}
		return nil, err
	}

	dc := &dockerClient{cli: cli, conn: c, cfg: cfg, closer: closer, runtime: runtime}

	// 分支行为：连接建立即 ping 验证。
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := cli.Ping(pingCtx); err != nil {
		dc.close()
		// 隧道场景下的 reset/EOF 几乎总是「远端拒绝 TCP 转发」：DBX 的隧道在
		// direct-tcpip 被 sshd 以 Administratively prohibited 拒绝后立刻关闭本地
		// 监听上的连接，客户端只能看到连接被重置，得不到任何 Docker 响应。
		// 典型触发条件是 NAS 出厂 sshd 配置里的 AllowTcpForwarding no。
		if (cfg.Protocol == "http" || cfg.Protocol == "https") && hasEnabledTunnel(c) && isTunnelReset(err) {
			return nil, fmt.Errorf("Failed to reach Docker daemon: %w. The DBX SSH tunnel was closed before the daemon could answer; the remote sshd most likely refuses TCP forwarding (AllowTcpForwarding no, common on NAS systems). Enable exec-channel proxying for the SSH hop in DBX, or switch this connection to the \"Unix socket over SSH\" protocol", err)
		}
		return nil, fmt.Errorf("Failed to reach Docker daemon: %w", err)
	}
	return dc, nil
}

// isTunnelReset 判断错误是否为「隧道本地端点把连接重置」。
func isTunnelReset(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"forcibly closed", "connection reset", "broken pipe", "eof"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

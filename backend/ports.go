package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var browserHostname = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

// Resolve the published application port, never reuse the Engine tunnel port.
func containerPortURL(conn connectionPayload, ip string, public, private int) (string, error) {
	if public < 1 || public > 65535 || private < 1 || private > 65535 {
		return "", errors.New("Invalid published port")
	}
	cfg := conn.config()
	host := strings.Trim(conn.Host, "[]")
	switch cfg.Protocol {
	case "unix", "npipe":
		host = "127.0.0.1"
	case "unix-over-nc", "unix-over-nc-sudo":
		host = cfg.SSHHost
	}
	if hasEnabledTunnel(conn) && isLoopbackHost(host) {
		return "", errors.New("The Engine tunnel does not forward application ports. Open this service through its own accessible address or port forward.")
	}
	if isLoopbackHost(ip) && !isLoopbackHost(host) {
		return "", errors.New("This application port is bound to the remote host's loopback interface; forward the application port first.")
	}
	if ip != "" && ip != "0.0.0.0" && ip != "::" {
		host = strings.Trim(ip, "[]")
	}
	if net.ParseIP(host) == nil && !browserHostname.MatchString(host) {
		return "", errors.New("No valid browser-accessible host for this connection")
	}
	scheme := "http"
	if private == 443 || public == 443 || public == 8443 {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(public))}).String(), nil
}

func (p *plugin) openContainerPort(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		PublicPort  int    `json:"publicPort"`
		PrivatePort int    `json:"privatePort"`
		IP          string `json:"ip"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ContainerID == "" {
		return nil, errInvalidParams
	}
	ctx, cancel := context.WithTimeout(sess.context(), 15*time.Second)
	defer cancel()
	inspected, err := sess.client.cli.ContainerInspect(ctx, in.ContainerID)
	if err != nil {
		return nil, err
	}
	matched := false
	if inspected.NetworkSettings != nil {
		for port, bindings := range inspected.NetworkSettings.Ports {
			if port.Proto() != "tcp" || port.Int() != in.PrivatePort {
				continue
			}
			for _, binding := range bindings {
				if binding.HostPort == strconv.Itoa(in.PublicPort) && binding.HostIP == in.IP {
					matched = true
				}
			}
		}
	}
	if !matched {
		return nil, errors.New("Published TCP port has changed; refresh the container list")
	}
	address, err := containerPortURL(sess.conn, in.IP, in.PublicPort, in.PrivatePort)
	if err != nil {
		return nil, err
	}
	if err = launchBrowser(address); err != nil {
		return nil, err
	}
	return map[string]any{"url": address}, nil
}

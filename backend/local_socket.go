package main

import "strings"

const (
	defaultSocketPath = "/var/run/docker.sock"
	defaultPipePath   = "//./pipe/docker_engine"
)

// Local mode chooses a fixed OS default. Hidden paths from a previously selected
// protocol must not redirect this connection to another daemon. Custom paths
// remain available through the explicit Unix Socket / Named Pipe modes.
func resolveLocalSocket(cfg pluginConfig) pluginConfig {
	if cfg.Protocol == "local" {
		if namedPipeSupported() {
			cfg.Protocol, cfg.PipePath = "npipe", defaultPipePath
		} else {
			cfg.Protocol, cfg.SocketPath = "unix", defaultSocketPath
		}
	}
	// Accept both Windows spellings; validateConfig still rejects remote pipes,
	// empty names, nested paths and control characters before any dial.
	cfg.PipePath = strings.ReplaceAll(cfg.PipePath, `\`, "/")
	return cfg
}

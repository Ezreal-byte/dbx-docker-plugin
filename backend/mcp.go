package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type callToolResult struct {
	Content []map[string]any `json:"content"`
	IsError bool             `json:"isError"`
}

func toolResult(value any, err error) callToolResult {
	text := ""
	if err != nil {
		text = err.Error()
	} else {
		data, _ := json.Marshal(sanitizeToolData(value))
		text = string(data)
	}
	const max = 192 * 1024
	if len(text) > max {
		text = string(jsonBytes(map[string]any{"truncated": true, "message": "Result exceeds 192 KiB; use smaller limit/offset or a specific resource"}))
	}
	return callToolResult{[]map[string]any{{"type": "text", "text": text}}, err != nil}
}
func hashValue(v any) string { sum := sha256.Sum256(jsonBytes(v)); return hex.EncodeToString(sum[:]) }
func (p *plugin) mcpTools(id string) any {
	allowed := map[string]bool{}
	_ = p.lockedState(false, func(s *automationState) error {
		for key, policy := range s.Policies {
			if (id == "" || key == id) && policy.Enabled {
				for _, t := range policy.AllowedTools {
					allowed[t] = true
				}
			}
		}
		return nil
	})
	p.mu.RLock()
	sess := p.sessions[id]
	readOnly := sess != nil && sess.conn.effectiveReadOnly()
	p.mu.RUnlock()
	out := []toolDefinition{}
	for _, t := range toolCatalog() {
		if allowed[t.Name] && (!readOnly || t.Group == "query") {
			out = append(out, t)
		}
	}
	return map[string]any{"tools": out}
}

func (p *plugin) mcpCall(raw json.RawMessage) (result callToolResult) {
	var in struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
		Lifecycle params         `json:"lifecycle"`
	}
	if len(raw) > 256*1024 || json.Unmarshal(raw, &in) != nil {
		return toolResult(nil, errInvalidParams)
	}
	id := in.Lifecycle.Connection.ID
	if len(in.Tool) > 64 || len(id) > 256 {
		return toolResult(nil, errInvalidParams)
	}
	outcome := "rejected"
	defer func() { p.audit(id, in.Tool, outcome) }()
	t, ok := findTool(in.Tool)
	if !ok {
		return toolResult(nil, errors.New("Unknown Docker tool"))
	}
	if in.Arguments == nil {
		in.Arguments = map[string]any{}
	}
	forwarded := false
	if v, exists := in.Arguments["_dbxDockerForwarded"]; exists {
		if v != true {
			return toolResult(nil, errInvalidParams)
		}
		forwarded = true
		delete(in.Arguments, "_dbxDockerForwarded")
	}
	if err := validateValue(t.InputSchema, in.Arguments, "arguments"); err != nil {
		return toolResult(nil, err)
	}
	if id == "" || (in.Lifecycle.ConnectionID != "" && in.Lifecycle.ConnectionID != id) {
		return toolResult(nil, errors.New("A matching host lifecycle connection is required"))
	}
	if in.Lifecycle.Connection.PluginID != "" && in.Lifecycle.Connection.PluginID != pluginID {
		return toolResult(nil, errors.New("Connection is not bound to this plugin"))
	}
	if err := p.policyAllows(id, t.Name); err != nil {
		return toolResult(nil, err)
	}
	if t.Group != "query" && in.Lifecycle.Connection.effectiveReadOnly() {
		return toolResult(nil, errors.New("Read-only connection rejects modifications"))
	}
	if t.Name == "apply_compose" {
		if req, _ := in.Arguments["request"].(map[string]any); req["replaceExisting"] == true {
			if err := p.policyAllows(id, "remove_container"); err != nil {
				return toolResult(nil, errors.New("Replacing Compose containers also requires remove_container permission"))
			}
		}
	}
	// Persisted task/status operations do not need an engine connection.
	if strings.HasPrefix(t.Method, "docker/task") || t.Method == "docker/auditList" {
		value, err := p.taskRPC(t.Method, id, jsonBytes(in.Arguments))
		if err == nil {
			outcome = "success"
		}
		return toolResult(value, err)
	}
	sess, err := p.automationSession(in.Lifecycle)
	if errors.Is(err, errTunnelSession) {
		if forwarded {
			return toolResult(nil, err)
		}
		result, err = p.forwardToDesktop(id, t.Name, in.Arguments)
		if err == nil {
			outcome = "forwarded"
			return result
		}
		return toolResult(nil, err)
	}
	if err != nil {
		return toolResult(nil, redactedError(err, in.Lifecycle.Connection))
	}
	value, err := p.runTool(sess, t, in.Arguments, true)
	if err == nil {
		outcome = "success"
	}
	return toolResult(value, redactedError(err, sess.conn))
}

var errTunnelSession = errors.New("No active DBX tunnel session. Open this connection's Docker workbench and retry; direct fallback is disabled")

func validTunnelRuntime(c connectionPayload, rt runtimeEndpoint) bool {
	return rt.Host != "" && rt.Port > 0 && rt.Port <= 65535 && isLoopbackHost(rt.Host) && !(rt.Host == c.Host && rt.Port == c.Port)
}
func sameConnection(a, b connectionPayload) bool {
	// Policy flags and display metadata may change without changing the endpoint.
	a.ReadOnly = false
	a.IsProduction = false
	a.Name = ""
	a.Color = ""
	b.ReadOnly = false
	b.IsProduction = false
	b.Name = ""
	b.Color = ""
	clean := func(c *connectionPayload) {
		cfg := map[string]any{}
		for k, v := range c.ExternalConfig {
			if k != "read_only" && k != "is_production" {
				cfg[k] = v
			}
		}
		c.ExternalConfig = cfg
	}
	clean(&a)
	clean(&b)
	return hashValue(a) == hashValue(b)
}
func (p *plugin) automationSession(v params) (*session, error) {
	c := v.Connection
	p.mu.RLock()
	cached := p.sessions[c.ID]
	p.mu.RUnlock()
	if cached != nil && !sameConnection(c, cached.conn) {
		return nil, errors.New("Connection configuration changed; reopen its Docker workbench")
	}
	if hasEnabledTunnel(c) {
		if cached != nil && validTunnelRuntime(c, cached.client.runtime) {
			return &session{client: cached.client, conn: c, parent: cached}, nil
		}
		if !validTunnelRuntime(c, v.Runtime) {
			return nil, errTunnelSession
		}
	}
	base, err := p.sessionFor(c.ID, c, v.Runtime)
	if err != nil {
		return nil, err
	}
	return &session{client: base.client, conn: c, parent: base}, nil
}

func (p *plugin) forwardToDesktop(id, tool string, args map[string]any) (callToolResult, error) {
	// DBX resolves a portable data directory too. Derive from its injected plugin
	// data directory instead of guessing a user's profile or reading credentials.
	root := os.Getenv("DBX_APP_DATA_DIR")
	if root == "" {
		root = filepath.Dir(filepath.Dir(p.dataDir))
	}
	data, err := os.ReadFile(filepath.Join(root, "mcp-bridge-port"))
	if err != nil {
		return callToolResult{}, errTunnelSession
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port < 1 || port > 65535 {
		return callToolResult{}, errTunnelSession
	}
	forwarded := map[string]any{}
	for k, v := range args {
		forwarded[k] = v
	}
	forwarded["_dbxDockerForwarded"] = true
	body := jsonBytes(map[string]any{"plugin_id": pluginID, "connection_id": id, "tool": tool, "arguments": forwarded, "timeout_ms": 110000})
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/call-plugin-tool", port), bytes.NewReader(body))
	if err != nil {
		return callToolResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 115 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return callToolResult{}, errTunnelSession
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return callToolResult{}, fmt.Errorf("DBX desktop bridge rejected the tool (HTTP %d); open the workbench and check DBX MCP policy", resp.StatusCode)
	}
	var result callToolResult
	if err = json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&result); err != nil || result.Content == nil {
		return callToolResult{}, errors.New("Invalid DBX desktop bridge response")
	}
	return result, nil
}

func (p *plugin) runTool(sess *session, t toolDefinition, args map[string]any, automation bool) (any, error) {
	if automation {
		sess = &session{client: sess.client, conn: sess.conn, parent: sess, ctx: sess.context(), automated: true}
	}
	if t.Group != "query" {
		if err := p.ensureWritable(sess, t.Name); err != nil {
			return nil, err
		}
	}
	if t.Destructive {
		token, _ := args["previewToken"].(string)
		delete(args, "previewToken")
		if token == "" {
			return p.prepareMutation(sess, t, args)
		}
		if err := p.consumePreview(sess, t, args, token); err != nil {
			return nil, err
		}
	}
	if t.Long {
		return p.startTask(sess, t, args, automation)
	}
	if t.Group != "query" {
		return p.executeMutation(sess, t, args)
	}
	return p.executeTool(sess, t, args)
}
func (p *plugin) executeTool(sess *session, t toolDefinition, args map[string]any) (any, error) {
	if t.Name == "prune_resources" {
		return p.pruneExact(sess, args)
	}
	if t.Name == "pull_image" {
		return p.pullImageTask(sess, args)
	}
	if t.Name == "list_containers" {
		if _, ok := args["all"]; !ok {
			args["all"] = true
		}
	}
	value, err := p.dispatchSession(t.Method, sess, jsonBytes(args), nil)
	if err != nil {
		return nil, err
	}
	if t.Name == "engine_info" {
		details := value.(DockerEngineDetails)
		return map[string]any{"summary": details.Summary, "version": details.Version}, nil
	}
	if strings.HasPrefix(t.Name, "list_") {
		var list []any
		if json.Unmarshal(jsonBytes(value), &list) != nil {
			return nil, errors.New("Invalid list response")
		}
		offset, limit := 0, 100
		if n, ok := args["offset"].(float64); ok {
			offset = int(n)
		}
		if n, ok := args["limit"].(float64); ok {
			limit = int(n)
		}
		start := min(offset, len(list))
		end := min(start+limit, len(list))
		return map[string]any{"items": list[start:end], "total": len(list), "offset": start, "nextOffset": end, "hasMore": end < len(list)}, nil
	}
	return value, nil
}
func redactedError(err error, c connectionPayload) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, value := range c.Secrets {
		if value != "" {
			s = strings.ReplaceAll(s, value, "[redacted]")
		}
	}
	if len(s) > 2048 {
		s = s[:2048] + "…"
	}
	return errors.New(s)
}
func sanitizeToolData(value any) any {
	var decoded any
	if json.Unmarshal(jsonBytes(value), &decoded) != nil {
		return nil
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, item := range x {
				key := strings.ToLower(strings.ReplaceAll(k, "_", ""))
				if slices.Contains([]string{"env", "environment", "command", "cmd", "args", "entrypoint", "createdby", "labels", "options", "driveropts", "driveroptions", "log", "test", "registryconfig", "authorization", "authconfig", "logconfig"}, key) || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "credential") || strings.Contains(key, "token") || strings.Contains(key, "privatekey") {
					if key != "previewtoken" {
						continue
					}
				}
				out[k] = walk(item)
			}
			return out
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		default:
			return v
		}
	}
	return walk(decoded)
}

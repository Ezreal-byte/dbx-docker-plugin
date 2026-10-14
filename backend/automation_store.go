package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type automationPolicy struct {
	Enabled      bool     `json:"enabled"`
	AllowedTools []string `json:"allowedTools"`
	Revision     int      `json:"revision"`
}
type previewRecord struct {
	ConnectionID  string    `json:"connectionId"`
	Operation     string    `json:"operation"`
	ArgumentsHash string    `json:"argumentsHash"`
	SnapshotHash  string    `json:"snapshotHash"`
	ExpiresAt     time.Time `json:"expiresAt"`
}
type taskRecord struct {
	ID              string    `json:"id"`
	ConnectionID    string    `json:"connectionId"`
	Tool            string    `json:"tool"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	CancelRequested bool      `json:"cancelRequested"`
	Result          any       `json:"result,omitempty"`
	Error           string    `json:"error,omitempty"`
}
type auditRecord struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connectionId"`
	Tool         string    `json:"tool"`
	Outcome      string    `json:"outcome"`
}
type automationState struct {
	Version  int                         `json:"version"`
	Policies map[string]automationPolicy `json:"policies"`
	Previews map[string]previewRecord    `json:"previews"`
	Tasks    map[string]taskRecord       `json:"tasks"`
	Audit    []auditRecord               `json:"audit"`
}

// The lock covers read/modify/replace across desktop and external MCP processes.
// A separate lock file remains stable when the JSON file is atomically replaced.
func (p *plugin) lockedState(write bool, fn func(*automationState) error) error {
	if p.dataDir == "" {
		return errors.New("DBX_PLUGIN_DATA_DIR is missing; AI/MCP permissions are disabled")
	}
	if err := os.MkdirAll(p.dataDir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.dataDir, "automation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if tryFileLock(f) {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("Automation settings are locked; retry")
		}
		time.Sleep(15 * time.Millisecond)
	}
	defer unlockFile(f)
	state := automationState{Version: 1, Policies: map[string]automationPolicy{}, Previews: map[string]previewRecord{}, Tasks: map[string]taskRecord{}, Audit: []auditRecord{}}
	path := filepath.Join(p.dataDir, "automation.json")
	file, err := os.Open(path)
	if err == nil {
		data, e := io.ReadAll(io.LimitReader(file, 8*1024*1024+1))
		file.Close()
		if e != nil || len(data) > 8*1024*1024 || json.Unmarshal(data, &state) != nil || state.Version != 1 || state.Policies == nil || state.Previews == nil || state.Tasks == nil {
			return errors.New("Automation settings cannot be read; permissions are disabled (restore automation.json from backup)")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("Automation settings cannot be read; permissions are disabled: %w", err)
	}
	if err = fn(&state); err != nil {
		return err
	}
	if !write {
		return nil
	}
	for id, v := range state.Previews {
		if time.Now().After(v.ExpiresAt) {
			delete(state.Previews, id)
		}
	}
	if len(state.Audit) > 200 {
		state.Audit = state.Audit[len(state.Audit)-200:]
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > 8*1024*1024 {
		return errors.New("Automation store capacity exceeded")
	}
	tmp, err := os.CreateTemp(p.dataDir, ".automation-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0600)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmp.Name(), path)
}

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (p *plugin) policyAllows(id, tool string) error {
	return p.lockedState(false, func(s *automationState) error {
		v := s.Policies[id]
		if id == "" || !v.Enabled || !slices.Contains(v.AllowedTools, tool) {
			return errors.New("AI/MCP tool is not authorized for this connection; configure AI/MCP permissions in the Docker workbench")
		}
		return nil
	})
}
func (p *plugin) settingsGet(id string) (any, error) {
	if id == "" {
		return nil, errors.New("connectionId is required")
	}
	policy := automationPolicy{AllowedTools: []string{}}
	err := p.lockedState(false, func(s *automationState) error {
		if v, ok := s.Policies[id]; ok {
			policy = v
		}
		return nil
	})
	out := map[string]any{"connectionId": id, "policy": policy, "tools": toolCatalog()}
	if err != nil {
		out["policy"] = automationPolicy{AllowedTools: []string{}}
		out["warning"] = err.Error()
	}
	return out, nil
}
func (p *plugin) settingsSet(raw json.RawMessage) (any, error) {
	var in struct {
		ConnectionID string   `json:"connectionId"`
		Enabled      bool     `json:"enabled"`
		AllowedTools []string `json:"allowedTools"`
		Revision     int      `json:"revision"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ConnectionID == "" {
		return nil, errInvalidParams
	}
	p.mu.RLock()
	sess := p.sessions[in.ConnectionID]
	p.mu.RUnlock()
	if sess == nil {
		return nil, errors.New("Open this connection's workbench before editing AI/MCP permissions")
	}
	allowed := []string{}
	for _, name := range in.AllowedTools {
		t, ok := findTool(name)
		if !ok {
			return nil, fmt.Errorf("Unknown tool: %s", name)
		}
		if sess.conn.effectiveReadOnly() && t.Group != "query" {
			return nil, errors.New("Read-only connections cannot authorize modifications")
		}
		if !slices.Contains(allowed, name) {
			allowed = append(allowed, name)
		}
	}
	err := p.lockedState(true, func(s *automationState) error {
		prev := s.Policies[in.ConnectionID]
		if prev.Revision != in.Revision {
			return errors.New("Permissions changed in another window; reload before saving")
		}
		// First enable, including re-enable, always starts with queries only.
		if in.Enabled && !prev.Enabled {
			allowed = nil
			for _, t := range toolCatalog() {
				if t.Group == "query" {
					allowed = append(allowed, t.Name)
				}
			}
		}
		s.Policies[in.ConnectionID] = automationPolicy{Enabled: in.Enabled, AllowedTools: allowed, Revision: prev.Revision + 1}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p.settingsGet(in.ConnectionID)
}
func (p *plugin) audit(id, tool, outcome string) {
	_ = p.lockedState(true, func(s *automationState) error {
		s.Audit = append(s.Audit, auditRecord{time.Now().UTC(), id, tool, outcome})
		return nil
	})
}

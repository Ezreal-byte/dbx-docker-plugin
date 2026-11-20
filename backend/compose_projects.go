package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/docker/docker/api/types/container"
)

const composeSourceLimit = 128 * 1024

type composeProject struct {
	Name       string   `json:"name"`
	Directory  string   `json:"directory"`
	Files      []string `json:"files"`
	Containers int      `json:"containers"`
	Running    int      `json:"running"`
	Conflict   bool     `json:"conflict"`
}

// Remember paths, never YAML or .env contents; a removed project can be deployed
// again. The connection configuration hash prevents reuse after retargeting.
func composeRegistryKey(s *session) string { return hashValue(s.conn) }

func (p *plugin) composeProjects(sess *session) ([]composeProject, error) {
	ctx, cancel := context.WithTimeout(sess.context(), 20*time.Second)
	defer cancel()
	items, err := sess.client.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	projects := map[string]composeProject{}
	err = p.lockedState(false, func(s *automationState) error {
		for name, project := range s.Compose[composeRegistryKey(sess)] {
			project.Containers, project.Running = 0, 0
			projects[name] = project
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range items {
		name := c.Labels[labelProject]
		if name == "" {
			continue
		}
		project := composeProject{Name: name, Directory: c.Labels["com.docker.compose.project.working_dir"], Files: []string{}}
		for _, file := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
			if strings.TrimSpace(file) != "" {
				project.Files = append(project.Files, strings.TrimSpace(file))
			}
		}
		previous := projects[name]
		if seen[name] {
			project.Conflict = previous.Conflict || project.Directory != previous.Directory || hashValue(project.Files) != hashValue(previous.Files)
			project.Containers, project.Running = previous.Containers, previous.Running
		}
		project.Containers++
		if string(c.State) == "running" {
			project.Running++
		}
		projects[name] = project
		seen[name] = true
	}
	if len(projects) > 200 {
		return nil, errors.New("More than 200 Compose projects; narrow this connection's scope")
	}
	err = p.lockedState(true, func(s *automationState) error {
		if s.Compose == nil {
			s.Compose = map[string]map[string]composeProject{}
		}
		s.Compose[composeRegistryKey(sess)] = projects
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []composeProject{}
	for _, project := range projects {
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (p *plugin) findComposeProject(sess *session, name string) (composeProject, error) {
	projects, err := p.composeProjects(sess)
	if err != nil {
		return composeProject{}, err
	}
	for _, project := range projects {
		if project.Name == name {
			if project.Conflict {
				return project, errors.New("Project containers disagree on Compose paths; resolve labels before managing the project")
			}
			if _, err := validateProjectName(name); err != nil {
				return project, err
			}
			if len(project.Files) == 0 || len(project.Files) > 8 || !validComposeHostPath(project.Directory, sess.client.ssh != nil) {
				return project, errors.New("Original Compose paths are unavailable; import YAML explicitly instead")
			}
			for _, f := range project.Files {
				if !validComposeHostPath(f, sess.client.ssh != nil) {
					return project, errors.New("Compose file paths must be absolute host paths")
				}
			}
			tcp := sess.client.cfg.Protocol == "http" || sess.client.cfg.Protocol == "https"
			if sess.client.ssh == nil && (hasEnabledTunnel(sess.conn) || (tcp && !isLoopbackHost(sess.conn.Host))) {
				return project, errors.New("Remote TCP/tunnel connections cannot access daemon host files; use SSH socket mode or import YAML")
			}
			return project, nil
		}
	}
	return composeProject{}, errors.New("Compose project not found; refresh the list")
}

func validComposeHostPath(path string, remote bool) bool {
	if strings.ContainsAny(path, "\x00\r\n") || len(path) > 4096 {
		return false
	}
	if remote {
		return strings.HasPrefix(path, "/")
	}
	return filepath.IsAbs(path)
}

// Output is bounded even when a command emits continuously.
type boundedOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	max       int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) > b.max-b.data.Len() {
		p = p[:b.max-b.data.Len()]
		b.truncated = true
	}
	b.data.Write(p)
	return n, nil
}

func composeCLIArgs(sess *session, project composeProject, stdin bool) []string {
	args := []string{"--host", sess.client.cli.DaemonHost()}
	if sess.client.ssh != nil {
		args[1] = "unix://" + sess.client.cfg.SocketPath
	}
	if sess.client.cfg.Protocol == "https" {
		args = append(args, "--tlsverify")
		for flag, value := range map[string]string{"--tlscacert": sess.client.cfg.CACertPath, "--tlscert": sess.client.cfg.ClientCertPath, "--tlskey": sess.client.cfg.ClientKeyPath} {
			if value != "" {
				args = append(args, flag, value)
			}
		}
	}
	args = append(args, "compose", "--ansi", "never", "--project-name", project.Name, "--project-directory", project.Directory)
	if stdin {
		return append(args, "--file", "-")
	}
	for _, file := range project.Files {
		args = append(args, "--file", file)
	}
	return args
}

func hostCommand(ctx context.Context, sess *session, dir string, args []string, input string, max int) ([]byte, error) {
	out := &boundedOutput{max: max}
	stderr := &boundedOutput{max: 4096}
	if bridge := sess.client.ssh; bridge != nil {
		select {
		case bridge.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-bridge.done:
			return nil, errors.New("SSH disconnected")
		}
		ready := make(chan error, 1)
		go func() {
			defer func() { <-bridge.slots }()
			channel, err := bridge.client.NewSession()
			if err != nil {
				ready <- err
				return
			}
			defer channel.Close()
			if ctx.Err() != nil {
				ready <- ctx.Err()
				return
			}
			channel.Stdin = strings.NewReader(input)
			channel.Stdout = out
			channel.Stderr = stderr
			command := "cd -- " + shellQuote(dir) + " && "
			if bridge.sudo {
				command += "sudo -n "
			}
			parts := []string{}
			for _, arg := range args {
				parts = append(parts, shellQuote(arg))
			}
			command += strings.Join(parts, " ")
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					channel.Close()
				case <-done:
				}
			}()
			ready <- channel.Run(command)
		}()
		select {
		case err := <-ready:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				return nil, fmt.Errorf("Host command failed (%v); verify Docker Compose v2 and host file permissions", err)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		hideHostCommand(cmd)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(input)
		cmd.Stdout = out
		cmd.Stderr = stderr
		// An explicit daemon host wins over ambient Docker contexts and TLS vars.
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "DOCKER_") && !strings.HasPrefix(entry, "COMPOSE_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("Host command failed (%v); verify Docker Compose v2 and host file permissions", err)
		}
	}
	if out.truncated {
		return nil, errors.New("Host command output exceeds size limit")
	}
	return out.data.Bytes(), nil
}

func readComposeHostFile(ctx context.Context, sess *session, project composeProject, file string) (string, error) {
	var data []byte
	var err error
	if sess.client.ssh != nil {
		data, err = hostCommand(ctx, sess, project.Directory, []string{"head", "-c", fmt.Sprint(composeSourceLimit + 1), "--", file}, "", composeSourceLimit+1)
	} else {
		f, e := os.Open(file)
		if e != nil {
			return "", e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() {
			return "", errors.New("Compose source must be a regular file")
		}
		data, err = io.ReadAll(io.LimitReader(f, composeSourceLimit+1))
	}
	if err != nil {
		return "", err
	}
	if len(data) > composeSourceLimit || !utf8.Valid(data) {
		return "", errors.New("Compose source must be UTF-8 and at most 128 KiB")
	}
	return string(data), nil
}

func (p *plugin) composeSource(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Project string `json:"project"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, errInvalidParams
	}
	project, err := p.findComposeProject(sess, in.Project)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(sess.context(), 30*time.Second)
	defer cancel()
	files := []map[string]string{}
	for _, file := range project.Files {
		content, e := readComposeHostFile(ctx, sess, project, file)
		if e != nil {
			return nil, e
		}
		files = append(files, map[string]string{"path": file, "content": content})
	}
	return map[string]any{"project": project, "files": files, "editable": len(files) == 1, "sourceHash": hashValue(files[0]["content"])}, nil
}

func (p *plugin) composeProjectSnapshot(sess *session, args map[string]any) (any, error) {
	project, err := p.findComposeProject(sess, args["project"].(string))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(sess.context(), 45*time.Second)
	defer cancel()
	files := []string{}
	for _, file := range project.Files {
		body, e := readComposeHostFile(ctx, sess, project, file)
		if e != nil {
			return nil, e
		}
		files = append(files, body)
	}
	content, editing := args["content"].(string)
	if editing && (len(files) != 1 || args["action"] != "deploy") {
		return nil, errors.New("Source edits require single-file deploy")
	}
	if editing && args["sourceHash"] != hashValue(files[0]) {
		return nil, errors.New("Compose source changed; reload before editing")
	}
	command := append([]string{"docker"}, composeCLIArgs(sess, project, editing)...)
	command = append(command, "config", "--format", "json")
	rendered, err := hostCommand(ctx, sess, project.Directory, command, content, 4*1024*1024)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if json.Unmarshal(rendered, &config) != nil {
		return nil, errors.New("Docker Compose v2 returned invalid configuration")
	}
	// Reject include/extends files when editing: the stdin base changes relative
	// imports on older Compose versions. Existing multifile projects stay operable.
	if editing && (strings.Contains(content, "include:") || strings.Contains(content, "extends:")) {
		return nil, errors.New("Edit include/extends files on the host, then deploy without a source edit")
	}
	items, err := sess.client.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	states := []any{}
	for _, item := range items {
		if item.Labels[labelProject] == project.Name {
			state, e := p.mutationSnapshot(sess, toolDefinition{Name: "remove_container"}, map[string]any{"containerId": item.ID})
			if e != nil {
				return nil, e
			}
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return hashValue(states[i]) < hashValue(states[j]) })
	return map[string]any{"project": project, "sourceHash": hashValue(files), "renderedHash": hashValue(config), "containers": states}, nil
}

func writeComposeSource(ctx context.Context, sess *session, project composeProject, content string) error {
	file := project.Files[0]
	if sess.client.ssh != nil {
		// A fresh sibling plus rename prevents partial writes; refuse symlinks.
		script := "set -eu; test -f " + shellQuote(file) + "; test ! -L " + shellQuote(file) + "; tmp=$(mktemp " + shellQuote(file+".dbx-XXXXXX") + "); trap 'rm -f -- \"$tmp\"' EXIT; cat > \"$tmp\"; "
		if sess.client.ssh.sudo {
			script += "chown --reference=" + shellQuote(file) + " \"$tmp\"; "
		}
		script += "chmod --reference=" + shellQuote(file) + " \"$tmp\"; mv -f -- \"$tmp\" " + shellQuote(file)
		_, err := hostCommand(ctx, sess, project.Directory, []string{"sh", "-c", script}, content, 4096)
		return err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Refusing to replace a symlink or special file")
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".dbx-compose-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.WriteString(content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceFile(tmp.Name(), file)
}

func (p *plugin) composeProjectAction(sess *session, raw json.RawMessage) (any, error) {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "Compose management"); err != nil {
		return nil, err
	}
	project, err := p.findComposeProject(sess, args["project"].(string))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(sess.context(), 20*time.Minute)
	defer cancel()
	if content, ok := args["content"].(string); ok {
		if err = writeComposeSource(ctx, sess, project, content); err != nil {
			return nil, err
		}
	}
	commands := map[string][]string{"deploy": {"up", "--detach"}, "start": {"start"}, "stop": {"stop"}, "restart": {"restart"}, "down": {"down"}}
	operation, ok := commands[args["action"].(string)]
	if !ok {
		return nil, errInvalidParams
	}
	command := append([]string{"docker"}, composeCLIArgs(sess, project, false)...)
	command = append(command, operation...)
	_, err = hostCommand(ctx, sess, project.Directory, command, "", 1024*1024)
	if err != nil {
		return nil, fmt.Errorf("Compose may have partially completed; inspect the project before retrying. Source edits, if any, were saved. %w", err)
	}
	return map[string]any{"project": project.Name, "action": args["action"], "volumesRetained": true}, nil
}

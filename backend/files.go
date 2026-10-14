package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const filePreviewLimit = 2 * 1024 * 1024 // 2 MiB

// listScript 逐字复刻分支 service.rs 的只读 /bin/sh 脚本（路径作为 $1 独立参数传入）。
const listScript = `[ -d "$1" ] && [ -r "$1" ] && [ -x "$1" ] || { printf 'Directory is unavailable or permission denied\n' >&2; exit 1; }; for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do [ -e "$entry" ] || [ -L "$entry" ] || continue; stat -c '%F	%s	%Y	%n' -- "$entry"; done`

// A fixed operation set; paths are positional arguments, never shell source.
func fileOperationCommand(operation, target, name string) (string, []string, error) {
	if _, err := validateContainerPath(target); err != nil {
		return "", nil, err
	}
	if path.Clean(target) != target || target == "/" || len(target) > 4096 {
		return "", nil, errors.New("File operations require a normalized non-root path")
	}
	args := []string{target}
	switch operation {
	case "mkdir":
		return `mkdir -- "$1"`, args, nil
	case "create":
		return `if [ -e "$1" ] || [ -L "$1" ]; then printf 'Target already exists\n' >&2; exit 1; fi; (set -C; : > "$1")`, args, nil
	case "rename":
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00\n\r") || len(name) > 255 {
			return "", nil, errors.New("Invalid file name")
		}
		dest := path.Join(path.Dir(target), name)
		args = append(args, dest)
		return `if [ -e "$2" ] || [ -L "$2" ]; then printf 'Target already exists\n' >&2; exit 1; fi; mv -nT -- "$1" "$2" && { [ ! -e "$1" ] && [ ! -L "$1" ]; }`, args, nil
	case "delete":
		return `rm -r -- "$1"`, args, nil
	default:
		return "", nil, errors.New("Unsupported file operation")
	}
}

func (p *plugin) fileOperation(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Operation   string `json:"operation"`
		Path        string `json:"path"`
		Name        string `json:"name"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ContainerID == "" {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "changing container files"); err != nil {
		return nil, err
	}
	script, args, err := fileOperationCommand(in.Operation, in.Path, in.Name)
	if err != nil {
		return nil, err
	}
	sess.writeMutex().Lock()
	defer sess.writeMutex().Unlock()
	if _, err := execContainerScript(sess.client.cli, in.ContainerID, script, args...); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

var previewScript = `head -c ` + strconv.Itoa(filePreviewLimit+1) + ` -- "$1"`

func (p *plugin) listContainerFiles(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("listContainerFiles requires containerId")
	}
	path := in.Path
	if path == "" {
		path = "/"
	}
	if _, err := validateContainerPath(path); err != nil {
		return nil, err
	}
	output, err := execContainerScript(sess.client.cli, in.ContainerID, listScript, path)
	if err != nil {
		return nil, fmt.Errorf("Container file browsing requires /bin/sh and stat: %w", err)
	}
	entries := parseFileEntries(string(output))
	return entries, nil
}

func parseFileEntries(output string) []DockerFileEntry {
	entries := []DockerFileEntry{}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		kindText := strings.ToLower(parts[0])
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		modified, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		fullPath := path.Clean(parts[3])

		kind := "file"
		if strings.Contains(kindText, "directory") {
			kind = "directory"
		} else if strings.Contains(kindText, "symbolic link") {
			kind = "symlink"
		}
		name := fullPath
		if idx := strings.LastIndex(fullPath, "/"); idx >= 0 {
			name = fullPath[idx+1:]
		}
		entries = append(entries, DockerFileEntry{
			Name:     name,
			Path:     fullPath,
			Kind:     kind,
			Size:     size,
			Modified: modified,
		})
	}
	// 目录在前，再按名称小写不敏感排序。
	sort.SliceStable(entries, func(i, j int) bool {
		iDir := entries[i].Kind == "directory"
		jDir := entries[j].Kind == "directory"
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func (p *plugin) previewContainerFile(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" || in.Path == "" {
		return nil, errors.New("previewContainerFile requires containerId and path")
	}
	if _, err := validateContainerPath(in.Path); err != nil {
		return nil, err
	}
	output, err := execContainerScript(sess.client.cli, in.ContainerID, previewScript, in.Path)
	if err != nil {
		return nil, fmt.Errorf("Container file preview requires /bin/sh and head: %w", err)
	}
	return containerFilePreview(in.Path, output), nil
}

func containerFilePreview(filePath string, output []byte) DockerFilePreview {
	truncated := len(output) > filePreviewLimit
	if truncated {
		output = output[:filePreviewLimit]
	}
	binary := !utf8.Valid(output) || strings.IndexByte(string(output), 0) >= 0
	content := ""
	if !binary {
		content = string(output)
	}
	preview := DockerFilePreview{
		Path:      filePath,
		Content:   content,
		Truncated: truncated,
		Binary:    binary,
	}
	mimeType := http.DetectContentType(output)
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/x-icon":
		if !truncated {
			preview.MimeType = mimeType
			preview.DataBase64 = base64.StdEncoding.EncodeToString(output)
			preview.Content = ""
		}
	}
	return preview
}

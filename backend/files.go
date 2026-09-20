package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const filePreviewLimit = 2 * 1024 * 1024 // 2 MiB

// listScript 逐字复刻分支 service.rs 的只读 /bin/sh 脚本（路径作为 $1 独立参数传入）。
const listScript = `for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do [ -e "$entry" ] || [ -L "$entry" ] || continue; stat -c '%F	%s	%Y	%n' -- "$entry"; done`

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
		fullPath := parts[3]

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
	truncated := len(output) > filePreviewLimit
	if truncated {
		output = output[:filePreviewLimit]
	}
	binary := !utf8.Valid(output) || strings.IndexByte(string(output), 0) >= 0
	content := ""
	if !binary {
		content = string(output)
	}
	return DockerFilePreview{
		Path:      in.Path,
		Content:   content,
		Truncated: truncated,
		Binary:    binary,
	}, nil
}

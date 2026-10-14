package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestContainerPreviewFormats(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZgAAAABJRU5ErkJggg==")
	preview := containerFilePreview("/image.png", png)
	if preview.MimeType != "image/png" || preview.DataBase64 == "" || preview.Content != "" {
		t.Fatal(preview)
	}
	for _, file := range []string{"/app.yml", "/app.xml", "/app.properties"} {
		preview = containerFilePreview(file, []byte("server=中文\n"))
		if preview.Binary || preview.Content != "server=中文\n" || preview.DataBase64 != "" {
			t.Fatal(preview)
		}
	}
	preview = containerFilePreview("/too-large.png", append(png, bytes.Repeat([]byte{0}, filePreviewLimit)...))
	if !preview.Truncated || preview.DataBase64 != "" {
		t.Fatal("partial image must not be rendered")
	}
	preview = containerFilePreview("/diagram.svg", []byte(`<svg onload="alert(1)"></svg>`))
	if preview.DataBase64 != "" || preview.Binary {
		t.Fatal("SVG must stay in the text editor")
	}
}

func TestFileOperationValidation(t *testing.T) {
	for _, path := range []string{"/", "/tmp/..", "/tmp/", "relative", "/tmp/\x00bad", "/tmp/../etc"} {
		if _, _, err := fileOperationCommand("delete", path, ""); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	for _, name := range []string{"", ".", "..", "../other", "a\nb"} {
		if _, _, err := fileOperationCommand("rename", "/tmp/file", name); err == nil {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
	target := "/tmp/$(touch pwned); spaced name"
	script, args, err := fileOperationCommand("rename", target, "quote'new")
	if err != nil || strings.Contains(script, target) || args[0] != target || args[1] != "/tmp/quote'new" {
		t.Fatal(script, args, err)
	}
	p := testAutomationPlugin(t)
	if _, err := p.fileOperation(&session{conn: connectionPayload{ReadOnly: true}}, jsonBytes(map[string]any{"containerId": "test", "operation": "mkdir", "path": "/tmp/new"})); err == nil {
		t.Fatal("read-only file mutation accepted")
	}
}

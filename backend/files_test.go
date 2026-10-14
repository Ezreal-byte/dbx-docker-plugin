package main

import (
	"strings"
	"testing"
)

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

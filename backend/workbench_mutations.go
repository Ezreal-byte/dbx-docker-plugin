package main

import (
	"encoding/json"
	"errors"
)

// These operations share the preview machinery but are deliberately not MCP
// tools: file writes and tag deletion remain explicit workbench actions.
func findMutation(name string) (toolDefinition, bool) {
	if t, ok := findTool(name); ok {
		return t, ok
	}
	var method string
	var input schema
	switch name {
	case "delete_file":
		method = "docker/fileOperation"
		input = object(schema{"containerId": str(256), "path": str(4096), "operation": enum("delete")}, "containerId", "path", "operation")
	case "upload_file":
		method = "docker/startFileUpload"
		input = object(schema{"containerId": str(256), "path": str(4096), "size": integer(0, maxFileTransferBytes), "mode": enum("overwrite", "append")}, "containerId", "path", "size", "mode")
	case "untag_image":
		method = "docker/untagImage"
		input = object(schema{"reference": str(256)}, "reference")
	default:
		return toolDefinition{}, false
	}
	input["properties"].(schema)["previewToken"] = str(48)
	return toolDefinition{Name: name, Method: method, Group: "delete", Destructive: true, InputSchema: input}, true
}

func workbenchMutation(method string, raw json.RawMessage) string {
	switch method {
	case "docker/removeContainer":
		return "remove_container"
	case "docker/removeImage":
		return "remove_image"
	case "docker/removeNetwork":
		return "remove_network"
	case "docker/removeVolume":
		return "remove_volume"
	case "docker/applyCompose":
		return "apply_compose"
	case "docker/prune":
		return "prune_resources"
	case "docker/disconnectNetwork":
		return "disconnect_network"
	case "docker/tagImage":
		return "tag_image"
	case "docker/untagImage":
		return "untag_image"
	case "docker/fileOperation":
		var in struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(raw, &in)
		if in.Operation == "delete" {
			return "delete_file"
		}
	}
	return ""
}

// Guard the public RPC entry, including legacy aliases. Internal dispatch from
// executeMutation has already consumed and rechecked the bound preview.
func (p *plugin) guardedWorkbenchMutation(sess *session, operation string, raw json.RawMessage) (any, error) {
	t, _ := findMutation(operation)
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errInvalidParams
	}
	args := map[string]any{}
	for key := range t.InputSchema["properties"].(schema) {
		if value, exists := envelope[key]; exists {
			args[key] = value
		}
	}
	if token, _ := args["previewToken"].(string); token == "" {
		return nil, errors.New("A confirmed preview is required; use docker/mutation to preview, then execute with its previewToken")
	}
	if err := validateValue(t.InputSchema, args, "arguments"); err != nil {
		return nil, err
	}
	return p.runTool(sess, t, args, false)
}

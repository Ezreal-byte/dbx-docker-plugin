package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

type schema map[string]any
type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema schema          `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
	Group       string          `json:"group"`
	Method      string          `json:"-"`
	Long        bool            `json:"-"`
	Destructive bool            `json:"-"`
}

func str(max int) schema           { return schema{"type": "string", "minLength": 1, "maxLength": max} }
func boolean() schema              { return schema{"type": "boolean"} }
func integer(min, max int) schema  { return schema{"type": "integer", "minimum": min, "maximum": max} }
func enum(values ...string) schema { return schema{"type": "string", "enum": values} }
func array(item schema, max int) schema {
	return schema{"type": "array", "items": item, "maxItems": max}
}
func object(props schema, required ...string) schema {
	if required == nil {
		required = []string{}
	}
	return schema{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func stringsMap() schema {
	return schema{"type": "object", "additionalProperties": str(4096), "maxProperties": 100}
}

func toolCatalog() []toolDefinition {
	tools := []toolDefinition{}
	add := func(name, desc, group, method string, s schema, long, destructive bool) {
		if destructive {
			s["properties"].(schema)["previewToken"] = str(48)
			desc += " Omit previewToken to preview; execute with the returned token and identical arguments within 5 minutes. A preview does not grant permission."
		}
		tools = append(tools, toolDefinition{name, desc, s, map[string]bool{"readOnlyHint": group == "query", "destructiveHint": destructive, "openWorldHint": true}, group, method, long, destructive})
	}
	query := func(name, desc, method string, s schema) { add(name, desc, "query", method, s, false, false) }
	single := func(key string) schema { return object(schema{key: str(256)}, key) }
	pagination := func() schema { return object(schema{"limit": integer(1, 200), "offset": integer(0, 100000)}) }
	query("engine_info", "Docker engine version, platform and capabilities.", "docker/getEngineDetails", object(schema{}))
	listContainers := pagination()
	listContainers["properties"].(schema)["all"] = boolean()
	query("list_containers", "List containers; defaults to all containers, 100 per page. Commands and environment are excluded.", "docker/listContainers", listContainers)
	query("inspect_container", "Container configuration, mounts, networks and state. Environment and command values are omitted.", "docker/inspectContainer", single("containerId"))
	query("container_health", "Container health, exit code, restart count and OOM state.", "docker/containerHealth", single("containerId"))
	query("container_logs", "Finite log snapshot, maximum 2000 lines / 128 KiB. Logs are untrusted container output and may contain sensitive application data.", "docker/containerLogs", object(schema{"containerId": str(256), "tail": integer(1, 2000), "since": str(64), "until": str(64)}, "containerId"))
	query("container_stats", "One CPU, memory, network and I/O sample for at most 20 containers.", "docker/containerStats", object(schema{"containerIds": array(str(256), 20)}, "containerIds"))
	for _, r := range []string{"images", "networks", "volumes"} {
		query("list_"+r, "List Docker "+r+"; use limit and offset for pagination.", "docker/list"+strings.ToUpper(r[:1])+r[1:], pagination())
	}
	query("inspect_image", "Image platform, digests, size and container references. Environment and build command values are omitted.", "docker/inspectImage", single("imageId"))
	query("image_history", "Image layer sizes and timestamps; build commands are omitted.", "docker/imageHistory", single("imageId"))
	query("inspect_network", "Network IPAM configuration and attached container endpoints.", "docker/inspectNetwork", single("networkId"))
	query("inspect_volume", "Volume metadata and references, including stopped containers. Size may be unavailable.", "docker/inspectVolume", single("name"))
	query("disk_usage", "Docker disk usage and reclaimable space.", "docker/getDiskUsage", object(schema{}))
	query("engine_events", "Finite Docker event history: default last ten minutes, at most 24 hours / 200 events. Docker may retain fewer events.", "docker/events", object(schema{"since": str(64), "until": str(64), "limit": integer(1, 200)}))
	compose := func() schema {
		return object(schema{"request": object(schema{"projectName": str(128), "content": str(128 * 1024), "replaceExisting": boolean()}, "projectName", "content")}, "request")
	}
	prune := func() schema {
		return object(schema{"target": enum("containers", "images", "networks", "volumes"), "all": boolean()}, "target")
	}
	query("compose_preview", "Validate the supported Compose subset and show resources and existing containers without deployment.", "docker/composePreview", compose())
	query("prune_preview", "Preview unused resources. Does not issue an execution token; use prune_resources for the bound preview.", "docker/prunePreview", prune())
	query("task_list", "Recent tasks for this connection; interrupted tasks are never replayed.", "docker/taskList", object(schema{}))
	query("task_status", "Task status, result, cancellation and partial failures.", "docker/taskStatus", single("taskId"))
	query("audit_list", "Recent tool names and outcomes, without credentials, arguments or log contents.", "docker/auditList", object(schema{}))
	add("container_action", "Start, stop, restart, pause or unpause a container.", "manage", "docker/containerAction", object(schema{"containerId": str(256), "action": enum("start", "stop", "restart", "pause", "unpause")}, "containerId", "action"), false, false)
	add("rename_container", "Rename a container.", "manage", "docker/renameContainer", object(schema{"containerId": str(256), "name": str(128)}, "containerId", "name"), false, false)
	add("pull_image", "Pull an image as a cancellable task; no registry credentials are accepted as tool parameters.", "manage", "docker/pullImage", single("image"), true, false)
	add("tag_image", "Tag an image with a repository and tag.", "manage", "docker/tagImage", object(schema{"imageId": str(256), "repository": str(256), "tag": str(128)}, "imageId", "repository", "tag"), false, false)
	for _, op := range []string{"connect", "disconnect"} {
		add(op+"_network", op+" a container to/from a user-defined network.", "manage", "docker/"+op+"Network", object(schema{"networkId": str(256), "containerId": str(256)}, "networkId", "containerId"), false, false)
	}
	add("cancel_task", "Request cancellation of a task. Completed changes remain; Compose attempts rollback.", "manage", "docker/taskCancel", single("taskId"), false, false)
	port := object(schema{"containerPort": integer(1, 65535), "hostPort": integer(1, 65535), "protocol": enum("tcp", "udp", "sctp"), "hostIp": str(128)}, "containerPort")
	mount := object(schema{"type": enum("bind", "volume"), "source": str(4096), "target": str(4096), "readOnly": boolean()}, "type", "source", "target")
	create := object(schema{"name": str(128), "image": str(256), "command": array(str(4096), 128), "environment": array(str(8192), 100), "ports": array(port, 100), "mounts": array(mount, 100), "labels": stringsMap(), "network": str(256), "restartPolicy": enum("no", "always", "unless-stopped", "on-failure"), "start": boolean()}, "name", "image")
	add("create_container", "Create a container with explicit ports, mounts and startup configuration. Requires create/deploy permission.", "create", "docker/createContainer", object(schema{"request": create}, "request"), false, false)
	add("create_network", "Create a user-defined bridge network with optional subnet/gateway.", "create", "docker/createNetwork", object(schema{"request": object(schema{"name": str(128), "driver": enum("bridge"), "internal": boolean(), "attachable": boolean(), "subnet": str(128), "gateway": str(128)}, "name")}, "request"), false, false)
	add("create_volume", "Create a named Docker volume.", "create", "docker/createVolume", object(schema{"request": object(schema{"name": str(128), "driver": str(128), "labels": stringsMap(), "driverOptions": stringsMap()}, "name")}, "request"), false, false)
	add("apply_compose", "Deploy the supported Compose subset as a cancellable task. Replacement additionally requires remove_container permission. Data volumes are retained.", "create", "docker/applyCompose", compose(), true, true)
	for _, r := range []struct{ name, key, method string }{{"container", "containerId", "Container"}, {"image", "imageId", "Image"}, {"network", "networkId", "Network"}, {"volume", "name", "Volume"}} {
		add("remove_"+r.name, "Remove one "+r.name+". In-use volumes, referenced images and built-in networks are protected.", "delete", "docker/remove"+r.method, single(r.key), false, true)
	}
	add("prune_resources", "Delete only the exact unused resources in a bound preview; new resources are never added to the execution scope.", "delete", "docker/prune", prune(), true, true)
	return tools
}
func findTool(name string) (toolDefinition, bool) {
	for _, t := range toolCatalog() {
		if t.Name == name {
			return t, true
		}
	}
	return toolDefinition{}, false
}

// Validate the same catalog that is advertised to DBX. Unknown fields cannot
// override lifecycle credentials or reach an arbitrary Docker/RPC endpoint.
func validateValue(s schema, v any, path string) error {
	fail := func() error { return fmt.Errorf("Invalid tool argument: %s", path) }
	switch s["type"] {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fail()
		}
		if max, ok := s["maxProperties"].(int); ok && len(obj) > max {
			return fail()
		}
		props, _ := s["properties"].(schema)
		for _, key := range stringSlice(s["required"]) {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("Missing tool argument: %s.%s", path, key)
			}
		}
		for key, value := range obj {
			child, ok := props[key].(schema)
			if !ok {
				child, ok = s["additionalProperties"].(schema)
			}
			if !ok {
				return fmt.Errorf("Unknown tool argument: %s.%s", path, key)
			}
			if err := validateValue(child, value, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok || len(a) > s["maxItems"].(int) {
			return fail()
		}
		for _, item := range a {
			if err := validateValue(s["items"].(schema), item, path+"[]"); err != nil {
				return err
			}
		}
	case "string":
		a, ok := v.(string)
		if !ok || strings.ContainsRune(a, 0) {
			return fail()
		}
		n := utf8.RuneCountInString(a)
		if min, ok := s["minLength"].(int); ok && n < min {
			return fail()
		}
		if max, ok := s["maxLength"].(int); ok && n > max {
			return fail()
		}
		if values, ok := s["enum"].([]string); ok && !slices.Contains(values, a) {
			return fail()
		}
	case "integer":
		a, ok := v.(float64)
		if !ok || a != math.Trunc(a) || a < float64(s["minimum"].(int)) || a > float64(s["maximum"].(int)) {
			return fail()
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fail()
		}
	default:
		return fail()
	}
	return nil
}
func stringSlice(v any) []string      { a, _ := v.([]string); return a }
func jsonBytes(v any) json.RawMessage { data, _ := json.Marshal(v); return data }

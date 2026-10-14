package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// Snapshots exclude clocks and counters which change without changing the
// deletion scope, while retaining identity, configuration and references.
func (p *plugin) mutationSnapshot(sess *session, t toolDefinition, args map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(sess.context(), 60*time.Second)
	defer cancel()
	switch t.Name {
	case "delete_file", "upload_file":
		return fileMutationSnapshot(sess, t.Name, args)
	case "untag_image":
		return p.resourceOperation("docker/inspectImage", sess, jsonBytes(map[string]any{"imageId": args["reference"]}))
	case "tag_image":
		source, _, err := sess.client.cli.ImageInspectWithRaw(ctx, args["imageId"].(string))
		if err != nil {
			return nil, err
		}
		reference := args["repository"].(string) + ":" + args["tag"].(string)
		target, _, err := sess.client.cli.ImageInspectWithRaw(ctx, reference)
		if err != nil && !errdefs.IsNotFound(err) {
			return nil, err
		}
		return map[string]any{"sourceId": source.ID, "reference": reference, "previousImageId": target.ID, "replacesTag": target.ID != "" && target.ID != source.ID}, nil
	case "disconnect_network":
		n, err := sess.client.cli.NetworkInspect(ctx, args["networkId"].(string), network.InspectOptions{})
		if err != nil {
			return nil, err
		}
		c, err := sess.client.cli.ContainerInspect(ctx, args["containerId"].(string))
		if err != nil {
			return nil, err
		}
		return map[string]any{"network": n, "containerId": c.ID, "containerName": c.Name}, nil
	case "remove_container":
		c, err := sess.client.cli.ContainerInspect(ctx, args["containerId"].(string))
		if err != nil {
			return nil, err
		}
		if c.State == nil {
			return nil, errors.New("Container state is unavailable")
		}
		return map[string]any{"id": c.ID, "name": c.Name, "image": c.Image, "config": c.Config, "mounts": c.Mounts, "networks": c.NetworkSettings, "status": c.State.Status, "startedAt": c.State.StartedAt, "finishedAt": c.State.FinishedAt, "restartCount": c.RestartCount}, nil
	case "remove_image":
		return p.resourceOperation("docker/inspectImage", sess, jsonBytes(args))
	case "remove_network":
		return sess.client.cli.NetworkInspect(ctx, args["networkId"].(string), network.InspectOptions{})
	case "remove_volume":
		return p.resourceOperation("docker/inspectVolume", sess, jsonBytes(map[string]any{"name": args["name"], "metadataOnly": true}))
	case "apply_compose":
		var in struct {
			Request DockerComposeApplyRequest `json:"request"`
		}
		_ = json.Unmarshal(jsonBytes(args), &in)
		plan, err := buildComposePlan(in.Request)
		if err != nil {
			return nil, err
		}
		items, err := sess.client.cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, c := range items {
			if c.Labels[labelProject] == plan.Project {
				ids = append(ids, c.ID)
			}
		}
		sort.Strings(ids)
		states := []any{}
		for _, id := range ids {
			st, err := p.mutationSnapshot(sess, toolDefinition{Name: "remove_container"}, map[string]any{"containerId": id})
			if err != nil {
				return nil, err
			}
			states = append(states, st)
		}
		networks, err := sess.client.cli.NetworkList(ctx, network.ListOptions{})
		if err != nil {
			return nil, err
		}
		sort.Slice(networks, func(i, j int) bool { return networks[i].ID < networks[j].ID })
		relevant := []any{}
		for _, n := range networks {
			for _, name := range plan.Networks {
				if n.Name == name {
					relevant = append(relevant, n)
				}
			}
		}
		return map[string]any{"project": plan.Project, "containers": states, "networks": relevant}, nil
	case "prune_resources":
		value, err := p.prunePreview(sess, jsonBytes(args))
		if err != nil {
			return nil, err
		}
		preview := value.(DockerPrunePreview)
		if preview.Truncated {
			return nil, errors.New("More than 200 prune candidates; remove selected resources first")
		}
		sort.Slice(preview.Items, func(i, j int) bool { return preview.Items[i].ID < preview.Items[j].ID })
		specs := map[string]struct{ tool, key string }{"containers": {"remove_container", "containerId"}, "images": {"remove_image", "imageId"}, "networks": {"remove_network", "networkId"}, "volumes": {"remove_volume", "name"}}
		spec, ok := specs[preview.Target]
		if !ok {
			return nil, errInvalidParams
		}
		states := []any{}
		for _, item := range preview.Items {
			st, err := p.mutationSnapshot(sess, toolDefinition{Name: spec.tool}, map[string]any{spec.key: item.ID})
			if err != nil {
				return nil, err
			}
			states = append(states, map[string]any{"id": item.ID, "state": st})
		}
		return map[string]any{"target": preview.Target, "items": states}, nil
	}
	return nil, errMethodNotFound
}

func (p *plugin) prepareMutation(sess *session, t toolDefinition, args map[string]any) (any, error) {
	snapshot, err := p.mutationSnapshot(sess, t, args)
	if err != nil {
		return nil, err
	}
	display := sanitizeToolData(snapshot)
	if t.Name == "apply_compose" {
		display, err = p.resourceOperation("docker/composePreview", sess, jsonBytes(args))
		if err != nil {
			return nil, err
		}
	}
	if t.Name == "prune_resources" {
		display, err = p.prunePreview(sess, jsonBytes(args))
		if err != nil {
			return nil, err
		}
	}
	if len(jsonBytes(display)) > 160*1024 {
		return nil, errors.New("Preview is too large; select fewer resources")
	}
	// Multi-query display generation must describe the same state we bind.
	if t.Name == "apply_compose" || t.Name == "prune_resources" {
		current, err := p.mutationSnapshot(sess, t, args)
		if err != nil {
			return nil, err
		}
		if hashValue(current) != hashValue(snapshot) {
			return nil, errors.New("Target changed while preparing the preview; retry")
		}
	}
	token := randomID()
	expires := time.Now().UTC().Add(5 * time.Minute)
	err = p.lockedState(true, func(s *automationState) error {
		if len(s.Previews) > 500 {
			return errors.New("Too many pending previews; wait five minutes")
		}
		s.Previews[token] = previewRecord{sess.conn.ID, t.Name, hashValue(args), hashValue(snapshot), expires}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"previewToken": token, "expiresAt": expires, "operation": t.Name, "connectionId": sess.conn.ID, "isProduction": sess.conn.effectiveProduction(), "preview": display, "executed": false}, nil
}

func (p *plugin) consumePreview(sess *session, t toolDefinition, args map[string]any, token string) error {
	var record previewRecord
	// Consume even when the snapshot is stale: tokens never become valid again.
	err := p.lockedState(true, func(s *automationState) error {
		r, ok := s.Previews[token]
		if !ok || r.ConnectionID != sess.conn.ID || r.Operation != t.Name || r.ArgumentsHash != hashValue(args) {
			return errors.New("Invalid preview token, connection or arguments; preview again")
		}
		record = r
		delete(s.Previews, token)
		return nil
	})
	if err != nil {
		return err
	}
	if time.Now().After(record.ExpiresAt) {
		return errors.New("Preview expired; preview again")
	}
	snapshot, err := p.mutationSnapshot(sess, t, args)
	if err != nil {
		return err
	}
	if hashValue(snapshot) != record.SnapshotHash {
		return errors.New("Target state changed; preview again")
	}
	args["_previewSnapshot"] = snapshot
	return nil
}

func (p *plugin) executeMutation(sess *session, t toolDefinition, args map[string]any) (any, error) {
	lock := sess.writeMutex()
	lock.Lock()
	defer lock.Unlock()
	if sess.automated {
		if err := p.policyAllows(sess.conn.ID, t.Name); err != nil {
			return nil, err
		}
		if t.Name == "apply_compose" {
			if request, _ := args["request"].(map[string]any); request["replaceExisting"] == true {
				if err := p.policyAllows(sess.conn.ID, "remove_container"); err != nil {
					return nil, err
				}
			}
		}
	}
	child := &session{client: sess.client, conn: sess.conn, parent: sess, ctx: sess.context(), lockHeld: true}
	if err := child.context().Err(); err != nil {
		return nil, err
	}
	if t.Destructive {
		expected, ok := args["_previewSnapshot"]
		if !ok {
			return nil, errors.New("A bound preview is required")
		}
		current, err := p.mutationSnapshot(child, t, args)
		if err != nil {
			return nil, err
		}
		if hashValue(expected) != hashValue(current) {
			return nil, errors.New("Target state changed before execution; preview again")
		}
		// Use immutable IDs even when a caller supplied a mutable container name.
		if t.Name == "remove_container" {
			args["containerId"] = current.(map[string]any)["id"]
		}
		if t.Name == "remove_image" {
			var out struct{ Details struct{ ID string } }
			_ = json.Unmarshal(jsonBytes(current), &out)
			args["imageId"] = out.Details.ID
		}
		if t.Name == "remove_network" {
			var out struct{ ID string }
			_ = json.Unmarshal(jsonBytes(current), &out)
			args["networkId"] = out.ID
		}
		child.mutationScope = current
		if t.Name == "tag_image" {
			args["imageId"] = current.(map[string]any)["sourceId"]
		}
		if t.Name == "delete_file" {
			args["containerId"] = current.(map[string]any)["containerId"]
		}
		if t.Name == "disconnect_network" {
			args["containerId"] = current.(map[string]any)["containerId"]
			args["networkId"] = current.(map[string]any)["network"].(network.Inspect).ID
		}
	}
	return p.executeTool(child, t, args)
}

func (p *plugin) pruneExact(sess *session, args map[string]any) (any, error) {
	snapshot, ok := args["_previewSnapshot"].(map[string]any)
	if !ok {
		return nil, errors.New("Prune requires a bound preview")
	}
	items := snapshot["items"].([]any)
	specs := map[string]struct{ tool, key string }{"containers": {"remove_container", "containerId"}, "images": {"remove_image", "imageId"}, "networks": {"remove_network", "networkId"}, "volumes": {"remove_volume", "name"}}
	spec := specs[args["target"].(string)]
	tool, _ := findTool(spec.tool)
	deleted := []string{}
	failed := []map[string]any{}
	for _, item := range items {
		if sess.context().Err() != nil {
			break
		}
		entry := item.(map[string]any)
		id := entry["id"].(string)
		params := map[string]any{spec.key: id}
		current, err := p.mutationSnapshot(sess, tool, params)
		if err == nil && hashValue(current) != hashValue(entry["state"]) {
			err = errors.New("Target state changed; skipped")
		}
		if err == nil {
			_, err = p.dispatchSession(tool.Method, sess, jsonBytes(params), nil)
		}
		if err != nil {
			failed = append(failed, map[string]any{"id": id, "error": redactedError(err, sess.conn).Error()})
		} else {
			deleted = append(deleted, id)
		}
	}
	result := map[string]any{"deleted": deleted, "failed": failed, "partial": len(deleted) < len(items), "remaining": len(items) - len(deleted) - len(failed), "spaceReclaimed": 0}
	if sess.context().Err() != nil {
		return result, sess.context().Err()
	}
	if len(failed) > 0 {
		return result, fmt.Errorf("%d resources could not be removed; see partial result", len(failed))
	}
	return result, nil
}

func (p *plugin) mutationRPC(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Operation string         `json:"operation"`
		Arguments map[string]any `json:"arguments"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, errInvalidParams
	}
	t, ok := findMutation(in.Operation)
	if !ok || !t.Destructive {
		return nil, errors.New("Unknown preview operation")
	}
	if err := validateValue(t.InputSchema, in.Arguments, "arguments"); err != nil {
		return nil, err
	}
	if in.Operation == "upload_file" && in.Arguments["previewToken"] != nil {
		return nil, errors.New("Execute an upload through docker/startFileUpload with its previewToken")
	}
	return p.runTool(sess, t, in.Arguments, false)
}

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
)

func resourceID(raw json.RawMessage, key string) (string, error) {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return "", errInvalidParams
	}
	id, _ := in[key].(string)
	if strings.TrimSpace(id) == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
		return "", fmt.Errorf("%s is required (max 256 characters)", key)
	}
	return id, nil
}

func (p *plugin) resourceOperation(method string, sess *session, raw json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(sess.context(), 30*time.Second)
	defer cancel()
	cli := sess.client.cli
	switch method {
	case "docker/events":
		var in struct {
			Since string `json:"since"`
			Until string `json:"until"`
			Limit int    `json:"limit"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return nil, errInvalidParams
		}
		until := time.Now().UTC()
		since := until.Add(-10 * time.Minute)
		var err error
		if in.Since != "" {
			since, err = time.Parse(time.RFC3339, in.Since)
			if err != nil {
				return nil, errInvalidParams
			}
		}
		if in.Until != "" {
			until, err = time.Parse(time.RFC3339, in.Until)
			if err != nil {
				return nil, errInvalidParams
			}
		}
		if until.After(time.Now()) || !since.Before(until) || until.Sub(since) > 24*time.Hour {
			return nil, errors.New("Event range must be in the past and no longer than 24 hours")
		}
		if in.Limit == 0 {
			in.Limit = 100
		}
		if in.Limit < 1 || in.Limit > 200 {
			return nil, errors.New("Event limit must be between 1 and 200")
		}
		messages, errs := cli.Events(ctx, events.ListOptions{Since: since.Format(time.RFC3339Nano), Until: until.Format(time.RFC3339Nano)})
		items := []map[string]any{}
		for {
			select {
			case <-ctx.Done():
				return map[string]any{"items": items, "truncated": true}, ctx.Err()
			case err := <-errs:
				if err != nil && err != io.EOF {
					return nil, err
				}
				return map[string]any{"items": items, "truncated": false}, nil
			case event, ok := <-messages:
				if !ok {
					return map[string]any{"items": items, "truncated": false}, nil
				}
				items = append(items, map[string]any{"id": event.Actor.ID, "type": event.Type, "action": event.Action, "time": event.Time, "timeNano": event.TimeNano})
				if len(items) >= in.Limit {
					return map[string]any{"items": items, "truncated": true}, nil
				}
			}
		}
	case "docker/inspectImage":
		id, err := resourceID(raw, "imageId")
		if err != nil {
			return nil, err
		}
		detail, _, err := cli.ImageInspectWithRaw(ctx, id)
		if err != nil {
			return nil, err
		}
		containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return nil, err
		}
		refs := []map[string]any{}
		for _, c := range containers {
			if c.ImageID == detail.ID {
				refs = append(refs, resourceRef(c))
			}
		}
		sortResources(refs)
		return map[string]any{"details": detail, "containers": refs}, nil
	case "docker/inspectNetwork":
		id, err := resourceID(raw, "networkId")
		if err != nil {
			return nil, err
		}
		return cli.NetworkInspect(ctx, id, network.InspectOptions{})
	case "docker/inspectVolume":
		id, err := resourceID(raw, "name")
		if err != nil {
			return nil, err
		}
		detail, err := cli.VolumeInspect(ctx, id)
		if err != nil {
			return nil, err
		}
		containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return nil, err
		}
		refs := []map[string]any{}
		for _, c := range containers {
			for _, m := range c.Mounts {
				if m.Name == id {
					r := resourceRef(c)
					r["destination"] = m.Destination
					r["readOnly"] = !m.RW
					refs = append(refs, r)
				}
			}
		}
		sortResources(refs)
		var options struct {
			MetadataOnly bool `json:"metadataOnly"`
		}
		_ = json.Unmarshal(raw, &options)
		if !options.MetadataOnly && detail.UsageData == nil {
			usageCtx, usageCancel := context.WithTimeout(ctx, 5*time.Second)
			usage, usageErr := cli.DiskUsage(usageCtx, dockertypes.DiskUsageOptions{Types: []dockertypes.DiskUsageObject{dockertypes.VolumeObject}})
			usageCancel()
			if usageErr == nil {
				for _, v := range usage.Volumes {
					if v != nil && v.Name == id {
						detail.UsageData = v.UsageData
						break
					}
				}
			}
		}
		return map[string]any{"details": detail, "containers": refs, "usageKnown": detail.UsageData != nil && detail.UsageData.Size >= 0}, nil
	case "docker/containerHealth":
		id, err := resourceID(raw, "containerId")
		if err != nil {
			return nil, err
		}
		c, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if c.State == nil {
			return nil, errors.New("Container state is unavailable")
		}
		out := map[string]any{"id": c.ID, "name": c.Name, "state": c.State.Status, "exitCode": c.State.ExitCode, "oomKilled": c.State.OOMKilled, "restartCount": c.RestartCount, "health": "not-configured"}
		if c.State.Health != nil {
			out["health"] = c.State.Health.Status
			out["failingStreak"] = c.State.Health.FailingStreak
		}
		return out, nil
	case "docker/containerLogs":
		id, err := resourceID(raw, "containerId")
		if err != nil {
			return nil, err
		}
		var in struct {
			Tail  int    `json:"tail"`
			Since string `json:"since"`
			Until string `json:"until"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return nil, errInvalidParams
		}
		if in.Tail == 0 {
			in.Tail = 200
		}
		if in.Tail < 1 || in.Tail > 2000 {
			return nil, errors.New("tail must be between 1 and 2000")
		}
		for _, v := range []string{in.Since, in.Until} {
			if v != "" {
				if _, e := time.Parse(time.RFC3339, v); e != nil {
					return nil, errors.New("since/until must be RFC3339 timestamps")
				}
			}
		}
		reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: false, Tail: strconv.Itoa(in.Tail), Since: in.Since, Until: in.Until, Timestamps: true})
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		const limit = 128 * 1024
		data, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if err != nil {
			return nil, err
		}
		truncated := len(data) > limit
		if truncated {
			data = data[:limit]
		}
		return map[string]any{"containerId": id, "text": strings.ToValidUTF8(string(decodeLogSnapshot(data)), "�"), "truncated": truncated}, nil
	case "docker/composePreview":
		var in struct {
			Request DockerComposeApplyRequest `json:"request"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return nil, errInvalidParams
		}
		plan, err := buildComposePlan(in.Request)
		if err != nil {
			return nil, err
		}
		items, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return nil, err
		}
		existing := []map[string]any{}
		for _, c := range items {
			if c.Labels[labelProject] == plan.Project {
				existing = append(existing, resourceRef(c))
			}
		}
		services := []map[string]any{}
		for _, s := range plan.Services {
			services = append(services, map[string]any{"service": s.Name, "name": s.Request.Name, "image": s.Request.Image, "ports": s.Request.Ports, "mounts": s.Request.Mounts, "network": s.Request.Network})
		}
		return map[string]any{"project": plan.Project, "services": services, "networks": plan.Networks, "existingContainers": existing, "replaceExisting": in.Request.ReplaceExisting, "dataVolumesKept": true}, nil
	}
	if err := p.ensureWritable(sess, method); err != nil {
		return nil, err
	}
	sess.writeMutex().Lock()
	defer sess.writeMutex().Unlock()
	switch method {
	case "docker/removeVolume":
		name, err := resourceID(raw, "name")
		if err != nil {
			return nil, err
		}
		used, err := referencedVolumeNames(ctx, cli)
		if err != nil {
			return nil, err
		}
		if used[name] {
			return nil, errors.New("Volume is referenced by a container (including stopped containers)")
		}
		if err = cli.VolumeRemove(ctx, name, false); err != nil {
			return nil, err
		}
	case "docker/removeNetwork":
		id, err := resourceID(raw, "networkId")
		if err != nil {
			return nil, err
		}
		n, err := cli.NetworkInspect(ctx, id, network.InspectOptions{})
		if err != nil {
			return nil, err
		}
		if protectedNetwork(n.Name, n.Driver) {
			return nil, errors.New("Built-in Docker networks cannot be removed")
		}
		if len(n.Containers) > 0 {
			return nil, errors.New("Disconnect attached containers before removing this network")
		}
		if err = cli.NetworkRemove(ctx, n.ID); err != nil {
			return nil, err
		}
	case "docker/connectNetwork", "docker/disconnectNetwork":
		id, err := resourceID(raw, "networkId")
		if err != nil {
			return nil, err
		}
		c, err := resourceID(raw, "containerId")
		if err != nil {
			return nil, err
		}
		n, err := cli.NetworkInspect(ctx, id, network.InspectOptions{})
		if err != nil {
			return nil, err
		}
		if protectedNetwork(n.Name, n.Driver) {
			return nil, errors.New("Use a user-defined network for connection changes")
		}
		if method == "docker/connectNetwork" {
			err = cli.NetworkConnect(ctx, n.ID, c, &network.EndpointSettings{})
		} else {
			err = cli.NetworkDisconnect(ctx, n.ID, c, false)
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, errMethodNotFound
	}
	return map[string]any{"success": true}, nil
}

func protectedNetwork(name, driver string) bool {
	return name == "bridge" || name == "host" || name == "none" || driver == "host" || driver == "null"
}

// A bounded snapshot may end halfway through a multiplexed Docker frame.
// Strip the final frame header too; never return binary framing to the model.
func decodeLogSnapshot(data []byte) []byte {
	if len(data) < 8 || data[0] > 2 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return data
	}
	var out []byte
	for len(data) >= 8 {
		length := int(binary.BigEndian.Uint32(data[4:8]))
		data = data[8:]
		n := min(length, len(data))
		out = append(out, data[:n]...)
		data = data[n:]
		if n < length {
			break
		}
	}
	return out
}
func resourceRef(c container.Summary) map[string]any {
	return map[string]any{"id": c.ID, "names": c.Names, "imageId": c.ImageID, "state": c.State}
}

// Canonical ordering keeps destructive previews stable across Docker list order.
func sortResources(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["id"]) < fmt.Sprint(items[j]["id"]) })
}

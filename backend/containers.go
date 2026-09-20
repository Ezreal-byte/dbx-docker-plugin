package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/go-connections/nat"
)

// ---------- engine ----------

func fetchConnectionInfo(ctx context.Context, cli dockerAPI) (DockerConnectionInfo, error) {
	ver, err := cli.ServerVersion(ctx)
	if err != nil {
		return DockerConnectionInfo{}, err
	}
	return DockerConnectionInfo{
		EngineVersion:     ver.Version,
		APIVersion:        ver.APIVersion,
		MinimumAPIVersion: ver.MinAPIVersion,
		OperatingSystem:   ver.Os,
		Architecture:      ver.Arch,
	}, nil
}

func (p *plugin) getEngineDetails(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cli := sess.client.cli

	ver, err := cli.ServerVersion(ctx)
	if err != nil {
		return nil, err
	}
	info, err := cli.Info(ctx)
	if err != nil {
		return nil, err
	}

	versionJSON, _ := json.Marshal(ver)
	infoJSON, _ := json.Marshal(info)
	var versionMap, infoMap map[string]any
	_ = json.Unmarshal(versionJSON, &versionMap)
	_ = json.Unmarshal(infoJSON, &infoMap)

	summary := DockerEngineSummary{
		EngineVersion:     ver.Version,
		APIVersion:        ver.APIVersion,
		MinimumAPIVersion: ver.MinAPIVersion,
		OperatingSystem:   firstNonEmpty(info.OperatingSystem, ver.Os),
		Architecture:      firstNonEmpty(info.Architecture, ver.Arch),
		KernelVersion:     info.KernelVersion,
		StorageDriver:     info.Driver,
		Containers:        info.Containers,
		ContainersRunning: info.ContainersRunning,
		ContainersPaused:  info.ContainersPaused,
		ContainersStopped: info.ContainersStopped,
		Images:            info.Images,
		DockerRootDir:     info.DockerRootDir,
		SecurityOptions:   nonNilStrings(info.SecurityOptions),
		Warnings:          nonNilStrings(info.Warnings),
	}
	return DockerEngineDetails{Version: versionMap, Info: infoMap, Summary: summary}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

// ---------- containers ----------

func (p *plugin) listContainers(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		All bool `json:"all"`
	}
	_ = json.Unmarshal(raw, &in)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	items, err := sess.client.cli.ContainerList(ctx, container.ListOptions{All: in.All})
	if err != nil {
		return nil, err
	}
	out := make([]DockerContainer, 0, len(items))
	for _, c := range items {
		ports := make([]DockerPort, 0, len(c.Ports))
		for _, pp := range c.Ports {
			ports = append(ports, DockerPort{
				IP:          pp.IP,
				PrivatePort: int(pp.PrivatePort),
				PublicPort:  int(pp.PublicPort),
				PortType:    pp.Type,
			})
		}
		ips := map[string]string{}
		if c.NetworkSettings != nil {
			for name, ep := range c.NetworkSettings.Networks {
				if ep != nil && ep.IPAddress != "" {
					ips[name] = ep.IPAddress
				}
			}
		}
		out = append(out, DockerContainer{
			ID:         c.ID,
			Names:      nonNilStrings(c.Names),
			Image:      c.Image,
			ImageID:    c.ImageID,
			Command:    c.Command,
			Created:    c.Created,
			State:      string(c.State),
			Status:     c.Status,
			Ports:      ports,
			Labels:     nonNilMap(c.Labels),
			NetworkIPs: ips,
		})
	}
	return out, nil
}

func (p *plugin) containerAction(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Action      string `json:"action"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("containerAction requires containerId")
	}
	if err := p.ensureWritable(sess, "lifecycle operations"); err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cli := sess.client.cli
	var err error
	switch in.Action {
	case "start":
		err = cli.ContainerStart(ctx, in.ContainerID, container.StartOptions{})
	case "pause":
		err = cli.ContainerPause(ctx, in.ContainerID)
	case "unpause":
		err = cli.ContainerUnpause(ctx, in.ContainerID)
	case "stop":
		err = cli.ContainerStop(ctx, in.ContainerID, container.StopOptions{})
	case "restart":
		err = cli.ContainerRestart(ctx, in.ContainerID, container.StopOptions{})
	default:
		return nil, fmt.Errorf("Unsupported container action: %s", in.Action)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (p *plugin) inspectContainer(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("inspectContainer requires containerId")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, rawJSON, err := sess.client.cli.ContainerInspectWithRaw(ctx, in.ContainerID, false)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(rawJSON, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (p *plugin) containerStats(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerIDs []string `json:"containerIds"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	ids := in.ContainerIDs
	if len(ids) > 128 {
		ids = ids[:128]
	}
	results := make([]DockerContainerStats, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			stats, err := sampleContainerStats(sess.client.cli, id)
			if err != nil {
				results[i] = DockerContainerStats{ContainerID: id}
				return
			}
			results[i] = stats
		}(i, id)
	}
	wg.Wait()
	out := make([]DockerContainerStats, 0, len(results))
	for _, r := range results {
		if r.ContainerID != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

func sampleContainerStats(cli dockerAPI, containerID string) (DockerContainerStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return DockerContainerStats{}, err
	}
	defer resp.Body.Close()
	var v container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return DockerContainerStats{}, err
	}
	return statsFromResponse(containerID, &v), nil
}

// statsFromResponse 逐字复刻分支 service.rs 的公式。
func statsFromResponse(containerID string, v *container.StatsResponse) DockerContainerStats {
	cpuCount := uint64(v.CPUStats.OnlineCPUs)
	if cpuCount == 0 {
		cpuCount = uint64(len(v.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuCount == 0 {
		cpuCount = 1
	}
	cpuDelta := saturatingSub(v.CPUStats.CPUUsage.TotalUsage, v.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := saturatingSub(v.CPUStats.SystemUsage, v.PreCPUStats.SystemUsage)
	cpuPercent := 0.0
	if systemDelta > 0 {
		cpuPercent = float64(cpuDelta) / float64(systemDelta) * float64(cpuCount) * 100.0
	}

	cache := max3(v.MemoryStats.Stats["total_inactive_file"], v.MemoryStats.Stats["inactive_file"], v.MemoryStats.Stats["cache"])
	memUsage := saturatingSub(v.MemoryStats.Usage, cache)
	memLimit := v.MemoryStats.Limit
	memPercent := 0.0
	if memLimit > 0 {
		memPercent = float64(memUsage) / float64(memLimit) * 100.0
	}

	var rx, tx uint64
	for _, ifc := range v.Networks {
		rx += ifc.RxBytes
		tx += ifc.TxBytes
	}
	var blockRead, blockWrite uint64
	for _, entry := range v.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			blockRead += entry.Value
		case "write":
			blockWrite += entry.Value
		}
	}

	return DockerContainerStats{
		ContainerID:   containerID,
		ReadAt:        v.Read.Format(time.RFC3339Nano),
		CPUPercent:    cpuPercent,
		MemoryUsage:   memUsage,
		MemoryLimit:   memLimit,
		MemoryPercent: memPercent,
		NetworkRx:     rx,
		NetworkTx:     tx,
		BlockRead:     blockRead,
		BlockWrite:    blockWrite,
	}
}

func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func max3(a, b, c uint64) uint64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

// ---------- create / remove container ----------

func validateResourceName(name, field string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("Docker %s must not be empty", field)
	}
	if strings.ContainsAny(trimmed, "\x00\n\r") {
		return "", fmt.Errorf("Docker %s contains invalid characters", field)
	}
	return trimmed, nil
}

func validateContainerPath(path string) (string, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
		return "", fmt.Errorf("Invalid container path: %q", path)
	}
	return path, nil
}

// prepareCreateContainer 复刻分支 service.rs 的请求体映射与校验。
func prepareCreateContainer(req DockerCreateContainerRequest) (*container.Config, *container.HostConfig, *network.NetworkingConfig, string, error) {
	name, err := validateResourceName(req.Name, "container name")
	if err != nil {
		return nil, nil, nil, "", err
	}
	image, err := validateResourceName(req.Image, "image")
	if err != nil {
		return nil, nil, nil, "", err
	}
	for _, env := range req.Environment {
		if strings.ContainsRune(env, '\x00') {
			return nil, nil, nil, "", errors.New("Environment entries must not contain NUL bytes")
		}
	}

	config := &container.Config{
		Image:  image,
		Labels: req.Labels,
	}
	if len(req.Command) > 0 {
		config.Cmd = strslice.StrSlice(req.Command)
	}
	if len(req.Environment) > 0 {
		config.Env = req.Environment
	}

	hostConfig := &container.HostConfig{}
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	for _, pb := range req.Ports {
		if pb.ContainerPort == 0 {
			return nil, nil, nil, "", errors.New("Container port must not be 0")
		}
		proto := strings.ToLower(strings.TrimSpace(pb.Protocol))
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			return nil, nil, nil, "", fmt.Errorf("Unsupported port protocol: %s", pb.Protocol)
		}
		portKey := nat.Port(fmt.Sprintf("%d/%s", pb.ContainerPort, proto))
		exposedPorts[portKey] = struct{}{}
		hostPort := ""
		if pb.HostPort != nil {
			hostPort = fmt.Sprintf("%d", *pb.HostPort)
		}
		portBindings[portKey] = []nat.PortBinding{{
			HostIP:   strings.TrimSpace(pb.HostIP),
			HostPort: hostPort,
		}}
	}
	if len(exposedPorts) > 0 {
		config.ExposedPorts = exposedPorts
		hostConfig.PortBindings = portBindings
	}

	for _, m := range req.Mounts {
		mtype := strings.ToLower(strings.TrimSpace(m.Type))
		if mtype != "bind" && mtype != "volume" {
			return nil, nil, nil, "", fmt.Errorf("Unsupported mount type: %s", m.Type)
		}
		source, err := validateResourceName(m.Source, "mount source")
		if err != nil {
			return nil, nil, nil, "", err
		}
		target, err := validateContainerPath(m.Target)
		if err != nil {
			return nil, nil, nil, "", err
		}
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:     mount.Type(mtype),
			Source:   source,
			Target:   target,
			ReadOnly: m.ReadOnly,
		})
	}

	restart := strings.ToLower(strings.TrimSpace(req.RestartPolicy))
	if restart == "" {
		restart = "no"
	}
	var restartMode container.RestartPolicyMode
	switch restart {
	case "no", "always", "unless-stopped", "on-failure":
		restartMode = container.RestartPolicyMode(restart)
	default:
		return nil, nil, nil, "", fmt.Errorf("Unsupported restart policy: %s", req.RestartPolicy)
	}
	hostConfig.RestartPolicy = container.RestartPolicy{Name: restartMode}

	var networkingConfig *network.NetworkingConfig
	if strings.TrimSpace(req.Network) != "" {
		networkName, err := validateResourceName(req.Network, "network")
		if err != nil {
			return nil, nil, nil, "", err
		}
		networkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
		}
	}
	return config, hostConfig, networkingConfig, name, nil
}

func (p *plugin) createContainer(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Request DockerCreateContainerRequest `json:"request"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "creating containers"); err != nil {
		return nil, err
	}
	config, hostConfig, networkingConfig, name, err := prepareCreateContainer(in.Request)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := sess.client.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, name)
	if err != nil {
		return nil, err
	}
	if resp.ID == "" {
		return nil, errors.New("Docker created the container but did not return its ID")
	}
	if in.Request.Start {
		if err := sess.client.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return nil, err
		}
	}
	return DockerCreateContainerResult{ID: resp.ID, Warnings: nonNilStrings(resp.Warnings)}, nil
}

func (p *plugin) removeContainer(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("removeContainer requires containerId")
	}
	if err := p.ensureWritable(sess, "removing containers"); err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sess.client.cli.ContainerRemove(ctx, in.ContainerID, container.RemoveOptions{Force: false, RemoveVolumes: false}); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

// ---------- guard ----------

func (p *plugin) ensureWritable(sess *session, action string) error {
	if sess.conn.effectiveReadOnly() {
		return fmt.Errorf("Docker connection is read-only; %s is disabled", action)
	}
	return nil
}

// ---------- exec 辅助（files.go 使用） ----------

func execContainerScript(cli dockerAPI, containerID string, script string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := append([]string{"/bin/sh", "-c", script, "dbx"}, args...)
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          cmd,
	})
	if err != nil {
		return nil, err
	}
	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Detach: false, Tty: false})
	if err != nil {
		return nil, err
	}
	defer attach.Close()
	var buf []byte
	scanner := bufio.NewReader(attach.Reader)
	dec := newStreamDecoder()
	tmp := make([]byte, 32*1024)
	for {
		n, err := scanner.Read(tmp)
		if n > 0 {
			buf = append(buf, dec.feed(tmp[:n])...)
		}
		if err != nil {
			break
		}
	}
	buf = append(buf, dec.flush()...)
	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, err
	}
	if inspect.ExitCode != 0 {
		trimmed := strings.TrimSpace(string(buf))
		if trimmed == "" {
			return nil, errors.New("Container command failed")
		}
		return nil, errors.New(trimmed)
	}
	return buf, nil
}

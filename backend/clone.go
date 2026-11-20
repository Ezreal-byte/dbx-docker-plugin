package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
)

func cloneConfiguration(source container.InspectResponse) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	if source.Config == nil || source.HostConfig == nil {
		return nil, nil, nil, errors.New("Source configuration is unavailable")
	}
	var config container.Config
	var host container.HostConfig
	_ = json.Unmarshal(jsonBytes(source.Config), &config)
	_ = json.Unmarshal(jsonBytes(source.HostConfig), &host)
	config.Hostname, config.Domainname, config.MacAddress = "", "", ""
	config.Image = source.Image // Pin the original image, even if its tag moved.
	config.Volumes = nil
	for key := range config.Labels {
		if strings.HasPrefix(key, "com.docker.compose.") {
			delete(config.Labels, key)
		}
	}
	// A copy is stopped and has no shared storage, published ports, device access
	// or references to the source's process/network namespaces.
	host.Binds, host.Mounts, host.VolumesFrom, host.Links, host.PortBindings = nil, nil, nil, nil, nil
	host.Devices, host.DeviceRequests, host.CapAdd, host.SecurityOpt = nil, nil, nil, nil
	host.DeviceCgroupRules = nil
	host.Privileged, host.AutoRemove, host.PublishAllPorts = false, false, false
	host.RestartPolicy = container.RestartPolicy{Name: "no"}
	host.ContainerIDFile, host.PidMode, host.IpcMode, host.UTSMode = "", "", "private", ""
	host.ExtraHosts = strslice.StrSlice{}
	if host.NetworkMode.IsContainer() || host.NetworkMode.IsHost() {
		host.NetworkMode = "default"
	}
	return &config, &host, &network.NetworkingConfig{}, nil
}

func (p *plugin) cloneContainer(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Name        string `json:"name"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, errInvalidParams
	}
	name, err := validateResourceName(in.Name, "container")
	if err != nil {
		return nil, err
	}
	if err := p.ensureWritable(sess, "copying containers"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(sess.context(), 60*time.Second)
	defer cancel()
	source, err := sess.client.cli.ContainerInspect(ctx, in.ContainerID)
	if err != nil {
		return nil, err
	}
	config, host, networks, err := cloneConfiguration(source)
	if err != nil {
		return nil, err
	}
	created, err := sess.client.cli.ContainerCreate(ctx, config, host, networks, nil, name)
	if err != nil {
		return nil, err
	}
	return DockerCreateContainerResult{ID: created.ID, Warnings: nonNilStrings(created.Warnings)}, nil
}

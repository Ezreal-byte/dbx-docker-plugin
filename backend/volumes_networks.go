package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

func (p *plugin) listVolumes(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := sess.client.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]DockerVolume, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		out = append(out, DockerVolume{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			Labels:     nonNilMap(v.Labels),
		})
	}
	return out, nil
}

func (p *plugin) createVolume(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Request DockerCreateVolumeRequest `json:"request"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "creating volumes"); err != nil {
		return nil, err
	}
	name, err := validateResourceName(in.Request.Name, "volume name")
	if err != nil {
		return nil, err
	}
	driver := in.Request.Driver
	if driver == "" {
		driver = "local"
	}
	if _, err := validateResourceName(driver, "volume driver"); err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := sess.client.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:       name,
		Driver:     driver,
		Labels:     in.Request.Labels,
		DriverOpts: in.Request.DriverOptions,
	})
	if err != nil {
		return nil, err
	}
	return DockerVolume{
		Name:       created.Name,
		Driver:     created.Driver,
		Mountpoint: created.Mountpoint,
		Scope:      created.Scope,
		Labels:     nonNilMap(created.Labels),
	}, nil
}

func (p *plugin) listNetworks(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	items, err := sess.client.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]DockerNetwork, 0, len(items))
	for _, n := range items {
		out = append(out, DockerNetwork{
			ID:         n.ID,
			Name:       n.Name,
			Driver:     n.Driver,
			Scope:      n.Scope,
			Internal:   n.Internal,
			Attachable: n.Attachable,
			Labels:     nonNilMap(n.Labels),
		})
	}
	return out, nil
}

func (p *plugin) createNetwork(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Request DockerCreateNetworkRequest `json:"request"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "creating networks"); err != nil {
		return nil, err
	}
	name, err := validateResourceName(in.Request.Name, "network name")
	if err != nil {
		return nil, err
	}
	driver := in.Request.Driver
	if driver == "" {
		driver = "bridge"
	}

	opts := network.CreateOptions{
		Driver:     driver,
		Internal:   in.Request.Internal,
		Attachable: in.Request.Attachable,
	}
	if in.Request.Subnet != "" || in.Request.Gateway != "" {
		opts.IPAM = &network.IPAM{
			Config: []network.IPAMConfig{{
				Subnet:  in.Request.Subnet,
				Gateway: in.Request.Gateway,
			}},
		}
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := sess.client.cli.NetworkCreate(ctx, name, opts)
	if err != nil {
		return nil, err
	}
	if resp.ID == "" {
		return nil, errors.New("Docker created the network but did not return its ID")
	}
	return DockerCreateNetworkResult{ID: resp.ID, Warning: resp.Warning}, nil
}

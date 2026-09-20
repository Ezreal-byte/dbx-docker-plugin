package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// ---------- 磁盘占用 ----------
// 对齐 `docker system df`：镜像层、容器可写层、卷、构建缓存各自的占用与可回收空间。

func (p *plugin) getDiskUsage(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usage, err := sess.client.cli.DiskUsage(ctx, dockertypes.DiskUsageOptions{})
	if err != nil {
		return nil, err
	}
	// Docker 的 /system/df 不返回网络，`docker system df` 也只是列出数量，
	// 这里单独统计数量以便和官方 CLI 的输出对齐。
	networks, err := sess.client.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}

	var imageSize, imageReclaimable int64
	for _, img := range usage.Images {
		if img == nil {
			continue
		}
		imageSize += img.Size
		if img.Containers == 0 {
			imageReclaimable += img.Size
		}
	}
	var containerSize, containerReclaimable int64
	for _, item := range usage.Containers {
		if item == nil {
			continue
		}
		containerSize += item.SizeRw
		if item.State != "running" {
			containerReclaimable += item.SizeRw
		}
	}
	var volumeSize, volumeReclaimable int64
	for _, item := range usage.Volumes {
		if item == nil {
			continue
		}
		volumeSize += item.UsageData.Size
		if item.UsageData.RefCount == 0 {
			volumeReclaimable += item.UsageData.Size
		}
	}
	var buildCacheSize, buildCacheReclaimable int64
	for _, record := range usage.BuildCache {
		if record == nil {
			continue
		}
		buildCacheSize += record.Size
		if !record.InUse {
			buildCacheReclaimable += record.Size
		}
	}

	return DockerDiskUsage{
		LayersSize: usage.LayersSize,
		Images: DockerDiskUsageCategory{
			Count: len(usage.Images), Size: imageSize, Reclaimable: imageReclaimable,
		},
		Containers: DockerDiskUsageCategory{
			Count: len(usage.Containers), Size: containerSize, Reclaimable: containerReclaimable,
		},
		Volumes: DockerDiskUsageCategory{
			Count: len(usage.Volumes), Size: volumeSize, Reclaimable: volumeReclaimable,
		},
		BuildCache: DockerDiskUsageCategory{
			Count: len(usage.BuildCache), Size: buildCacheSize, Reclaimable: buildCacheReclaimable,
		},
		Networks: DockerDiskUsageCategory{
			Count: len(networks),
		},
	}, nil
}

// ---------- 清理 ----------

func (p *plugin) prune(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Target string `json:"target"` // containers | images | volumes | networks
		All    bool   `json:"all"`    // 镜像：true = 删除所有未被使用的镜像（含带标签的）
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "pruning Docker resources"); err != nil {
		return nil, err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cli := sess.client.cli

	switch in.Target {
	case "containers":
		report, err := cli.ContainersPrune(ctx, filters.NewArgs())
		if err != nil {
			return nil, err
		}
		return DockerPruneResult{Deleted: nonNilStrings(report.ContainersDeleted), SpaceReclaimed: int64(report.SpaceReclaimed)}, nil
	case "images":
		args := filters.NewArgs()
		if in.All {
			args = filters.NewArgs(filters.Arg("dangling", "false"))
		} else {
			args = filters.NewArgs(filters.Arg("dangling", "true"))
		}
		report, err := cli.ImagesPrune(ctx, args)
		if err != nil {
			return nil, err
		}
		deleted := make([]string, 0, len(report.ImagesDeleted))
		for _, item := range report.ImagesDeleted {
			switch {
			case item.Deleted != "":
				deleted = append(deleted, item.Deleted)
			case item.Untagged != "":
				deleted = append(deleted, item.Untagged)
			}
		}
		return DockerPruneResult{Deleted: deleted, SpaceReclaimed: int64(report.SpaceReclaimed)}, nil
	case "volumes":
		report, err := cli.VolumesPrune(ctx, filters.NewArgs())
		if err != nil {
			return nil, err
		}
		return DockerPruneResult{Deleted: nonNilStrings(report.VolumesDeleted), SpaceReclaimed: int64(report.SpaceReclaimed)}, nil
	case "networks":
		report, err := cli.NetworksPrune(ctx, filters.NewArgs())
		if err != nil {
			return nil, err
		}
		return DockerPruneResult{Deleted: nonNilStrings(report.NetworksDeleted)}, nil
	default:
		return nil, fmt.Errorf("Unsupported prune target: %s", in.Target)
	}
}

// ---------- 容器重命名 ----------

func (p *plugin) renameContainer(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("renameContainer requires containerId")
	}
	if err := p.ensureWritable(sess, "renaming containers"); err != nil {
		return nil, err
	}
	name, err := validateResourceName(in.Name, "container name")
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.client.cli.ContainerRename(ctx, in.ContainerID, name); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "name": name}, nil
}

// ---------- 镜像打标签 ----------

func (p *plugin) tagImage(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ImageID    string `json:"imageId"`
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ImageID == "" {
		return nil, errors.New("tagImage requires imageId")
	}
	if err := p.ensureWritable(sess, "tagging images"); err != nil {
		return nil, err
	}
	repository, err := validateResourceName(in.Repository, "image repository")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(repository, " \t") {
		return nil, errors.New("Docker image repository must not contain whitespace")
	}
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = "latest"
	}
	if strings.ContainsAny(tag, " \t:/") {
		return nil, errors.New("Docker image tag must not contain whitespace, ':' or '/'")
	}
	reference := repository + ":" + tag

	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.client.cli.ImageTag(ctx, in.ImageID, reference); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "reference": reference}, nil
}

// ---------- 删除单个镜像标签 ----------
// 与 removeImage 不同：removeImage 会删掉镜像的全部标签，这里只摘掉一个引用，
// 便于清理打错的标签而不影响其它 tag 与镜像数据。

func (p *plugin) untagImage(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Reference) == "" {
		return nil, errors.New("untagImage requires reference")
	}
	if err := p.ensureWritable(sess, "removing image tags"); err != nil {
		return nil, err
	}
	reference := strings.TrimSpace(in.Reference)
	if strings.ContainsAny(reference, " \t") || !strings.Contains(reference, ":") {
		return nil, fmt.Errorf("Invalid image reference: %q", reference)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := sess.client.cli.ImageRemove(ctx, reference, image.RemoveOptions{}); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

// ---------- 镜像分层历史 ----------

func (p *plugin) imageHistory(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ImageID string `json:"imageId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ImageID == "" {
		return nil, errors.New("imageHistory requires imageId")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := sess.client.cli.ImageHistory(ctx, in.ImageID)
	if err != nil {
		return nil, err
	}
	out := make([]DockerImageLayer, 0, len(items))
	for _, item := range items {
		out = append(out, DockerImageLayer{
			ID:        item.ID,
			Created:   item.Created,
			CreatedBy: item.CreatedBy,
			Size:      item.Size,
			Comment:   item.Comment,
			Tags:      nonNilStrings(item.Tags),
		})
	}
	return out, nil
}

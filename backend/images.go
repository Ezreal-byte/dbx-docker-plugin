package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

func (p *plugin) listImages(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := sess.client.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	out := make([]DockerImage, 0, len(items))
	for _, img := range items {
		out = append(out, DockerImage{
			ID:          img.ID,
			RepoTags:    nonNilStrings(img.RepoTags),
			RepoDigests: nonNilStrings(img.RepoDigests),
			Created:     img.Created,
			Size:        img.Size,
			Labels:      nonNilMap(img.Labels),
		})
	}
	return out, nil
}

// pullImage / pushImage / exportImage 都在后台 goroutine 里跑流，立即返回 sessionId；
// 进度与原始字节经 emitter 以 binary 帧推给前端。

func (p *plugin) pullImage(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		Image     string             `json:"image"`
		Auth      DockerRegistryAuth `json:"auth"`
		SessionID string             `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Image) == "" {
		return nil, errors.New("pullImage requires image")
	}
	if in.SessionID == "" {
		return nil, errors.New("pullImage requires sessionId")
	}
	if err := p.ensureWritable(sess, "pulling images"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.streams.add(&streamSession{sessionID: in.SessionID, connectionID: sess.conn.ID, kind: "pull", cancel: cancel})

	go func() {
		defer p.streams.remove(in.SessionID)
		header := transferHeader{SessionID: in.SessionID, Kind: "pull", Direction: "download", Image: in.Image, Status: "running"}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)

		opts := image.PullOptions{}
		if encoded := encodeRegistryAuth(in.Auth); encoded != "" {
			opts.RegistryAuth = encoded
		}
		reader, err := sess.client.cli.ImagePull(ctx, in.Image, opts)
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
			return
		}
		defer reader.Close()
		status := streamJSONLines(ctx, reader, func(chunk []byte) {
			emitFrame(emitter, channelTransfer, frameKindData, transferHeader{
				SessionID: in.SessionID, Kind: "pull", Direction: "download", Image: in.Image,
				Status: "running", DataLen: len(chunk),
			}, chunk)
		})
		header.Status = status
		if ctx.Err() != nil {
			header.Status = "cancelled"
		}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

func (p *plugin) pushImage(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		SourceImageID   string             `json:"sourceImageId"`
		TargetReference string             `json:"targetReference"`
		Auth            DockerRegistryAuth `json:"auth"`
		SessionID       string             `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.SourceImageID == "" || in.TargetReference == "" {
		return nil, errors.New("pushImage requires sourceImageId and targetReference")
	}
	if in.SessionID == "" {
		return nil, errors.New("pushImage requires sessionId")
	}
	if err := p.ensureWritable(sess, "pushing images"); err != nil {
		return nil, err
	}

	// 复刻分支：rsplit('/') 与 rsplit(':') 拆 repository:tag。
	repo, tag := splitPushReference(in.TargetReference)
	if repo == "" || tag == "" {
		return nil, fmt.Errorf("Invalid push target reference: %s", in.TargetReference)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.streams.add(&streamSession{sessionID: in.SessionID, connectionID: sess.conn.ID, kind: "push", cancel: cancel})

	go func() {
		defer p.streams.remove(in.SessionID)
		header := transferHeader{SessionID: in.SessionID, Kind: "push", Direction: "upload", Image: in.TargetReference, Status: "running"}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)

		if err := sess.client.cli.ImageTag(ctx, in.SourceImageID, in.TargetReference); err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
			return
		}
		opts := image.PushOptions{}
		if encoded := encodeRegistryAuth(in.Auth); encoded != "" {
			opts.RegistryAuth = encoded
		}
		reader, err := sess.client.cli.ImagePush(ctx, in.TargetReference, opts)
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
			return
		}
		defer reader.Close()
		status := streamJSONLines(ctx, reader, func(chunk []byte) {
			emitFrame(emitter, channelTransfer, frameKindData, transferHeader{
				SessionID: in.SessionID, Kind: "push", Direction: "upload", Image: in.TargetReference,
				Status: "running", DataLen: len(chunk),
			}, chunk)
		})
		header.Status = status
		if ctx.Err() != nil {
			header.Status = "cancelled"
		}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

func splitPushReference(ref string) (repo, tag string) {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

func (p *plugin) exportImage(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		ImageID   string `json:"imageId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ImageID == "" {
		return nil, errors.New("exportImage requires imageId")
	}
	if in.SessionID == "" {
		return nil, errors.New("exportImage requires sessionId")
	}
	// 分支明确 export 不受 read_only 约束。
	ctx, cancel := context.WithCancel(context.Background())
	p.streams.add(&streamSession{sessionID: in.SessionID, connectionID: sess.conn.ID, kind: "export", cancel: cancel})

	go func() {
		defer p.streams.remove(in.SessionID)
		header := transferHeader{SessionID: in.SessionID, Kind: "export", Direction: "download", Image: in.ImageID, Status: "running"}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)

		reader, err := sess.client.cli.ImageSave(ctx, []string{in.ImageID})
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
			return
		}
		defer reader.Close()

		buf := make([]byte, 512*1024)
		streamErr := error(nil)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				emitFrame(emitter, channelTransfer, frameKindData, transferHeader{
					SessionID: in.SessionID, Kind: "export", Direction: "download", Image: in.ImageID,
					Status: "running", DataLen: n,
				}, buf[:n])
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				streamErr = readErr
				break
			}
			if ctx.Err() != nil {
				break
			}
		}
		header.Status = "done"
		if streamErr != nil {
			header.Status, header.Error = "error", streamErr.Error()
		} else if ctx.Err() != nil {
			header.Status = "cancelled"
		}
		emitFrame(emitter, channelTransfer, frameKindJSON, header, nil)
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

func (p *plugin) removeImage(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		ImageID string `json:"imageId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ImageID == "" {
		return nil, errors.New("removeImage requires imageId")
	}
	if err := p.ensureWritable(sess, "removing images"); err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cli := sess.client.cli

	// 复刻分支：列出镜像找到该 id 的 tags，按 tag 逐个删；无 tag 直接按 id 删。
	items, err := cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, img := range items {
		if img.ID == in.ImageID {
			for _, t := range img.RepoTags {
				if t != "<none>:<none>" {
					tags = append(tags, t)
				}
			}
			break
		}
	}
	opts := image.RemoveOptions{Force: false, PruneChildren: false}
	if len(tags) == 0 {
		if _, err := cli.ImageRemove(ctx, in.ImageID, opts); err != nil {
			return nil, err
		}
		return map[string]any{"success": true}, nil
	}
	for _, tag := range tags {
		if _, err := cli.ImageRemove(ctx, tag, opts); err != nil {
			return nil, err
		}
	}
	return map[string]any{"success": true}, nil
}

// streamJSONLines 读取 docker 进度流（每行一个 JSON），把原始块透传给 onChunk。
// 返回 "done" 或 "error"。
func streamJSONLines(ctx context.Context, reader io.Reader, onChunk func([]byte)) string {
	buf := make([]byte, 64*1024)
	for {
		if ctx.Err() != nil {
			return "cancelled"
		}
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onChunk(chunk)
		}
		if err == io.EOF {
			return "done"
		}
		if err != nil {
			return "error"
		}
	}
}

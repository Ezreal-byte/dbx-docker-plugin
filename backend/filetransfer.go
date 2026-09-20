package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

// filetransfer.go 容器文件的下载与上传。
//
// 下载：exec 固定脚本 `head -c <size> -- "$1"`，输出经 binary channel docker-file 分块推给前端。
// 上传：exec 固定脚本 `head -c "$1" > "$2"`，文件内容作为 stdin 经 docker-exec 通道分块写入。
//       `head -c N` 读满 N 字节即退出，因此不需要半关闭 stdin（hijacked 连接没有 CloseWrite），
//       由前端按精确字节数发送，写完后后端自然收到退出帧。
//
// 两条路径都只接受「容器内绝对路径」这一个参数，不拼接用户输入到脚本里，脚本本身是常量。

// maxFileTransferBytes 单次传输上限。下载会在前端内存中合并，上传需要选择一个本地文件；
// 过大的上限会直接打爆渲染进程内存。
const maxFileTransferBytes = 256 * 1024 * 1024

const channelFile = "docker-file"

type fileHeader struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Status    string `json:"status"` // running | done | error
	Size      int64  `json:"size"`
	Error     string `json:"error,omitempty"`
	DataLen   int    `json:"dataLen,omitempty"`
}

// fileSize 读取容器内文件的字节数。
func fileSize(sess *session, containerID, path string) (int64, error) {
	out, err := execContainerScript(sess.client.cli, containerID, `stat -c %s -- "$1"`, path)
	if err != nil {
		return 0, fmt.Errorf("Cannot read container file size: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("Container did not report a file size for %q", path)
	}
	return size, nil
}

// startFileDownload 流式读取容器内文件。
func (p *plugin) startFileDownload(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Path        string `json:"path"`
		SessionID   string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" || in.Path == "" {
		return nil, errors.New("startFileDownload requires containerId and path")
	}
	if in.SessionID == "" {
		return nil, errors.New("startFileDownload requires sessionId")
	}
	if _, err := validateContainerPath(in.Path); err != nil {
		return nil, err
	}

	size, err := fileSize(sess, in.ContainerID, in.Path)
	if err != nil {
		return nil, err
	}
	if size > maxFileTransferBytes {
		return nil, fmt.Errorf("Container file is %d bytes; the %d byte transfer limit was exceeded", size, int64(maxFileTransferBytes))
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.streams.add(&streamSession{sessionID: in.SessionID, connectionID: sess.conn.ID, kind: "download", cancel: cancel})

	go func() {
		defer p.streams.remove(in.SessionID)
		header := fileHeader{SessionID: in.SessionID, Path: in.Path, Status: "running", Size: size}

		script := fmt.Sprintf(`head -c %d -- "$1"`, size)
		created, err := sess.client.cli.ContainerExecCreate(ctx, in.ContainerID, container.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Tty:          false,
			Cmd:          []string{"/bin/sh", "-c", script, "dbx", in.Path},
		})
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelFile, frameKindJSON, header, nil)
			return
		}
		attach, err := sess.client.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: false})
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelFile, frameKindJSON, header, nil)
			return
		}
		defer attach.Close()

		emitFrame(emitter, channelFile, frameKindJSON, header, nil)

		// Tty=false 的 exec 输出是多路复用帧，需要先解码再透传。
		decoder := newStreamDecoder()
		var sent int64
		buf := make([]byte, 64*1024)
		for {
			n, readErr := attach.Reader.Read(buf)
			if n > 0 {
				chunk := decoder.feed(buf[:n])
				if len(chunk) > 0 {
					sent += int64(len(chunk))
					emitFrame(emitter, channelFile, frameKindData, fileHeader{
						SessionID: in.SessionID, Path: in.Path, Status: "running", Size: size, DataLen: len(chunk),
					}, chunk)
				}
			}
			if readErr == io.EOF {
				if rest := decoder.flush(); len(rest) > 0 {
					sent += int64(len(rest))
					emitFrame(emitter, channelFile, frameKindData, fileHeader{
						SessionID: in.SessionID, Path: in.Path, Status: "running", Size: size, DataLen: len(rest),
					}, rest)
				}
				break
			}
			if readErr != nil {
				header.Status, header.Error = "error", readErr.Error()
				emitFrame(emitter, channelFile, frameKindJSON, header, nil)
				return
			}
			if ctx.Err() != nil {
				header.Status = "cancelled"
				emitFrame(emitter, channelFile, frameKindJSON, header, nil)
				return
			}
		}

		if sent != size {
			header.Status = "error"
			header.Error = fmt.Sprintf("Container returned %d bytes but the file is %d bytes", sent, size)
			emitFrame(emitter, channelFile, frameKindJSON, header, nil)
			return
		}
		header.Status = "done"
		emitFrame(emitter, channelFile, frameKindJSON, header, nil)
	}()

	return map[string]any{"sessionId": in.SessionID, "size": size}, nil
}

// startFileUpload 在容器内创建/覆盖文件，内容由前端经 docker-exec 通道写入 stdin。
func (p *plugin) startFileUpload(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		ContainerID string `json:"containerId"`
		Path        string `json:"path"`
		SessionID   string `json:"sessionId"`
		Size        int64  `json:"size"`
		Mode        string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" || in.Path == "" {
		return nil, errors.New("startFileUpload requires containerId and path")
	}
	if in.SessionID == "" {
		return nil, errors.New("startFileUpload requires sessionId")
	}
	if err := p.ensureWritable(sess, "uploading container files"); err != nil {
		return nil, err
	}
	if _, err := validateContainerPath(in.Path); err != nil {
		return nil, err
	}
	if in.Size < 0 || in.Size > maxFileTransferBytes {
		return nil, fmt.Errorf("Upload size %d is outside the supported range", in.Size)
	}
	mode := "wb"
	switch in.Mode {
	case "", "overwrite":
	case "append":
		mode = "ab"
	default:
		return nil, fmt.Errorf("Unsupported upload mode: %s", in.Mode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// dd 的 oflag=append + conv=notrunc 保证追加语义；缺省时直接覆盖。
	script := `head -c "$1" > "$2"`
	if mode == "ab" {
		script = `head -c "$1" >> "$2"`
	}
	created, err := sess.client.cli.ContainerExecCreate(ctx, in.ContainerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          []string{"/bin/sh", "-c", script, "dbx", strconv.FormatInt(in.Size, 10), in.Path},
	})
	if err != nil {
		return nil, err
	}
	attach, err := sess.client.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return nil, err
	}

	// 复用 exec 会话注册表，前端就能用既有的 docker-exec 二进制通道写 stdin。
	es := &execSession{
		sessionID:    in.SessionID,
		connectionID: sess.conn.ID,
		execID:       created.ID,
		conn:         attach.Conn,
	}
	p.execs.stop(in.SessionID)
	p.execs.add(es)

	go func() {
		defer p.execs.remove(in.SessionID)
		defer es.close()
		emitFrame(emitter, channelExec, frameKindJSON, execHeader{SessionID: in.SessionID, Status: "running"}, nil)

		decoder := newStreamDecoder()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := attach.Reader.Read(buf)
			if n > 0 {
				if chunk := decoder.feed(buf[:n]); len(chunk) > 0 {
					emitFrame(emitter, channelExec, frameKindData, execHeader{
						SessionID: in.SessionID, Status: "running", DataLen: len(chunk),
					}, chunk)
				}
			}
			if readErr != nil {
				break
			}
		}
		if rest := decoder.flush(); len(rest) > 0 {
			emitFrame(emitter, channelExec, frameKindData, execHeader{
				SessionID: in.SessionID, Status: "running", DataLen: len(rest),
			}, rest)
		}
		exitCode := inspectExecExitCode(sess, created.ID)
		status, message := "done", ""
		if exitCode != 0 {
			status, message = "error", fmt.Sprintf("Container write command exited with code %d", exitCode)
		}
		emitFrame(emitter, channelExec, frameKindJSON, execHeader{
			SessionID: in.SessionID, Status: status, ExitCode: exitCode, Error: message,
		}, nil)
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

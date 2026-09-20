package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

// exec.go 容器内交互式终端：docker exec（Tty）attach + 双向 binary channel。
//
// 帧格式与 streams.go 完全一致：1 字节 kind + 4 字节 BE 头长度 + JSON 头 + 原始数据。
// 下行（后端 → 前端）复用 encodeFrame；上行（前端 → 后端）由前端构造同构帧，
// 因此 HandleBinary 用 decodeExecInput 解析同一种布局。

const channelExec = "docker-exec"

// execFrameHeaderBytes 与 streams.go encodeFrame 的帧头一致。
const execFrameHeaderBytes = 5

// execHeader 双向共用的 JSON 头：下行带 status/exitCode，上行带 kind。
type execHeader struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind,omitempty"`   // 上行：stdin
	Status    string `json:"status,omitempty"` // 下行：running | done | error
	Error     string `json:"error,omitempty"`
	ExitCode  int    `json:"exitCode,omitempty"`
	DataLen   int    `json:"dataLen,omitempty"`
}

// execSession 是一条已 attach 的容器内终端。
type execSession struct {
	sessionID    string
	connectionID string
	execID       string
	conn         net.Conn
	writeMu      sync.Mutex
	closeOnce    sync.Once
}

func (e *execSession) close() {
	e.closeOnce.Do(func() {
		_ = e.conn.Close()
	})
}

func (e *execSession) write(data []byte) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	_, err := e.conn.Write(data)
	return err
}

// execRegistry 按 sessionId 跟踪容器终端，断开连接时按 connectionId 全部关闭。
type execRegistry struct {
	mu      sync.Mutex
	entries map[string]*execSession
}

func newExecRegistry() *execRegistry {
	return &execRegistry{entries: map[string]*execSession{}}
}

func (r *execRegistry) add(s *execSession) {
	r.mu.Lock()
	r.entries[s.sessionID] = s
	r.mu.Unlock()
}

func (r *execRegistry) remove(sessionID string) {
	r.mu.Lock()
	delete(r.entries, sessionID)
	r.mu.Unlock()
}

func (r *execRegistry) get(sessionID string) *execSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[sessionID]
}

func (r *execRegistry) stop(sessionID string) bool {
	r.mu.Lock()
	s, ok := r.entries[sessionID]
	delete(r.entries, sessionID)
	r.mu.Unlock()
	if ok {
		s.close()
	}
	return ok
}

func (r *execRegistry) stopByConnection(connectionID string) {
	r.mu.Lock()
	var stops []*execSession
	for id, s := range r.entries {
		if s.connectionID == connectionID {
			stops = append(stops, s)
			delete(r.entries, id)
		}
	}
	r.mu.Unlock()
	for _, s := range stops {
		s.close()
	}
}

// startExec 建立容器内终端：exec create + attach（Tty），随后把输出推给前端。
func (p *plugin) startExec(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		ContainerID string   `json:"containerId"`
		SessionID   string   `json:"sessionId"`
		Command     []string `json:"command"`
		Cols        int      `json:"cols"`
		Rows        int      `json:"rows"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("startExec requires containerId")
	}
	if in.SessionID == "" {
		return nil, errors.New("startExec requires sessionId")
	}
	// docker exec 可以在容器内执行任意命令，按写操作处理。
	if err := p.ensureWritable(sess, "container terminals"); err != nil {
		return nil, err
	}

	command := sanitizeExecCommand(in.Command)
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}
	cols, rows := in.Cols, in.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := sess.client.cli.ContainerExecCreate(ctx, in.ContainerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          command,
	})
	if err != nil {
		return nil, err
	}
	attach, err := sess.client.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	// 首次尺寸需在 attach 之后设置，否则终端会以 80x24 渲染。
	_ = sess.client.cli.ContainerExecResize(ctx, created.ID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})

	es := &execSession{
		sessionID:    in.SessionID,
		connectionID: sess.conn.ID,
		execID:       created.ID,
		conn:         attach.Conn,
	}
	// 同名会话重复打开时先关掉旧的，避免泄漏 attach 连接。
	p.execs.stop(in.SessionID)
	p.execs.add(es)

	go func() {
		defer p.execs.remove(in.SessionID)
		defer es.close()
		reader := attach.Reader
		emitFrame(emitter, channelExec, frameKindJSON, execHeader{SessionID: in.SessionID, Status: "running"}, nil)

		buf := make([]byte, 32*1024)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				emitFrame(emitter, channelExec, frameKindData, execHeader{
					SessionID: in.SessionID, Status: "running", DataLen: n,
				}, buf[:n])
			}
			if readErr != nil {
				break
			}
		}
		emitFrame(emitter, channelExec, frameKindJSON, execHeader{
			SessionID: in.SessionID,
			Status:    "done",
			ExitCode:  inspectExecExitCode(sess, created.ID),
		}, nil)
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

// inspectExecExitCode 读取退出码；查询失败时返回 0（终端已结束，不必再报错）。
func inspectExecExitCode(sess *session, execID string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspect, err := sess.client.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return 0
	}
	return inspect.ExitCode
}

func (p *plugin) execResize(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return nil, errors.New("execResize requires sessionId")
	}
	es := p.execs.get(in.SessionID)
	if es == nil {
		// 终端已结束，尺寸同步按幂等处理。
		return map[string]any{"success": false}, nil
	}
	cols, rows := in.Cols, in.Rows
	if cols <= 0 || rows <= 0 {
		return nil, errors.New("execResize requires positive cols and rows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sess.client.cli.ContainerExecResize(ctx, es.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (p *plugin) stopExec(raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return nil, errors.New("stopExec requires sessionId")
	}
	p.execs.stop(in.SessionID)
	return map[string]any{"success": true}, nil
}

// HandleBinary 接收前端经 binary channel 上行的终端输入。
func (p *plugin) HandleBinary(channel string, data []byte, _ *sdk.Emitter) *sdk.PluginError {
	if channel != channelExec {
		// 未知通道不阻断协议，交给宿主日志即可。
		return sdk.NewError(-32601, "Unsupported binary channel: "+channel)
	}
	header, payload, err := decodeExecInput(data)
	if err != nil {
		return sdk.NewError(-32600, err.Error())
	}
	if header.SessionID == "" {
		return sdk.NewError(-32602, "exec input requires sessionId")
	}
	if header.Kind != "" && header.Kind != "stdin" {
		return sdk.NewError(-32602, "Unsupported exec input kind: "+header.Kind)
	}
	es := p.execs.get(header.SessionID)
	if es == nil {
		// 会话已结束：丢弃输入，不回错误（前端可能仍在收尾）。
		return nil
	}
	if writeErr := es.write(payload); writeErr != nil {
		return sdk.NewError(-32000, writeErr.Error())
	}
	return nil
}

// decodeExecInput 解析前端上行帧（与下行同构）。
func decodeExecInput(data []byte) (execHeader, []byte, error) {
	var header execHeader
	if len(data) < execFrameHeaderBytes {
		return header, nil, errors.New("exec input frame is too short")
	}
	headerLen := int(binary.BigEndian.Uint32(data[1:execFrameHeaderBytes]))
	if len(data) < execFrameHeaderBytes+headerLen {
		return header, nil, errors.New("exec input frame header is truncated")
	}
	if err := json.Unmarshal(data[execFrameHeaderBytes:execFrameHeaderBytes+headerLen], &header); err != nil {
		return header, nil, err
	}
	return header, data[execFrameHeaderBytes+headerLen:], nil
}

// sanitizeExecCommand 过滤空参数与 NUL，限制命令长度。
func sanitizeExecCommand(command []string) []string {
	if len(command) > 32 {
		command = command[:32]
	}
	out := make([]string, 0, len(command))
	for _, arg := range command {
		if arg == "" {
			continue
		}
		if len(arg) > 4096 {
			continue
		}
		cleaned := make([]rune, 0, len(arg))
		for _, r := range arg {
			if r == 0 {
				continue
			}
			cleaned = append(cleaned, r)
		}
		out = append(out, string(cleaned))
	}
	return out
}

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

// ---------- 多路复用帧解码 ----------
// 逐字复刻分支 service.rs 的 decode_multiplexed_bytes / decode_multiplexed_stream_chunk：
// 帧头 8 字节 [type(0..=2), 0,0,0, len:u32be]；首字节非法则整段视为裸文本（tty 容器）。

// streamDecoder 维护跨 chunk 缓冲，用于 follow 日志流与 exec 输出。
type streamDecoder struct {
	buf []byte
}

func newStreamDecoder() *streamDecoder { return &streamDecoder{} }

// feed 追加一段数据并吐出所有完整帧（或裸文本）。
func (d *streamDecoder) feed(chunk []byte) []byte {
	d.buf = append(d.buf, chunk...)
	var out []byte
	for len(d.buf) > 0 {
		b0 := d.buf[0]
		if b0 > 2 {
			// 非多路复用流：全部当裸文本。
			out = append(out, d.buf...)
			d.buf = d.buf[:0]
			break
		}
		if len(d.buf) < 8 {
			break
		}
		length := int(binary.BigEndian.Uint32(d.buf[4:8]))
		if len(d.buf) < 8+length {
			break
		}
		out = append(out, d.buf[8:8+length]...)
		d.buf = d.buf[8+length:]
	}
	return out
}

func (d *streamDecoder) flush() []byte {
	if len(d.buf) == 0 {
		return nil
	}
	out := d.buf
	d.buf = nil
	return out
}

// decodeMultiplexedBytes 一次性解码完整缓冲（exec 输出）。
func decodeMultiplexedBytes(data []byte) []byte {
	d := newStreamDecoder()
	out := d.feed(data)
	out = append(out, d.flush()...)
	return out
}

// ---------- 日志 follow ----------

func (p *plugin) startLogs(sess *session, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	var in struct {
		ContainerID string          `json:"containerId"`
		Options     DockerLogOptions `json:"options"`
		SessionID   string          `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ContainerID == "" {
		return nil, errors.New("startLogs requires containerId")
	}
	if in.SessionID == "" {
		return nil, errors.New("startLogs requires sessionId")
	}
	tail := in.Options.Tail
	if tail <= 0 {
		tail = 500
	}
	if tail > 10000 {
		tail = 10000
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.streams.add(&streamSession{sessionID: in.SessionID, connectionID: sess.conn.ID, kind: "log", cancel: cancel})

	go func() {
		defer p.streams.remove(in.SessionID)
		header := logHeader{SessionID: in.SessionID, Status: "running"}

		reader, err := sess.client.cli.ContainerLogs(ctx, in.ContainerID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Tail:       fmt.Sprintf("%d", tail),
			Timestamps: in.Options.Timestamps,
		})
		if err != nil {
			header.Status, header.Error = "error", err.Error()
			emitFrame(emitter, channelLog, frameKindJSON, header, nil)
			return
		}
		defer reader.Close()

		emitFrame(emitter, channelLog, frameKindJSON, header, nil)

		// Tty=false 的容器输出是多路复用帧；逐 chunk 解码后推给前端。
		dec := newStreamDecoder()
		buf := make([]byte, 32*1024)
		for {
			if ctx.Err() != nil {
				header.Status = "done"
				emitFrame(emitter, channelLog, frameKindJSON, header, nil)
				return
			}
			n, readErr := reader.Read(buf)
			if n > 0 {
				chunk := dec.feed(buf[:n])
				if len(chunk) > 0 {
					emitFrame(emitter, channelLog, frameKindData, logHeader{
						SessionID: in.SessionID, Status: "running", DataLen: len(chunk),
					}, chunk)
				}
			}
			if readErr == io.EOF {
				if rest := dec.flush(); len(rest) > 0 {
					emitFrame(emitter, channelLog, frameKindData, logHeader{
						SessionID: in.SessionID, Status: "running", DataLen: len(rest),
					}, rest)
				}
				header.Status = "done"
				emitFrame(emitter, channelLog, frameKindJSON, header, nil)
				return
			}
			if readErr != nil {
				if ctx.Err() != nil {
					header.Status = "done"
				} else {
					header.Status, header.Error = "error", readErr.Error()
				}
				emitFrame(emitter, channelLog, frameKindJSON, header, nil)
				return
			}
		}
	}()

	return map[string]any{"sessionId": in.SessionID}, nil
}

func (p *plugin) stopLogs(raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return nil, errors.New("stopLogs requires sessionId")
	}
	p.streams.stop(in.SessionID)
	return map[string]any{"success": true}, nil
}

func (p *plugin) stopStream(raw json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return nil, errors.New("stopStream requires sessionId")
	}
	p.streams.stop(in.SessionID)
	return map[string]any{"success": true}, nil
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"sync"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

// streamRegistry 跟踪所有进行中的流会话（日志 follow、镜像 pull/push/export）。
// 前端用 sessionId 显式停止；断开连接时按 connectionId 全部取消。
type streamRegistry struct {
	entries map[string]*streamSession
	mu      sync.Mutex
}

type streamSession struct {
	sessionID    string
	connectionID string
	kind         string // log | pull | push | export
	cancel       context.CancelFunc
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{entries: map[string]*streamSession{}}
}

func (r *streamRegistry) add(s *streamSession) {
	r.mu.Lock()
	r.entries[s.sessionID] = s
	r.mu.Unlock()
}

func (r *streamRegistry) remove(sessionID string) {
	r.mu.Lock()
	delete(r.entries, sessionID)
	r.mu.Unlock()
}

func (r *streamRegistry) stop(sessionID string) bool {
	r.mu.Lock()
	s, ok := r.entries[sessionID]
	delete(r.entries, sessionID)
	r.mu.Unlock()
	if ok {
		s.cancel()
	}
	return ok
}

func (r *streamRegistry) stopByConnection(connectionID string) {
	r.mu.Lock()
	var stops []*streamSession
	for id, s := range r.entries {
		if s.connectionID == connectionID {
			stops = append(stops, s)
			delete(r.entries, id)
		}
	}
	r.mu.Unlock()
	for _, s := range stops {
		s.cancel()
	}
}

// ---------- 帧格式 ----------
// 与前端约定：每个 binary 帧 = 1 字节 kind + 4 字节 big-endian 长度 + JSON 头 + 可选原始字节。
// kind: 0=JSON 头（仅头，无数据） 1=数据块

const (
	frameKindJSON = 0
	frameKindData = 1
)

// binary channel 名（前端按 channel 区分日志流与传输流）。
const (
	channelLog      = "docker-log"
	channelTransfer = "docker-transfer"
)

// transferHeader 是 pull/push/export 帧的 JSON 头。
type transferHeader struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`      // pull | push | export
	Direction string `json:"direction"` // download | upload
	Image     string `json:"image"`
	Status    string `json:"status"` // running | done | error | cancelled
	Error     string `json:"error,omitempty"`
	DataLen   int    `json:"dataLen,omitempty"` // 本帧携带的原始字节数（export 数据块）
}

// logHeader 是日志帧的 JSON 头。
type logHeader struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"` // running | done | error
	Error     string `json:"error,omitempty"`
	DataLen   int    `json:"dataLen,omitempty"`
}

func encodeFrame(kind byte, header any, data []byte) ([]byte, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, 5+len(h)+len(data))
	frame = append(frame, kind, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(frame[1:], uint32(len(h)))
	frame = append(frame, h...)
	frame = append(frame, data...)
	return frame, nil
}

func emitFrame(emitter frameEmitter, channel string, kind byte, header any, data []byte) {
	frame, err := encodeFrame(kind, header, data)
	if err != nil {
		return
	}
	_ = emitter.Binary(channel, frame)
}

type frameEmitter interface {
	Binary(channel string, data []byte) *sdk.PluginError
}

// ---------- registry-auth ----------

// encodeRegistryAuth 复刻分支：URL-safe base64 无 padding，键名 serveraddress 小写。
func encodeRegistryAuth(auth DockerRegistryAuth) string {
	if auth.Username == "" && auth.Password == "" && auth.ServerAddress == "" {
		return ""
	}
	payload, _ := json.Marshal(map[string]string{
		"username":      auth.Username,
		"password":      auth.Password,
		"serveraddress": auth.ServerAddress,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sync"
	"time"

	sdk "github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk"
)

const (
	pluginID      = "io.dbx.docker"
	pluginVersion = "0.1.4"
)

// session 是一条已建立的 Docker 连接（按 connectionId 缓存，disconnect 关闭）。
type session struct {
	client *dockerClient
	conn   connectionPayload
	mu     sync.Mutex // 串行化同一会话上的写操作
}

type plugin struct {
	lifecycle sync.Mutex
	mu        sync.RWMutex
	sessions  map[string]*session
	streams   *streamRegistry
	execs     *execRegistry
}

func newPlugin() *plugin {
	return &plugin{
		sessions: map[string]*session{},
		streams:  newStreamRegistry(),
		execs:    newExecRegistry(),
	}
}

func (p *plugin) Handle(_ sdk.RequestContext, method string, raw json.RawMessage, emitter *sdk.Emitter) (result any, perr *sdk.PluginError) {
	// SDK 为每条请求开一个 goroutine，但没有任何 recover：任一处 nil 解引用都会
	// 打挂整个 sidecar，用户当前的工作台随即全部失效。这里兜住单个方法内的 panic，
	// 让其退化为一条 RPC 错误，并把堆栈写到 stderr 便于定位。
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("panic in %s: %v\n%s", method, recovered, debug.Stack())
			result = nil
			perr = sdk.NewError(-32603, fmt.Sprintf("Internal plugin error in %s: %v", method, recovered))
		}
	}()

	v, decodeErr := decodeParams(raw)
	if decodeErr != nil {
		return nil, decodeErr
	}
	value, err := p.dispatch(method, v, raw, emitter)
	if err != nil {
		if err == errMethodNotFound {
			return nil, sdk.MethodNotFound(method)
		}
		return nil, serverError(err)
	}
	return value, nil
}

func (p *plugin) dispatch(method string, v params, raw json.RawMessage, emitter *sdk.Emitter) (any, error) {
	switch method {
	case "connection/test":
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		return p.testConnection(v)
	case "connection/connect":
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		return p.connect(v)
	case "connection/disconnect":
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		return p.disconnect(v)
	}

	// 业务方法按 connectionId 路由到会话；宿主（尤其开发宿主在 sidecar 重建后）
	// 可能不先发 connection/connect，与分支每请求重建连接的行为对齐：用请求
	// 信封里的 connection 配置惰性建立会话。
	sess, err := p.sessionFor(v.ConnectionID, v.Connection, v.Runtime)
	if err != nil {
		return nil, err
	}

	switch method {
	case "docker/getConnectionInfo":
		return p.getConnectionInfo(sess)
	case "docker/getEngineDetails":
		return p.getEngineDetails(sess)
	case "docker/listContainers":
		return p.listContainers(sess, raw)
	case "docker/containerAction":
		return p.containerAction(sess, raw)
	case "docker/inspectContainer":
		return p.inspectContainer(sess, raw)
	case "docker/containerStats":
		return p.containerStats(sess, raw)
	case "docker/createContainer":
		return p.createContainer(sess, raw)
	case "docker/applyCompose":
		return p.applyCompose(sess, raw)
	case "docker/removeContainer":
		return p.removeContainer(sess, raw)
	case "docker/listImages":
		return p.listImages(sess)
	case "docker/pullImage":
		return p.pullImage(sess, raw, emitter)
	case "docker/pushImage":
		return p.pushImage(sess, raw, emitter)
	case "docker/removeImage":
		return p.removeImage(sess, raw)
	case "docker/exportImage":
		return p.exportImage(sess, raw, emitter)
	case "docker/listVolumes":
		return p.listVolumes(sess)
	case "docker/createVolume":
		return p.createVolume(sess, raw)
	case "docker/listNetworks":
		return p.listNetworks(sess)
	case "docker/createNetwork":
		return p.createNetwork(sess, raw)
	case "docker/startLogs":
		return p.startLogs(sess, raw, emitter)
	case "docker/stopLogs":
		return p.stopLogs(raw)
	case "docker/startExec":
		return p.startExec(sess, raw, emitter)
	case "docker/execResize":
		return p.execResize(sess, raw)
	case "docker/stopExec":
		return p.stopExec(raw)
	case "docker/listContainerFiles":
		return p.listContainerFiles(sess, raw)
	case "docker/previewContainerFile":
		return p.previewContainerFile(sess, raw)
	case "docker/startFileDownload":
		return p.startFileDownload(sess, raw, emitter)
	case "docker/startFileUpload":
		return p.startFileUpload(sess, raw, emitter)
	case "docker/getDiskUsage":
		return p.getDiskUsage(sess)
	case "docker/prunePreview":
		return p.prunePreview(sess, raw)
	case "docker/prune":
		return p.prune(sess, raw)
	case "docker/renameContainer":
		return p.renameContainer(sess, raw)
	case "docker/tagImage":
		return p.tagImage(sess, raw)
	case "docker/untagImage":
		return p.untagImage(sess, raw)
	case "docker/imageHistory":
		return p.imageHistory(sess, raw)
	case "docker/stopStream":
		return p.stopStream(raw)
	default:
		return nil, errMethodNotFound
	}
}

func (p *plugin) sessionFor(connectionID string, conn connectionPayload, rt runtimeEndpoint) (*session, error) {
	p.mu.RLock()
	sess, ok := p.sessions[connectionID]
	p.mu.RUnlock()
	if ok {
		return sess, nil
	}
	// 惰性重连：会话缺失且请求自带连接配置时现场建立（分支行为即每请求重建）。
	if conn.ID == "" {
		return nil, fmt.Errorf("Docker connection %s is not connected; reopen the workbench", connectionID)
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.mu.RLock()
	if sess, ok := p.sessions[connectionID]; ok {
		p.mu.RUnlock()
		return sess, nil
	}
	p.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dc, err := connect(ctx, conn, rt)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.sessions[connectionID] = &session{client: dc, conn: conn}
	sess = p.sessions[connectionID]
	p.mu.Unlock()
	return sess, nil
}

func (p *plugin) getConnectionInfo(sess *session) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := fetchConnectionInfo(ctx, sess.client.cli)
	if err != nil {
		return nil, err
	}
	cfg := sess.client.cfg
	return map[string]any{
		"success":      true,
		"info":         info,
		"readOnly":     sess.conn.effectiveReadOnly(),
		"isProduction": cfg.IsProduction,
		"color":        sess.conn.Color,
		"name":         sess.conn.Name,
	}, nil
}

func (p *plugin) testConnection(v params) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dc, err := connect(ctx, v.Connection, v.Runtime)
	if err != nil {
		return nil, err
	}
	defer dc.close()
	info, err := fetchConnectionInfo(ctx, dc.cli)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success": true,
		"message": fmt.Sprintf("Docker Engine %s (API %s)", info.EngineVersion, info.APIVersion),
		"info":    info,
	}, nil
}

func (p *plugin) connect(v params) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dc, err := connect(ctx, v.Connection, v.Runtime)
	if err != nil {
		return nil, err
	}
	id := v.Connection.ID
	p.mu.Lock()
	if old, ok := p.sessions[id]; ok {
		old.client.close()
	}
	p.sessions[id] = &session{client: dc, conn: v.Connection}
	p.mu.Unlock()
	return map[string]any{"success": true}, nil
}

func (p *plugin) disconnect(v params) (any, error) {
	id := v.Connection.ID
	if id == "" {
		id = v.ConnectionID
	}
	p.mu.Lock()
	if old, ok := p.sessions[id]; ok {
		delete(p.sessions, id)
		old.client.close()
	}
	p.mu.Unlock()
	p.streams.stopByConnection(id)
	p.execs.stopByConnection(id)
	return map[string]any{"success": true}, nil
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[dbx-docker-plugin] ")
	server := sdk.NewServer(
		sdk.Metadata{
			ID:           pluginID,
			Version:      pluginVersion,
			Capabilities: []string{"connections"},
		},
		newPlugin(),
	).WithTransport(sdk.TransportFramed)
	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}

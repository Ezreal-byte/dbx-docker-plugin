// 流会话管理：日志 / pull / push / export 共用的前端侧模型。
// 对应分支的 api.dockerStartLogs / dockerPullImage(回调) / dockerPushImage / dockerStartImageExport，
// 这里改为：后端走 binary channel（docker-log / docker-transfer），前端集中订阅后按 sessionId 分发。
import { onBinary } from './bridge';
import { stopStream } from './api';
import { decodeFrame, type LogHeader, type TransferHeader } from './frames';
import type { DockerStreamEvent, DockerStreamHandle, DockerTransferProgress } from './types';

export interface ExportProgress extends DockerTransferProgress {
  dataChunks: Uint8Array[];
}

interface LogListener {
  kind: 'log';
  onEvent: (event: DockerStreamEvent) => void;
}

interface TransferListener {
  kind: 'transfer';
  onEvent: (event: { chunk: string; done: boolean; error?: string | null }) => void;
  sawDone: boolean;
}

interface ExportListener {
  kind: 'export';
  onEvent: (progress: ExportProgress) => void;
  state: ExportProgress;
}

type Listener = LogListener | TransferListener | ExportListener;

const listeners = new Map<string, Listener>();
let connectionId = '';
let installed = false;

export function initStreams(connId: string) {
  connectionId = connId;
  if (installed) return;
  installed = true;
  onBinary(({ channel, data }) => {
    if (channel === 'docker-log') handleLogFrame(data);
    else if (channel === 'docker-transfer') handleTransferFrame(data);
  });
}

function handleLogFrame(data: Uint8Array) {
  const frame = decodeFrame<LogHeader>(data);
  if (!frame) return;
  const listener = listeners.get(frame.header.sessionId);
  if (!listener || listener.kind !== 'log') return;
  const chunk = frame.kind === 1 ? new TextDecoder().decode(frame.data) : '';
  const done = frame.header.status === 'done';
  const error = frame.header.status === 'error' ? frame.header.error || 'log stream failed' : undefined;
  listener.onEvent({ sessionId: frame.header.sessionId, chunk, done: done || !!error, error: error ?? null });
  if (done || error) listeners.delete(frame.header.sessionId);
}

function handleTransferFrame(data: Uint8Array) {
  const frame = decodeFrame<TransferHeader>(data);
  if (!frame) return;
  const header = frame.header;
  const listener = listeners.get(header.sessionId);
  if (!listener) return;
  const terminal = header.status === 'done' || header.status === 'error' || header.status === 'cancelled';
  const chunk = frame.kind === 1 && header.kind !== 'export' ? new TextDecoder().decode(frame.data) : '';

  if (listener.kind === 'export') {
    const state = listener.state;
    if (frame.kind === 1 && frame.data.length) {
      state.dataChunks.push(frame.data);
      state.bytesCompleted += frame.data.length;
    }
    state.status = header.status;
    state.error = header.error ?? null;
    listener.onEvent({ ...state, dataChunks: state.dataChunks });
  } else if (listener.kind === 'transfer') {
    // 末帧（status=done/error/cancelled）可能仍携带数据；done 只在明确的 done 状态帧上报一次，
    // 避免 error/cancelled 与 done 同帧时先触发 done 回调。
    if (frame.kind === 1) {
      listener.onEvent({ chunk, done: header.status === 'done' && !listener.sawDone, error: header.status === 'error' ? header.error || 'transfer failed' : null });
      if (header.status === 'done') listener.sawDone = true;
    } else if (header.status === 'done' && !listener.sawDone) {
      listener.sawDone = true;
      listener.onEvent({ chunk: '', done: true, error: null });
    } else if (header.status === 'error' || header.status === 'cancelled') {
      listener.onEvent({ chunk: '', done: true, error: header.status === 'error' ? header.error || 'transfer failed' : null });
    }
  }
  if (terminal) listeners.delete(header.sessionId);
}

function makeHandle(sessionId: string): DockerStreamHandle {
  return {
    sessionId,
    stop: async () => {
      listeners.delete(sessionId);
      try {
        await stopStream(connectionId, sessionId);
      } catch {
        // 已结束的流在后端可能已不存在，忽略。
      }
    },
  };
}

export function startLogStream(
  containerSessionId: string,
  onEvent: (event: DockerStreamEvent) => void,
): DockerStreamHandle {
  listeners.set(containerSessionId, { kind: 'log', onEvent });
  return makeHandle(containerSessionId);
}

export function registerTransferStream(
  sessionId: string,
  onEvent: (event: { chunk: string; done: boolean; error?: string | null }) => void,
): DockerStreamHandle {
  listeners.set(sessionId, { kind: 'transfer', onEvent, sawDone: false });
  return makeHandle(sessionId);
}

export function registerExportStream(
  sessionId: string,
  image: string,
  onEvent: (progress: ExportProgress) => void,
): DockerStreamHandle {
  const state: ExportProgress = {
    sessionId,
    kind: 'export',
    direction: 'download',
    image,
    status: 'running',
    bytesCompleted: 0,
    dataChunks: [],
  };
  listeners.set(sessionId, { kind: 'export', onEvent, state });
  return makeHandle(sessionId);
}

export function unregisterStream(sessionId: string) {
  listeners.delete(sessionId);
}

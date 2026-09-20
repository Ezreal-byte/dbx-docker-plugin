// 后端 binary 帧协议与 Go streams.go 对应：1 字节 kind + 4 字节 BE 长度 + JSON 头 + 原始数据。
export interface TransferHeader {
  sessionId: string;
  kind: 'pull' | 'push' | 'export';
  direction: 'download' | 'upload';
  image: string;
  status: 'running' | 'done' | 'error' | 'cancelled';
  error?: string;
  dataLen?: number;
}

export interface LogHeader {
  sessionId: string;
  status: 'running' | 'done' | 'error';
  error?: string;
  dataLen?: number;
}

// 容器内终端（docker exec）帧头；上行只带 kind，下行带 status/exitCode。
export interface ExecHeader {
  sessionId: string;
  kind?: 'stdin';
  status?: 'running' | 'done' | 'error';
  error?: string;
  exitCode?: number;
  dataLen?: number;
}

export interface DecodedFrame<T> {
  kind: number; // 0=JSON 头 1=数据块
  header: T;
  data: Uint8Array;
}

export function decodeFrame<T>(bytes: Uint8Array): DecodedFrame<T> | null {
  if (bytes.length < 5) return null;
  const kind = bytes[0];
  const headerLen = new DataView(bytes.buffer, bytes.byteOffset + 1, 4).getUint32(0);
  if ((kind !== 0 && kind !== 1) || headerLen > bytes.length - 5) return null;
  const headerBytes = bytes.slice(5, 5 + headerLen);
  const data = bytes.slice(5 + headerLen);
  try {
    const header = JSON.parse(new TextDecoder().decode(headerBytes)) as T;
    return { kind, header, data };
  } catch {
    return null;
  }
}

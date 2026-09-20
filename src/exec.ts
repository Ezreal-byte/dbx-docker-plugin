// 容器内终端 / 上传的二进制上行通道。
// 帧格式与后端 streams.go encodeFrame / exec.go decodeExecInput 一致：
// 1 字节 kind(=1 数据) + 4 字节 BE 头长度 + JSON 头 + 原始数据。
import { getPlugin } from './bridge';

const encoder = new TextEncoder();

/** 单帧上限：宿主 binary payload 上限 8 MiB，base64 会再膨胀 4/3，这里留足余量。 */
export const UPLOAD_CHUNK_BYTES = 512 * 1024;

// 宿主 sendBinary 会把 Uint8Array 的底层 buffer 整体转移，
// 因此这里显式构造「恰好等于帧长」的 buffer，避免带上多余字节。
export function encodeExecInputFrame(sessionId: string, payload: string | Uint8Array): Uint8Array {
  const headerBytes = encoder.encode(JSON.stringify({ sessionId, kind: 'stdin' }));
  const dataBytes = typeof payload === 'string' ? encoder.encode(payload) : payload;
  const frame = new Uint8Array(5 + headerBytes.length + dataBytes.length);
  frame[0] = 1;
  new DataView(frame.buffer).setUint32(1, headerBytes.length);
  frame.set(headerBytes, 5);
  frame.set(dataBytes, 5 + headerBytes.length);
  return frame;
}

function toBase64(bytes: Uint8Array): string {
  let binary = '';
  // 一次性展开大数组会超出参数长度上限，按块拼接。
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

async function sendFrame(sessionId: string, payload: string | Uint8Array): Promise<void> {
  const plugin = getPlugin();
  if (!plugin.sendBinary) throw new Error('Host API sendBinary is unavailable; the plugin needs host.binary.');
  await plugin.sendBinary('docker-exec', toBase64(encodeExecInputFrame(sessionId, payload)));
}

/** 把键盘输入写入容器内终端的 stdin。 */
export async function sendExecInput(sessionId: string, payload: string): Promise<void> {
  if (!payload) return;
  await sendFrame(sessionId, payload);
}

/** 上传容器文件：按块写入 exec 的 stdin，并回报已发送字节数。 */
export async function sendExecBytes(
  sessionId: string,
  bytes: Uint8Array,
  onProgress?: (sent: number, total: number) => void,
): Promise<void> {
  const total = bytes.length;
  for (let offset = 0; offset < total; offset += UPLOAD_CHUNK_BYTES) {
    const end = Math.min(offset + UPLOAD_CHUNK_BYTES, total);
    await sendFrame(sessionId, bytes.subarray(offset, end));
    onProgress?.(end, total);
  }
}

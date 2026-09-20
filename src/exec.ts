// 容器内终端的二进制上行通道。
// 帧格式与后端 streams.go encodeFrame / exec.go decodeExecInput 一致：
// 1 字节 kind(=1 数据) + 4 字节 BE 头长度 + JSON 头 + 原始数据。
import { getPlugin } from './bridge';

const encoder = new TextEncoder();

// 宿主 sendBinary 会把 Uint8Array 的底层 buffer 整体转移，
// 因此这里显式构造「恰好等于帧长」的 buffer，避免带上多余字节。
export function encodeExecInputFrame(sessionId: string, payload: string): Uint8Array {
  const headerBytes = encoder.encode(JSON.stringify({ sessionId, kind: 'stdin' }));
  const dataBytes = encoder.encode(payload);
  const frame = new Uint8Array(5 + headerBytes.length + dataBytes.length);
  frame[0] = 1;
  new DataView(frame.buffer).setUint32(1, headerBytes.length);
  frame.set(headerBytes, 5);
  frame.set(dataBytes, 5 + headerBytes.length);
  return frame;
}

function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

/** 把键盘输入写入容器内终端的 stdin。 */
export async function sendExecInput(sessionId: string, payload: string): Promise<void> {
  if (!payload) return;
  const plugin = getPlugin();
  if (!plugin.sendBinary) throw new Error('Host API sendBinary is unavailable; the plugin needs host.binary.');
  await plugin.sendBinary('docker-exec', toBase64(encodeExecInputFrame(sessionId, payload)));
}

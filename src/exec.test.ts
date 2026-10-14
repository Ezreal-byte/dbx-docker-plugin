import { describe, expect, it, vi } from 'vitest';
import { encodeExecInputFrame, sendExecBytes, UPLOAD_CHUNK_BYTES } from './exec';
import { decodeFrame, type ExecHeader } from './frames';

describe('container terminal input frames', () => {
  it('stops an upload between chunks when cancelled', async () => {
    const abort = new AbortController();
    const sendBinary = vi.fn(async () => { abort.abort(); });
    vi.stubGlobal('window', { dbxPlugin: { sendBinary } });
    try {
      await expect(sendExecBytes('upload-cancel', new Uint8Array(UPLOAD_CHUNK_BYTES * 2), undefined, abort.signal)).rejects.toMatchObject({ name: 'AbortError' });
      expect(sendBinary).toHaveBeenCalledTimes(1);
    } finally { vi.unstubAllGlobals(); }
  });
  it('round-trips through the same layout the Go sidecar decodes', () => {
    const frame = encodeExecInputFrame('exec-1', 'echo hi\n');
    // 后端 decodeExecInput 读取 [kind][u32 头长度][JSON 头][数据]。
    expect(frame[0]).toBe(1);
    const headerLength = new DataView(frame.buffer, frame.byteOffset + 1, 4).getUint32(0);
    const header = JSON.parse(new TextDecoder().decode(frame.subarray(5, 5 + headerLength))) as ExecHeader;
    expect(header).toEqual({ sessionId: 'exec-1', kind: 'stdin' });
    expect(new TextDecoder().decode(frame.subarray(5 + headerLength))).toBe('echo hi\n');
  });

  it('produces a frame whose buffer is exactly the frame length', () => {
    const frame = encodeExecInputFrame('exec-2', 'x');
    // 宿主 sendBinary 会转移 Uint8Array 的整个底层 buffer，多出的字节会污染协议。
    expect(frame.byteLength).toBe(frame.buffer.byteLength);
  });

  it('stays decodable by the shared frame decoder', () => {
    const frame = encodeExecInputFrame('exec-3', 'ls\r');
    const decoded = decodeFrame<ExecHeader>(frame);
    expect(decoded?.header.sessionId).toBe('exec-3');
    expect(new TextDecoder().decode(decoded?.data)).toBe('ls\r');
  });

  it('keeps multi-byte characters intact', () => {
    const frame = encodeExecInputFrame('exec-4', 'echo 中文');
    const payload = frame.subarray(frame.length - new TextEncoder().encode('echo 中文').length);
    expect(new TextDecoder().decode(payload)).toBe('echo 中文');
  });
});

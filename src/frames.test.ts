import { describe, expect, it } from 'vitest';
import { decodeFrame, type TransferHeader } from './frames';

describe('Docker binary frames', () => {
  it('keeps export bytes intact after the JSON header', () => {
    const header = new TextEncoder().encode(JSON.stringify({ sessionId: 's1', kind: 'export', direction: 'download', image: 'nginx', status: 'running' }));
    const payload = Uint8Array.of(0, 255, 10, 128);
    const frame = new Uint8Array(5 + header.length + payload.length);
    frame[0] = 1;
    new DataView(frame.buffer).setUint32(1, header.length);
    frame.set(header, 5);
    frame.set(payload, 5 + header.length);

    const decoded = decodeFrame<TransferHeader>(frame);
    expect(decoded?.header.sessionId).toBe('s1');
    expect(decoded?.data).toEqual(payload);
  });

  it('rejects truncated or invalid headers', () => {
    expect(decodeFrame(Uint8Array.of(1, 0, 0, 1, 0, 123))).toBeNull();
    expect(decodeFrame(Uint8Array.of(3, 0, 0, 0, 2, 123, 125))).toBeNull();
  });
});

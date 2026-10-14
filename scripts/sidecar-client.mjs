import { spawn } from 'node:child_process';
export class Sidecar {
  constructor(executable, dataDir) {
    this.seq = 0; this.pending = new Map(); this.buffer = Buffer.alloc(0);
    this.child = spawn(executable, [], { env: { ...process.env, DBX_PLUGIN_DATA_DIR: dataDir }, stdio: ['pipe', 'pipe', 'pipe'] });
    this.child.stderr.on('data', chunk => process.stderr.write(chunk));
    this.child.stdout.on('data', chunk => {
      this.buffer = Buffer.concat([this.buffer, chunk]);
      while (this.buffer.length >= 5) {
        const size = this.buffer.readUInt32BE(1); if (this.buffer.length < 5 + size) return;
        const kind = this.buffer[0], body = this.buffer.subarray(5, 5 + size); this.buffer = this.buffer.subarray(5 + size);
        if (kind !== 0) continue;
        const msg = JSON.parse(body); const pending = this.pending.get(msg.id); if (!pending) continue;
        this.pending.delete(msg.id); clearTimeout(pending.timer); msg.error ? pending.reject(Error(msg.error.message)) : pending.resolve(msg.result);
      }
    });
  }
  request(method, params = {}) {
    const id = ++this.seq, payload = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
    const header = Buffer.alloc(5); header.writeUInt32BE(payload.length, 1);
    const promise = new Promise((resolve, reject) => { const timer = setTimeout(() => { this.pending.delete(id); reject(Error(`${method} timed out`)); }, 120000); this.pending.set(id, { resolve, reject, timer }); });
    this.child.stdin.write(Buffer.concat([header, payload])); return promise;
  }
  async close() { for (const p of this.pending.values()) clearTimeout(p.timer); if (this.child.exitCode === null) { const exited = new Promise(resolve => this.child.once('exit', resolve)); this.child.kill(); await exited; } }
}

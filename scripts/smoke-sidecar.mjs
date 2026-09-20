import { spawn } from 'node:child_process';

const executable = process.argv[2];
if (!executable) throw new Error('Usage: node scripts/smoke-sidecar.mjs <sidecar executable>');

const child = spawn(executable, [], { stdio: ['pipe', 'pipe', 'pipe'] });
const request = Buffer.from(JSON.stringify({
  jsonrpc: '2.0', id: 1, method: 'plugin/initialize',
  params: { host: { protocolVersions: [1] } },
}));
const frame = Buffer.alloc(5 + request.length);
frame[0] = 0;
frame.writeUInt32BE(request.length, 1);
request.copy(frame, 5);

let pending = Buffer.alloc(0);
let stderr = '';
child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
const response = new Promise((resolve, reject) => {
  const timeout = setTimeout(() => { child.kill(); reject(new Error('Sidecar handshake timed out')); }, 10_000);
  child.on('error', (error) => { clearTimeout(timeout); reject(error); });
  child.on('exit', (code) => {
    if (pending.length < 5) { clearTimeout(timeout); reject(new Error(`Sidecar exited ${code}: ${stderr}`)); }
  });
  child.stdout.on('data', (chunk) => {
    pending = Buffer.concat([pending, chunk]);
    if (pending.length < 5) return;
    const length = pending.readUInt32BE(1);
    if (pending.length < 5 + length) return;
    clearTimeout(timeout);
    if (pending[0] !== 0) reject(new Error('Expected a JSON protocol frame'));
    else resolve(JSON.parse(pending.subarray(5, 5 + length).toString('utf8')));
  });
});

child.stdin.end(frame);
const result = await response;
if (result.error || result.result?.protocolVersion !== 1 || result.result?.plugin?.id !== 'io.dbx.docker' || result.result?.plugin?.version !== '0.1.1') {
  throw new Error(`Unexpected sidecar handshake: ${JSON.stringify(result)}`);
}
console.log('Sidecar handshake passed');

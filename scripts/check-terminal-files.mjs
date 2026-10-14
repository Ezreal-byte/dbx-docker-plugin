// Focused regression against disposable containers; never touches existing ones.
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { Sidecar } from './sidecar-client.mjs';

const name = 'dbx-terminal-' + randomUUID().slice(0, 8);
const dataDir = await mkdtemp(join(tmpdir(), 'dbx-terminal-check-'));
const sidecar = new Sidecar(resolve(process.argv[2] || 'dist/verify-sidecar.exe'), dataDir);
const connection = { id: name, plugin_id: 'io.dbx.docker', host: '127.0.0.1', port: 2375, external_config: { protocol: 'http', api_version: 'auto' } };
const rpc = (method, args = {}) => sidecar.request(method, { connectionId: name, ...args });
const sessions = new Map(), containers = [];
let buffer = Buffer.alloc(0);
sidecar.child.stdout.on('data', chunk => {
  buffer = Buffer.concat([buffer, chunk]);
  while (buffer.length >= 5) {
    const n = buffer.readUInt32BE(1);
    if (buffer.length < n + 5) return;
    const kind = buffer[0], body = buffer.subarray(5, n + 5);
    buffer = buffer.subarray(n + 5);
    if (kind !== 1) continue;
    const channelLength = body.readUInt16BE(0);
    if (body.subarray(2, 2 + channelLength).toString() !== 'docker-exec') continue;
    const data = body.subarray(2 + channelLength), hlen = data.readUInt32BE(1);
    const header = JSON.parse(data.subarray(5, 5 + hlen).toString()), session = sessions.get(header.sessionId);
    if (!session) continue;
    if (data[0] === 1) session.output += data.subarray(5 + hlen).toString();
    if (header.status === 'done' || header.status === 'error') session.done = true;
  }
});
function sendInput(sessionId, input) {
  const h = Buffer.from(JSON.stringify({ sessionId, kind: 'stdin' })), data = Buffer.from(input);
  const inner = Buffer.alloc(5); inner[0] = 1; inner.writeUInt32BE(h.length, 1);
  const channel = Buffer.from('docker-exec'), channelHeader = Buffer.alloc(2); channelHeader.writeUInt16BE(channel.length);
  const body = Buffer.concat([channelHeader, channel, inner, h, data]), outer = Buffer.alloc(5);
  outer[0] = 1; outer.writeUInt32BE(body.length, 1);
  sidecar.child.stdin.write(Buffer.concat([outer, body]));
}
async function waitUntil(predicate, description) {
  const end = Date.now() + 10000;
  while (!predicate() && Date.now() < end) await new Promise(r => setTimeout(r, 50));
  assert(predicate(), typeof description === 'function' ? description() : description);
}
try {
  await rpc('connection/connect', { connection });
  for (const [index, image] of (process.argv.slice(3).length ? process.argv.slice(3) : ['alpine:latest', 'mysql:8.0.44']).entries()) {
    const { id: containerId } = await rpc('docker/createContainer', { request: { name: `${name}-${index}`, image, command: ['sleep', '180'], start: true } });
    containers.push(containerId);
    const op = (operation, path, newName) => rpc('docker/fileOperation', { containerId, operation, path, name: newName });
    await op('mkdir', '/home/dbx folder');
    await op('create', '/home/dbx folder/original.yml');
    await op('rename', '/home/dbx folder/original.yml', 'renamed.yml');
    const entries = await rpc('docker/listContainerFiles', { containerId, path: '/home/dbx folder' });
    assert.deepEqual(entries.map(e => e.name), ['renamed.yml']);
    await assert.rejects(op('mkdir', '/home/dbx folder'), /exists/i);
    for (const shell of image.startsWith('mysql:') ? ['/bin/sh', '/bin/bash'] : ['/bin/sh', '/bin/ash']) {
      const sessionId = randomUUID(), session = { output: '', done: false }; sessions.set(sessionId, session);
      await rpc('docker/startExec', { containerId, sessionId, command: [shell], colorOutput: true, cols: 100, rows: 30 });
      await waitUntil(() => /\x1b\[32m/.test(session.output), `${image} ${shell}: no colored prompt: ${session.output}`);
      session.output = '';
      sendInput(sessionId, 'ls -d /home\r');
      await waitUntil(() => /\x1b\[(?:0?1;)?34m[^\r\n]*\/home/.test(session.output), () => `${image} ${shell}: no colored directory output: ${JSON.stringify(session.output)}`);
      sendInput(sessionId, 'exit\r');
      await waitUntil(() => session.done, `${image} ${shell}: did not exit`);
      console.log(`PASS ${image} ${shell}: colored prompt and ls, interactive input, clean exit`);
    }
    // Explicit commands must not be turned into interactive shells.
    const sessionId = randomUUID(), session = { output: '', done: false }; sessions.set(sessionId, session);
    await rpc('docker/startExec', { containerId, sessionId, command: ['/bin/sh', '-c', "printf 'plain-output'"], colorOutput: true });
    await waitUntil(() => session.done, 'explicit command did not finish');
    assert.equal(session.output, 'plain-output');
    console.log(`PASS ${image}: /home mkdir/create/rename, visible collision error, explicit command preserved`);
  }
} finally {
  try {
    for (const containerId of containers) {
      // Only the IDs created above; -v also removes MySQL's anonymous image volume.
      execFileSync('docker', ['--host', 'tcp://127.0.0.1:2375', 'rm', '-f', '-v', containerId], { stdio: 'pipe' });
    }
  } finally {
    await sidecar.close();
    if (!resolve(dataDir).startsWith(resolve(tmpdir()) + sep + 'dbx-terminal-check-')) throw Error('Unexpected temp path');
    await rm(dataDir, { recursive: true, force: true });
  }
}

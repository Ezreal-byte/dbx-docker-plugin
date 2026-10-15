// Writes only inside one disposable container. No daemon-wide cleanup.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { Sidecar } from './sidecar-client.mjs';

const name = 'dbx-safety-' + randomUUID().slice(0, 8);
const dataDir = await mkdtemp(join(tmpdir(), 'dbx-safety-check-'));
const client = new Sidecar(resolve(process.argv[2]), dataDir);
const windows = process.platform === 'win32';
const docker = (...args) => execFileSync('docker', [...(windows ? ['--host', 'tcp://127.0.0.1:2375'] : []), ...args], { encoding: 'utf8' });
const connection = { id: name, plugin_id: 'io.dbx.docker', host: '127.0.0.1', port: 2375, external_config: windows ? { protocol: 'http', api_version: 'auto' } : { protocol: 'local', api_version: 'auto' } };
const rpc = (method, args = {}) => client.request(method, { connectionId: name, ...args });
const preview = (operation, args) => rpc('docker/mutation', { operation, arguments: args });
const execute = (operation, args, token) => preview(operation, { ...args, previewToken: token });
const streams = new Map();
let containerId;
client.onBinary((channel, data) => {
  if (channel !== 'docker-exec') return;
  const n = data.readUInt32BE(1), header = JSON.parse(data.subarray(5, 5 + n).toString());
  const stream = streams.get(header.sessionId); if (!stream) return;
  if (data[0] === 1) stream.output += data.subarray(5 + n).toString();
  if (header.status === 'done' || header.status === 'error') { stream.done = true; stream.exitCode = header.exitCode; stream.error = header.error; }
});
function send(sessionId, content) {
  const h = Buffer.from(JSON.stringify({ sessionId, kind: 'stdin' })), prefix = Buffer.alloc(5);
  prefix[0] = 1; prefix.writeUInt32BE(h.length, 1);
  client.sendBinary('docker-exec', Buffer.concat([prefix, h, Buffer.from(content)]));
}
async function waitFor(predicate, message) {
  const until = Date.now() + 10000;
  while (!predicate() && Date.now() < until) await new Promise(r => setTimeout(r, 100));
  assert(predicate(), message);
}
const target = '/home/dbx-safety/config.yml';
const uploadArgs = (path, size) => ({ containerId, path, size, mode: 'overwrite' });
async function startUpload(args, token) {
  const sessionId = randomUUID(), stream = { done: false, output: '' }; streams.set(sessionId, stream);
  await rpc('docker/startFileUpload', { ...args, sessionId, previewToken: token });
  return { sessionId, stream };
}
try {
  await rpc('connection/connect', { connection });
  ({ id: containerId } = await rpc('docker/createContainer', { request: { name, image: process.argv[3] || 'alpine:latest', command: ['sleep', '180'], start: true } }));
  docker('exec', containerId, '/bin/sh', '-c', 'mkdir /home/dbx-safety; printf original > /home/dbx-safety/config.yml; ln -s config.yml /home/dbx-safety/link');
  const read = () => docker('exec', containerId, 'cat', target);
  const args = uploadArgs(target, 8);
  await assert.rejects(rpc('docker/startFileUpload', { ...args, sessionId: randomUUID() }), /preview/i);
  assert.equal(read(), 'original');
  await assert.rejects(preview('upload_file', uploadArgs('/home/dbx-safety/link', 8)), /symlink/i);
  const bound = await preview('upload_file', args);
  assert.equal(bound.preview.exists, true); assert.equal(read(), 'original');
  const other = { ...connection, id: name + '-other' };
  await rpc('connection/connect', { connectionId: other.id, connection: other });
  await assert.rejects(rpc('docker/startFileUpload', { ...args, connectionId: other.id, sessionId: randomUUID(), previewToken: bound.previewToken }), /connection|token/i);
  const active = await startUpload(args, bound.previewToken);
  send(active.sessionId, 'replaced');
  await waitFor(() => active.stream.done, 'upload did not finish');
  assert.equal(active.stream.exitCode ?? 0, 0, active.stream.error || active.stream.output); assert.equal(read(), 'replaced');
  await assert.rejects(startUpload(args, bound.previewToken), /token/i);
  console.log('PASS: upload requires consent, rejects symlinks/cross-connection/replay, completes exact replacement');

  const stale = await preview('upload_file', args);
  docker('exec', containerId, '/bin/sh', '-c', 'printf changed > "$1"', 'dbx', target);
  await assert.rejects(startUpload(args, stale.previewToken), /changed/i);
  assert.equal(read(), 'changed');
  const modified = await preview('upload_file', args);
  const midway = await startUpload(args, modified.previewToken);
  send(midway.sessionId, 'half');
  docker('exec', containerId, '/bin/sh', '-c', 'printf newer > "$1"', 'dbx', target);
  send(midway.sessionId, 'done');
  await waitFor(() => midway.stream.done, 'changed-target upload did not finish');
  assert.notEqual(midway.stream.exitCode ?? 0, 0); assert.equal(read(), 'newer');
  const cancelPreview = await preview('upload_file', args);
  const cancelled = await startUpload(args, cancelPreview.previewToken);
  send(cancelled.sessionId, 'part');
  await rpc('docker/stopExec', { sessionId: cancelled.sessionId });
  await waitFor(() => cancelled.stream.done, 'cancelled upload did not finish');
  assert.equal(read(), 'newer');
  await waitFor(() => docker('exec', containerId, '/bin/sh', '-c', 'find /home/dbx-safety -name ".dbx-upload.*"').trim() === '', 'cancelled upload leaked temporary data');
  console.log('PASS: changed target and interrupted upload preserve the original; temporary files cleaned');

  const fresh = uploadArgs('/home/dbx-safety/new.conf', 0), freshPreview = await preview('upload_file', fresh);
  assert.equal(freshPreview.preview.exists, false);
  const empty = await startUpload(fresh, freshPreview.previewToken);
  await waitFor(() => empty.stream.done, 'zero-byte upload did not finish');
  assert.equal(empty.stream.exitCode ?? 0, 0, empty.stream.error || empty.stream.output);
  const removeArgs = { containerId, path: target, operation: 'delete' };
  await assert.rejects(rpc('docker/fileOperation', removeArgs), /confirmed preview/i);
  const deletion = await preview('delete_file', removeArgs); assert.equal(read(), 'newer');
  await execute('delete_file', removeArgs, deletion.previewToken);
  await assert.rejects(execute('delete_file', removeArgs, deletion.previewToken), /token/i);
  const entries = await rpc('docker/listContainerFiles', { containerId, path: '/home/dbx-safety' });
  assert(!entries.some(entry => entry.name === 'config.yml'));
  console.log('PASS: empty-file upload and bound file deletion; preview alone never deletes');
} finally {
  try { if (containerId) docker('rm', '-f', '-v', containerId); }
  finally {
    await client.close();
    if (!resolve(dataDir).startsWith(resolve(tmpdir()) + sep + 'dbx-safety-check-')) throw Error('Unexpected temp path');
    await rm(dataDir, { recursive: true, force: true });
  }
}

// Live Docker validation. Only creates/removes resources with this run's unique
// prefix. Never executes daemon-wide prune and never edits installed DBX settings.
// Usage: node scripts/check-live-automation.mjs <sidecar.exe> [--compat]
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { Sidecar } from './sidecar-client.mjs';

const prefix = 'dbx-v020-' + randomUUID().slice(0, 8);
const executable = resolve(process.argv[2] || 'dist/verify-sidecar.exe');
const dataDir = await mkdtemp(join(tmpdir(), 'dbx-docker-live-'));
const desktop = new Sidecar(executable, dataDir), external = new Sidecar(executable, dataDir);
const connection = { id: prefix, plugin_id: 'io.dbx.docker', name: prefix, host: '127.0.0.1', port: 2375, external_config: { protocol: 'http', api_version: 'auto' } };
const rpc = (method, params = {}) => desktop.request(method, { connectionId: connection.id, ...params });
const call = (tool, args = {}, conn = connection) => external.request('mcp/call', { tool, arguments: args, lifecycle: { connection: conn, connectionId: conn.id } });
const result = r => { assert.equal(r.isError, false, JSON.stringify(r)); return JSON.parse(r.content[0].text); };
const resources = { containers: [], networks: [], volumes: [] };
async function remove(tool, args) { const preview = result(await call(tool, args)); const removed = result(await call(tool, { ...args, previewToken: preview.previewToken })); assert.equal((await call(tool, { ...args, previewToken: preview.previewToken })).isError, true); return removed; }
async function waitTask(taskId) {
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    const task = result(await call('task_status', { taskId }));
    if (task.status !== 'running') { assert.equal(task.status, 'completed', JSON.stringify(task)); return task; }
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  await call('cancel_task', { taskId }); throw Error('Live task timeout; cancellation requested');
}
try {
  for (const client of [desktop, external]) await client.request('plugin/initialize', { host: { protocolVersions: [1] } });
  await rpc('connection/connect', { connection });
  assert.equal((await call('list_containers')).isError, true);
  let settings = await rpc('mcp/settings/set', { enabled: true, revision: 0, allowedTools: [] });
  settings = await rpc('mcp/settings/set', { enabled: true, revision: settings.policy.revision, allowedTools: settings.tools.map(t => t.name) });
  const info = result(await call('engine_info')); console.log('Engine:', info.summary.engineVersion, 'API:', info.summary.apiVersion);
  const network = result(await call('create_network', { request: { name: prefix + '-net', driver: 'bridge' } })); resources.networks.push(network.id);
  result(await call('create_volume', { request: { name: prefix + '-data' } })); resources.volumes.push(prefix + '-data');
  const created = result(await call('create_container', { request: { name: prefix + '-app', image: 'alpine:latest', command: ['/bin/sh', '-c', 'echo dbx-v020-live-ok; sleep 300'], environment: ['DBX_FIXTURE_SECRET=must-not-appear-in-ai-inspect'], network: prefix + '-net', mounts: [{ type: 'volume', source: prefix + '-data', target: '/data' }], start: true } })); resources.containers.push(created.id);
  const inspect = result(await call('inspect_container', { containerId: created.id })); assert(!JSON.stringify(inspect).includes('must-not-appear'));
  assert.equal(result(await call('container_health', { containerId: created.id })).state, 'running');
  assert(result(await call('container_logs', { containerId: created.id, tail: 5 })).text.includes('dbx-v020-live-ok'));
  assert.equal(result(await call('inspect_volume', { name: prefix + '-data' })).containers[0].id, created.id);
  assert(result(await call('inspect_network', { networkId: network.id })).Containers[created.id]);
  const attachedPreview = result(await call('remove_volume', { name: prefix + '-data' }));
  assert.equal((await call('remove_volume', { name: prefix + '-data', previewToken: attachedPreview.previewToken })).isError, true);
  assert.equal((await call('container_action', { containerId: created.id, action: 'stop' }, { ...connection, read_only: true })).isError, true);
  result(await call('disconnect_network', { networkId: network.id, containerId: created.id }));
  result(await call('connect_network', { networkId: network.id, containerId: created.id }));
  result(await call('inspect_image', { imageId: 'alpine:latest' }));
  assert(Array.isArray(result(await call('image_history', { imageId: 'alpine:latest' }))));
  assert(result(await call('container_stats', { containerIds: [created.id] }))[0].containerId === created.id);
  console.log('PASS: TCP, create/start, health/logs/stats, image details/history, volume references, network connect/disconnect, read-only and in-use-volume rejection.');
  if (process.platform === 'win32') {
    const pipe = { ...connection, id: prefix + '-pipe', external_config: { protocol: 'npipe', pipe_path: '//./pipe/docker_engine', api_version: 'auto' } };
    await rpc('connection/connect', { connection: pipe, connectionId: pipe.id });
    const queries = await rpc('mcp/settings/set', { connectionId: pipe.id, enabled: true, revision: 0 });
    assert(queries.policy.enabled);
    assert.equal(result(await call('container_health', { containerId: created.id }, pipe)).state, 'running');
    console.log('PASS: real Windows Named Pipe engine connection and cold MCP query.');
  }
  if (process.argv.includes('--compat')) {
    const { stdout } = await promisify(execFile)(process.execPath, ['scripts/smoke-sidecar.mjs', executable, 'http://127.0.0.1:2375', prefix + '-app'], { cwd: resolve('.'), env: { ...process.env, SMOKE_ALLOW_PRUNE: '0' }, timeout: 120000, maxBuffer: 1024 * 1024 });
    console.log(stdout);
  }
  const beforeStop = result(await call('remove_container', { containerId: created.id }));
  result(await call('container_action', { containerId: created.id, action: 'stop' }));
  assert.equal((await call('remove_container', { containerId: created.id, previewToken: beforeStop.previewToken })).isError, true);
  await remove('remove_container', { containerId: created.id }); resources.containers.splice(resources.containers.indexOf(created.id), 1);
  await remove('remove_network', { networkId: network.id }); resources.networks.splice(resources.networks.indexOf(network.id), 1);
  await remove('remove_volume', { name: prefix + '-data' }); resources.volumes.splice(resources.volumes.indexOf(prefix + '-data'), 1);
  console.log('PASS: changed-state preview rejection, real container/network/volume deletion and token replay rejection.');
  const composeRequest = { projectName: prefix + '-compose', content: 'services:\n  app:\n    image: alpine:latest\n    command: ["/bin/sh", "-c", "sleep 300"]\n', replaceExisting: false };
  // Register expected names before deployment so a failed task can be cleaned.
  resources.networks.push(prefix + '-compose_default'); resources.containers.push(prefix + '-compose-app-1');
  const preview = result(await call('apply_compose', { request: composeRequest }));
  const task = result(await call('apply_compose', { request: composeRequest, previewToken: preview.previewToken }));
  const completed = await waitTask(task.taskId); assert.equal(completed.result.containerIds.length, 1);
  console.log('PASS: Compose preview, background task and persisted task result.');
  const revoke = await rpc('mcp/settings/set', { enabled: false, revision: settings.policy.revision, allowedTools: [] });
  assert.equal((await call('list_containers')).isError, true);
  const enabled = await rpc('mcp/settings/set', { enabled: true, revision: revoke.policy.revision });
  await rpc('mcp/settings/set', { enabled: true, revision: enabled.policy.revision, allowedTools: settings.tools.map(t => t.name) });
  console.log('PASS: permission revocation observed by external sidecar.');
} finally {
  let failed = false;
  // Cleanup by immutable IDs / names tracked during this run, using only UI
  // business methods so revocation does not prevent cleanup of our fixtures.
  for (const id of resources.containers.reverse()) {
    try { await rpc('docker/containerAction', { containerId: id, action: 'stop' }); await rpc('docker/removeContainer', { containerId: id }); }
    catch (e) { if (!/No such container|not found/i.test(e.message)) { failed = true; console.error('Cleanup container:', id, e.message); } }
  }
  for (const id of resources.networks.reverse()) {
    try { await rpc('docker/removeNetwork', { networkId: id }); }
    catch (e) { if (!/not found|No such network/i.test(e.message)) { failed = true; console.error('Cleanup network:', id, e.message); } }
  }
  for (const name of resources.volumes) {
    try { await rpc('docker/removeVolume', { name }); }
    catch (e) { failed = true; console.error('Cleanup volume:', name, e.message); }
  }
  await desktop.close(); await external.close();
  if (!resolve(dataDir).startsWith(resolve(tmpdir()) + sep + 'dbx-docker-live-')) throw Error('Unexpected fixture directory');
  await rm(dataDir, { recursive: true, force: true });
  if (failed) throw Error(`Cleanup incomplete for ${prefix}; inspect the listed resources`);
  console.log('Cleaned test resources:', prefix);
}

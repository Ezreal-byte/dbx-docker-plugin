// Isolated framed-protocol integration test. No real Docker daemon or saved DBX
// connection is used. --ui serves the packaged workbench against these fixtures.
import assert from 'node:assert/strict';
import { Sidecar } from './sidecar-client.mjs';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve, sep } from 'node:path';

const executable = resolve(process.argv[2] || 'dist/verify-sidecar.exe');
const dataDir = await mkdtemp(join(tmpdir(), 'dbx-docker-automation-'));
const version = JSON.parse(await readFile(new URL('../manifest.json', import.meta.url), 'utf8')).version;

const containers = new Map(), networks = new Map(), volumes = new Map();
let serial = 0;
const readJSON = async req => { let data = ''; for await (const chunk of req) data += chunk; return data ? JSON.parse(data) : {}; };
const reply = (res, value, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json', 'API-Version': '1.47' }); res.end(typeof value === 'string' ? value : JSON.stringify(value)); };
const engine = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost'), path = url.pathname.replace(/^\/v[\d.]+/, ''), body = await readJSON(req);
  if (path === '/_ping') return reply(res, 'OK');
  if (path === '/version') return reply(res, { Version: 'fixture-27.5', ApiVersion: '1.47', MinAPIVersion: '1.24', Os: 'linux', Arch: 'amd64' });
  if (path === '/info') return reply(res, { Name: 'Isolated fixture', Containers: containers.size, Images: 1 });
  if (path === '/system/df') return reply(res, { Volumes: [...volumes.values()].map(v => ({ ...v, UsageData: { Size: 4096, RefCount: 1 } })) });
  if (path === '/containers/json') return reply(res, [...containers.values()].map(c => ({ Id: c.Id, Names: [c.Name], Image: c.Config.Image, ImageID: c.Image, State: c.State.Status, Status: c.State.Status, Mounts: c.Mounts, Labels: c.Config.Labels || {}, NetworkSettings: c.NetworkSettings })));
  if (path === '/containers/create') {
    const Id = `container-${++serial}`, c = { Id, Name: '/' + url.searchParams.get('name'), Image: 'sha256:fixture', Config: body, State: { Status: 'created', ExitCode: 0, StartedAt: '2026-01-01T00:00:00Z', FinishedAt: '2026-01-01T00:00:00Z' }, Mounts: (body.HostConfig?.Mounts || []).map(m => ({ Name: m.Source, Destination: m.Target, Type: m.Type, RW: !m.ReadOnly })), NetworkSettings: { Networks: {} }, RestartCount: 0 };
    containers.set(Id, c); return reply(res, { Id, Warnings: [] }, 201);
  }
  if (path.startsWith('/containers/')) {
    const [, , id, action] = path.split('/'), c = containers.get(id);
    if (!c) return reply(res, { message: 'Container not found' }, 404);
    if (req.method === 'DELETE') { containers.delete(id); return reply(res, '', 204); }
    if (action === 'json') return reply(res, c);
    if (action === 'logs') return reply(res, '2026-01-01T00:00:00Z fixture log\n');
    if (action === 'start' || action === 'stop') { c.State.Status = action === 'start' ? 'running' : 'exited'; return reply(res, '', 204); }
  }
  if (path === '/networks/create') { const Id = `network-${++serial}`; networks.set(Id, { Id, Name: body.Name, Driver: body.Driver, Scope: 'local', IPAM: body.IPAM || {}, Containers: {} }); return reply(res, { Id, Warning: '' }, 201); }
  if (path === '/networks') return reply(res, [...networks.values()]);
  if (path.startsWith('/networks/')) {
    const [, , id, action] = path.split('/'), n = networks.get(id); if (!n) return reply(res, { message: 'Network not found' }, 404);
    if (action === 'connect') { n.Containers[body.Container] = { Name: containers.get(body.Container)?.Name, IPv4Address: '172.25.0.2/16' }; return reply(res, ''); }
    if (action === 'disconnect') { delete n.Containers[body.Container]; return reply(res, ''); }
    if (req.method === 'DELETE') { networks.delete(id); return reply(res, '', 204); } return reply(res, n);
  }
  if (path === '/volumes/create') { const v = { Name: body.Name, Driver: body.Driver, Scope: 'local', Mountpoint: '/fixture/' + body.Name, CreatedAt: '2026-01-01T00:00:00Z', Labels: body.Labels }; volumes.set(v.Name, v); return reply(res, v, 201); }
  if (path === '/volumes') return reply(res, { Volumes: [...volumes.values()], Warnings: [] });
  if (path.startsWith('/volumes/')) { const name = path.split('/')[2]; if (req.method === 'DELETE') { volumes.delete(name); return reply(res, '', 204); } return reply(res, volumes.get(name) || {}, volumes.has(name) ? 200 : 404); }
  if (path === '/images/json') return reply(res, [{ Id: 'sha256:fixture', RepoTags: ['fixture:latest'], Size: 2048, Created: 1767225600 }]);
  if (path.startsWith('/images/') && path.endsWith('/json')) return reply(res, { Id: 'sha256:fixture', RepoTags: ['fixture:latest'], RepoDigests: ['fixture@sha256:example'], Size: 2048, Os: 'linux', Architecture: 'amd64' });
  if (path === '/events') return reply(res, '');
  reply(res, { message: `Fixture does not implement ${req.method} ${path}` }, 404);
});
await new Promise(resolve => engine.listen(0, '127.0.0.1', resolve));
const connection = { id: 'isolated-fixture', plugin_id: 'io.dbx.docker', name: 'Docker 验证环境（隔离模拟）', host: '127.0.0.1', port: engine.address().port, external_config: { protocol: 'http', api_version: '1.47' } };
const desktop = new Sidecar(executable, dataDir), external = new Sidecar(executable, dataDir);
const rpc = (method, params = {}) => desktop.request(method, { connectionId: connection.id, ...params });
const mcp = (tool, args = {}, client = external) => client.request('mcp/call', { tool, arguments: args, lifecycle: { connection, connectionId: connection.id } });
const result = value => { assert.equal(value.isError, false, JSON.stringify(value)); return JSON.parse(value.content[0].text); };
let uiServer;
async function close() { await desktop.close(); await external.close(); engine.close(); uiServer?.close(); if (!resolve(dataDir).startsWith(resolve(tmpdir()) + sep + 'dbx-docker-automation-')) throw Error('Unexpected fixture directory'); await rm(dataDir, { recursive: true, force: true }); }
try {
  for (const client of [desktop, external]) assert.equal((await client.request('plugin/initialize', { host: { protocolVersions: [1] } })).plugin.version, version);
  assert.equal((await external.request('mcp/tools')).tools.length, 0);
  assert.equal((await mcp('list_containers')).isError, true);
  await rpc('connection/connect', { connection });
  let settings = await rpc('mcp/settings/set', { enabled: true, revision: 0, allowedTools: ['remove_volume'] });
  assert(settings.policy.allowedTools.includes('list_containers')); assert(!settings.policy.allowedTools.includes('remove_volume'));
  assert.equal(result(await mcp('list_containers')).total, 0);
  settings = await rpc('mcp/settings/set', { enabled: true, revision: settings.policy.revision, allowedTools: settings.tools.map(t => t.name) });
  const n = result(await mcp('create_network', { request: { name: 'fixture-net' } }));
  result(await mcp('create_volume', { request: { name: 'fixture-data' } }));
  const c = result(await mcp('create_container', { request: { name: 'fixture-web', image: 'fixture:latest', start: true, mounts: [{ type: 'volume', source: 'fixture-data', target: '/data' }] } }));
  assert.equal(result(await mcp('inspect_volume', { name: 'fixture-data' })).containers.length, 1);
  result(await mcp('connect_network', { networkId: n.id, containerId: c.id }));
  assert.equal(Object.keys(result(await mcp('inspect_network', { networkId: n.id })).Containers).length, 1);
  assert(result(await mcp('container_logs', { containerId: c.id, tail: 5 })).text.includes('fixture log'));
  result(await mcp('disconnect_network', { networkId: n.id, containerId: c.id }));
  result(await mcp('container_action', { containerId: c.id, action: 'stop' }));
  for (const [tool, args] of [['remove_container', { containerId: c.id }], ['remove_network', { networkId: n.id }], ['remove_volume', { name: 'fixture-data' }]]) {
    const preview = result(await mcp(tool, args)); result(await mcp(tool, { ...args, previewToken: preview.previewToken }));
    assert.equal((await mcp(tool, { ...args, previewToken: preview.previewToken })).isError, true);
  }
  await rpc('mcp/settings/set', { enabled: false, revision: settings.policy.revision, allowedTools: [] });
  assert.equal((await mcp('list_containers')).isError, true);
  assert.equal(containers.size + networks.size + volumes.size, 0);
  console.log('PASS: two framed sidecars, shared permissions/revocation, cold MCP connection, create/inspect/connect/disconnect/stop/preview/delete resource lifecycle, token replay rejection.');
  if (process.argv.includes('--ui')) {
    await rpc('docker/createNetwork', { request: { name: 'app-network' } });
    await rpc('docker/createVolume', { request: { name: 'app-data' } });
    await rpc('docker/createContainer', { request: { name: 'app', image: 'fixture:latest', mounts: [{ type: 'volume', source: 'app-data', target: '/data' }] } });
    const injection = `<script>window.dbxPlugin={ready:Promise.resolve(),context:${JSON.stringify({ connectionId: connection.id, connection: { id: connection.id, name: connection.name } })},locale:'zh-CN',onContext:()=>()=>{},onBinary:()=>()=>{},capabilities:{aiRecommendations:true},ai:{setRecommendations:async(v)=>{window.fixtureAI=v},clearRecommendations:async()=>{}},invoke:async(method,params)=>{const r=await fetch('/rpc',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({method,params})});const body=await r.json();if(body.error)throw Error(body.error);return body.result;}};</script>`;
    const html = (await readFile(new URL('../ui/index.html', import.meta.url), 'utf8')).replace('<head>', '<head>' + injection);
    uiServer = createServer(async (req, res) => { if (req.url === '/rpc' && req.method === 'POST') { try { const { method, params } = await readJSON(req); reply(res, { result: await desktop.request(method, params) }); } catch (e) { reply(res, { error: e.message }, 500); } } else { res.setHeader('Content-Type', 'text/html;charset=utf-8'); res.end(html); } });
    await new Promise(resolve => uiServer.listen(0, '127.0.0.1', resolve));
    console.log(`UI fixture: http://127.0.0.1:${uiServer.address().port}`);
    process.once('SIGINT', async () => { await close(); process.exit(0); });
  } else await close();
} catch (error) { await close(); throw error; }

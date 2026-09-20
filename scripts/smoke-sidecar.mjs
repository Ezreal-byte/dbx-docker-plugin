// 端到端冒烟测试：直接以 framed 协议驱动 Go sidecar，覆盖握手、连接（含 DBX 隧道复用）、
// 资源列举、容器日志流、只读文件浏览、容器内交互式终端（双向 binary channel），
// 以及 system df / 镜像打标签 / 分层历史 / 容器重命名。
//
// 用法：
//   node scripts/smoke-sidecar.mjs <sidecar 可执行文件> [docker 端点] [容器名]
// 示例：
//   node scripts/smoke-sidecar.mjs dist/verify-sidecar.exe http://127.0.0.1:2375 dbx-mon-test
//
// 需要本地可访问的 Docker Engine。找不到容器时会自动跳过依赖容器的用例。
//
// 对机器状态的改动：
//   - 容器重命名会在 finally 中还原原名；镜像打标签会在 finally 中撤销该标签。
//   - prune 会真实删除资源，因此默认跳过，只有设置 SMOKE_ALLOW_PRUNE=1 才执行。
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

function defaultExecutable() {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..');
  for (const candidate of ['dist/verify-sidecar.exe', 'dist/verify-sidecar', 'backend/verify-sidecar.exe', 'backend/verify-sidecar']) {
    const path = join(root, candidate);
    if (existsSync(path)) return path;
  }
  return undefined;
}

const executable = process.argv[2] || defaultExecutable();
if (!executable) {
  throw new Error('Usage: node scripts/smoke-sidecar.mjs <sidecar executable> [docker endpoint] [container name]');
}
if (!existsSync(executable)) throw new Error(`Sidecar executable not found: ${executable}`);

const endpoint = parseEndpoint(process.argv[3] || 'http://127.0.0.1:2375');
const containerName = process.argv[4] || 'dbx-mon-test';

const FRAME_JSON = 0;
const FRAME_BINARY = 1;
const CONNECTION_ID = 'smoke-connection';

const checks = [];
let failures = 0;

function parseEndpoint(value) {
  const url = new URL(value);
  const port = url.port ? Number(url.port) : url.protocol === 'https:' ? 2376 : 2375;
  return { protocol: url.protocol.replace(':', ''), host: url.hostname, port };
}

function check(name, fn) {
  return Promise.resolve()
    .then(fn)
    .then((detail) => {
      checks.push({ name, ok: true, detail });
      console.log(`  ok   ${name}${detail ? ` — ${detail}` : ''}`);
    })
    .catch((error) => {
      failures += 1;
      checks.push({ name, ok: false, detail: error?.message || String(error) });
      console.error(`  FAIL ${name} — ${error?.message || error}`);
    });
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

// ---------- framed 协议客户端 ----------

class SidecarClient {
  constructor(command) {
    this.pending = new Map();
    this.binaryListeners = new Set();
    this.sequence = 0;
    this.buffer = Buffer.alloc(0);
    this.stderr = '';
    this.exit = undefined;
    this.child = spawn(command, [], { stdio: ['pipe', 'pipe', 'pipe'] });
    this.child.stderr.on('data', (chunk) => { this.stderr += chunk.toString(); });
    this.child.on('exit', (code) => { this.exit = code; });
    this.child.stdout.on('data', (chunk) => {
      this.buffer = Buffer.concat([this.buffer, chunk]);
      this.drain();
    });
  }

  drain() {
    while (this.buffer.length >= 5) {
      const kind = this.buffer[0];
      const length = this.buffer.readUInt32BE(1);
      if (this.buffer.length < 5 + length) return;
      const payload = this.buffer.subarray(5, 5 + length);
      this.buffer = this.buffer.subarray(5 + length);
      if (kind === FRAME_JSON) this.handleJSON(payload);
      else if (kind === FRAME_BINARY) this.handleBinary(payload);
    }
  }

  handleJSON(payload) {
    let message;
    try {
      message = JSON.parse(payload.toString('utf8'));
    } catch {
      return;
    }
    if (message.id === undefined || message.id === null) return;
    const key = JSON.stringify(message.id);
    const handler = this.pending.get(key);
    if (!handler) return;
    this.pending.delete(key);
    if (message.error) handler.reject(new Error(`${message.error.message} (code ${message.error.code})`));
    else handler.resolve(message.result);
  }

  handleBinary(payload) {
    if (payload.length < 2) return;
    const channelLength = payload.readUInt16BE(0);
    if (payload.length < 2 + channelLength) return;
    const channel = payload.subarray(2, 2 + channelLength).toString('utf8');
    const data = payload.subarray(2 + channelLength);
    for (const listener of this.binaryListeners) listener({ channel, data });
  }

  writeFrame(kind, payload) {
    const header = Buffer.alloc(5);
    header[0] = kind;
    header.writeUInt32BE(payload.length, 1);
    this.child.stdin.write(Buffer.concat([header, payload]));
  }

  request(method, params, timeoutMs = 30_000) {
    this.sequence += 1;
    const id = this.sequence;
    const payload = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
    const promise = new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.pending.delete(JSON.stringify(id));
        reject(new Error(`RPC ${method} timed out after ${timeoutMs}ms`));
      }, timeoutMs);
      this.pending.set(JSON.stringify(id), {
        resolve: (value) => { clearTimeout(timeout); resolve(value); },
        reject: (error) => { clearTimeout(timeout); reject(error); },
      });
    });
    this.writeFrame(FRAME_JSON, payload);
    return promise;
  }

  /** 发送 binary channel 数据：帧头 [u16 channelLen][channel][data]。 */
  sendBinary(channel, data) {
    const channelBytes = Buffer.from(channel, 'utf8');
    const header = Buffer.alloc(2);
    header.writeUInt16BE(channelBytes.length, 0);
    this.writeFrame(FRAME_BINARY, Buffer.concat([header, channelBytes, Buffer.from(data)]));
  }

  onBinary(listener) {
    this.binaryListeners.add(listener);
    return () => this.binaryListeners.delete(listener);
  }

  close() {
    try {
      this.child.stdin.end();
    } catch {
      // 进程可能已退出。
    }
    this.child.kill();
  }
}

// ---------- 业务帧编解码（插件自定义：1 字节 kind + 4 字节头长度 + JSON 头 + 数据）----------

function decodePluginFrame(bytes) {
  if (bytes.length < 5) return null;
  const headerLength = bytes.readUInt32BE(1);
  if (bytes.length < 5 + headerLength) return null;
  let header;
  try {
    header = JSON.parse(bytes.subarray(5, 5 + headerLength).toString('utf8'));
  } catch {
    return null;
  }
  return { kind: bytes[0], header, data: bytes.subarray(5 + headerLength) };
}

function encodeExecInput(sessionId, payload) {
  const header = Buffer.from(JSON.stringify({ sessionId, kind: 'stdin' }));
  const data = Buffer.isBuffer(payload) ? payload : Buffer.from(payload);
  const frame = Buffer.alloc(5 + header.length + data.length);
  frame[0] = 1;
  frame.writeUInt32BE(header.length, 1);
  header.copy(frame, 5);
  data.copy(frame, 5 + header.length);
  return frame;
}

/** 收集某个 docker-exec 会话直到出现 done 帧。 */
function waitForExecDone(sessionId, timeoutMs = 20_000) {
  let output = '';
  let exitCode;
  let settled = false;
  let resolveDone;
  const done = new Promise((resolve) => { resolveDone = resolve; });
  const stop = client.onBinary(({ channel, data }) => {
    if (channel !== 'docker-exec') return;
    const frame = decodePluginFrame(data);
    if (frame?.header?.sessionId !== sessionId) return;
    if (frame.kind === 1) output += frame.data.toString('utf8');
    if (typeof frame.header.exitCode === 'number') exitCode = frame.header.exitCode;
    if (!settled && (frame.header.status === 'done' || frame.header.status === 'error')) {
      settled = true;
      resolveDone();
    }
  });
  const timeout = setTimeout(() => { if (!settled) { settled = true; resolveDone(); } }, timeoutMs);
  return {
    promise: done.finally(() => { clearTimeout(timeout); stop(); }),
    result: () => ({ output, exitCode }),
  };
}

function connectionPayload(id, overrides = {}) {
  return {
    id,
    name: id,
    host: endpoint.host,
    port: endpoint.port,
    read_only: false,
    color: '',
    external_config: { protocol: endpoint.protocol, api_version: 'auto' },
    connection_secrets: {},
    transport_layers: [],
    ...overrides,
  };
}

// ---------- 用例 ----------

const client = new SidecarClient(executable);
let container = null;

try {
  console.log('Sidecar smoke test');

  await check('handshake', async () => {
    const result = await client.request('plugin/initialize', { host: { protocolVersions: [1] } });
    assert(result.protocolVersion === 1, `unexpected protocolVersion ${result.protocolVersion}`);
    assert(result.plugin?.id === 'io.dbx.docker', `unexpected plugin id ${result.plugin?.id}`);
    assert(result.plugin?.version === '0.1.4', `unexpected plugin version ${result.plugin?.version}`);
    return `${result.plugin.id} ${result.plugin.version}`;
  });

  await check('connection/test (direct endpoint)', async () => {
    const result = await client.request('connection/test', {
      connection: connectionPayload(CONNECTION_ID),
      runtime: { host: endpoint.host, port: endpoint.port },
    });
    assert(result.success === true, `connection/test failed: ${JSON.stringify(result)}`);
    return result.message;
  });

  await check('DBX tunnel reuse (transport_layers + runtime loopback)', async () => {
    const id = 'smoke-tunnel';
    const result = await client.request('connection/test', {
      // 逻辑端点是不可达的远端主机，实际拨号必须走宿主给出的 runtime 回环端点。
      connection: connectionPayload(id, {
        host: 'docker.example.invalid',
        port: 2375,
        transport_layers: [{ type: 'ssh', enabled: true, id: 'hop-1', name: 'hop-1' }],
      }),
      runtime: { host: endpoint.host, port: endpoint.port },
    });
    assert(result.success === true, `tunnel connection/test failed: ${JSON.stringify(result)}`);
    await client.request('connection/connect', {
      connection: connectionPayload(id, {
        host: 'docker.example.invalid',
        port: 2375,
        transport_layers: [{ type: 'ssh', enabled: true, id: 'hop-1', name: 'hop-1' }],
      }),
      runtime: { host: endpoint.host, port: endpoint.port },
    });
    await client.request('docker/getConnectionInfo', { connectionId: id });
    await client.request('connection/disconnect', { connection: connectionPayload(id) });
    return 'dialed runtime endpoint through tunnel layer';
  });

  await check('remote plain HTTP without tunnel is rejected', async () => {
    let message = '';
    try {
      await client.request('connection/test', {
        connection: connectionPayload('smoke-insecure', { host: 'docker.example.invalid', port: 2375 }),
        runtime: { host: '', port: 0 },
      });
    } catch (error) {
      message = error.message;
    }
    assert(message.includes('Remote Docker HTTP is disabled'), `expected the insecure-HTTP guard, got: ${message || '(no error)'}`);
    return 'guard fired';
  });

  await check('connection/connect', async () => {
    const result = await client.request('connection/connect', {
      connection: connectionPayload(CONNECTION_ID),
      runtime: { host: endpoint.host, port: endpoint.port },
    });
    assert(result.success === true, 'connect failed');
  });

  await check('docker/getConnectionInfo', async () => {
    const result = await client.request('docker/getConnectionInfo', { connectionId: CONNECTION_ID });
    assert(result.success === true, 'getConnectionInfo failed');
    assert(result.info?.apiVersion, 'missing apiVersion');
    return `engine ${result.info.engineVersion} / API ${result.info.apiVersion}`;
  });

  await check('docker/listContainers', async () => {
    const containers = await client.request('docker/listContainers', { connectionId: CONNECTION_ID, all: true });
    assert(Array.isArray(containers), 'expected an array');
    container = containers.find((item) => item.names?.some((name) => name.replace(/^\//, '') === containerName))
      ?? containers.find((item) => item.state === 'running')
      ?? null;
    return `${containers.length} containers`;
  });

  await check('docker/listImages', async () => {
    const images = await client.request('docker/listImages', { connectionId: CONNECTION_ID });
    assert(Array.isArray(images) && images.length > 0, 'expected at least one image');
    return `${images.length} images`;
  });

  await check('docker/listVolumes', async () => {
    const volumes = await client.request('docker/listVolumes', { connectionId: CONNECTION_ID });
    assert(Array.isArray(volumes), 'expected an array');
    return `${volumes.length} volumes`;
  });

  await check('docker/listNetworks', async () => {
    const networks = await client.request('docker/listNetworks', { connectionId: CONNECTION_ID });
    assert(Array.isArray(networks) && networks.length > 0, 'expected at least one network');
    return `${networks.length} networks`;
  });

  await check('docker/getEngineDetails', async () => {
    const details = await client.request('docker/getEngineDetails', { connectionId: CONNECTION_ID });
    assert(details.version?.Version, 'missing version payload');
    assert(Array.isArray(details.summary?.securityOptions), 'missing summary');
    return `driver ${details.summary.storageDriver}`;
  });

  if (!container) {
    console.log('  skip container-dependent cases (no running container found)');
  } else {
    const containerId = container.id;

    await check('docker/listContainerFiles', async () => {
      const entries = await client.request('docker/listContainerFiles', { connectionId: CONNECTION_ID, containerId, path: '/' });
      assert(Array.isArray(entries) && entries.length > 0, 'expected directory entries');
      return `${entries.length} entries`;
    });

    await check('docker/startLogs + docker/stopStream', async () => {
      const sessionId = 'smoke-logs';
      let received = '';
      let sawRunning = false;
      const stopListening = client.onBinary(({ channel, data }) => {
        if (channel !== 'docker-log') return;
        const frame = decodePluginFrame(data);
        if (frame?.header?.sessionId !== sessionId) return;
        if (frame.header.status === 'running') sawRunning = true;
        if (frame.kind === 1) received += frame.data.toString('utf8');
      });
      try {
        await client.request('docker/startLogs', {
          connectionId: CONNECTION_ID,
          containerId,
          sessionId,
          options: { tail: 20, timestamps: false },
        });
        // 容器可能本就没有历史输出，因此断言「流已挂上」而非「一定有字节」。
        const deadline = Date.now() + 8_000;
        while (!sawRunning && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 100));
        assert(sawRunning, 'log stream never reported a running frame');
        await client.request('docker/stopStream', { connectionId: CONNECTION_ID, sessionId });
      } finally {
        stopListening();
      }
      return `${received.length} log bytes`;
    });

    await check('docker/startExec + binary stdin + execResize + stopExec', async () => {
      const sessionId = 'smoke-exec';
      const marker = 'dbx-smoke-exec-ok';
      let output = '';
      let done = false;
      const stopListening = client.onBinary(({ channel, data }) => {
        if (channel !== 'docker-exec') return;
        const frame = decodePluginFrame(data);
        if (frame?.header?.sessionId !== sessionId) return;
        if (frame.kind === 1) output += frame.data.toString('utf8');
        else if (frame.header.status === 'done' || frame.header.status === 'error') done = true;
      });
      try {
        const started = await client.request('docker/startExec', {
          connectionId: CONNECTION_ID,
          containerId,
          sessionId,
          command: ['/bin/sh'],
          cols: 100,
          rows: 30,
        });
        assert(started.sessionId === sessionId, 'unexpected sessionId');

        await new Promise((resolve) => setTimeout(resolve, 300));
        client.sendBinary('docker-exec', encodeExecInput(sessionId, `echo ${marker}\n`));

        const deadline = Date.now() + 10_000;
        while (!output.includes(marker) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 100));
        assert(output.includes(marker), `marker not found in exec output: ${JSON.stringify(output.slice(-200))}`);

        const resized = await client.request('docker/execResize', { connectionId: CONNECTION_ID, sessionId, cols: 120, rows: 40 });
        assert(resized.success === true, 'execResize did not report success');

        // 退出 shell 后应收到 done 帧。
        client.sendBinary('docker-exec', encodeExecInput(sessionId, 'exit\n'));
        const exitDeadline = Date.now() + 8_000;
        while (!done && Date.now() < exitDeadline) await new Promise((resolve) => setTimeout(resolve, 100));
        assert(done, 'no terminal done frame after exit');
      } finally {
        stopListening();
        await client.request('docker/stopExec', { connectionId: CONNECTION_ID, sessionId }).catch(() => {});
      }
      return `${output.length} terminal bytes, done frame received`;
    });

    await check('container file upload + download round-trip', async () => {
      const target = '/tmp/dbx-smoke-upload.txt';
      const payload = Buffer.from(`dbx smoke file ${Date.now()}\n${'x'.repeat(4096)}\n`);

      const uploadSession = 'smoke-upload';
      const uploadWaiter = waitForExecDone(uploadSession);
      await client.request('docker/startFileUpload', {
        connectionId: CONNECTION_ID,
        containerId,
        path: target,
        sessionId: uploadSession,
        size: payload.length,
        mode: 'overwrite',
      });
      client.sendBinary('docker-exec', encodeExecInput(uploadSession, payload));
      await uploadWaiter.promise;
      const upload = uploadWaiter.result();
      assert((upload.exitCode ?? 0) === 0, `upload exited with ${upload.exitCode}: ${upload.output}`);

      const downloadSession = 'smoke-download';
      const chunks = [];
      let finished = false;
      let failure = '';
      const stopListening = client.onBinary(({ channel, data }) => {
        if (channel !== 'docker-file') return;
        const frame = decodePluginFrame(data);
        if (frame?.header?.sessionId !== downloadSession) return;
        if (frame.kind === 1) chunks.push(Buffer.from(frame.data));
        if (frame.header.status === 'error') failure = frame.header.error || 'download failed';
        if (frame.header.status === 'done' || frame.header.status === 'error') finished = true;
      });
      try {
        const started = await client.request('docker/startFileDownload', {
          connectionId: CONNECTION_ID,
          containerId,
          path: target,
          sessionId: downloadSession,
        });
        assert(started.size === payload.length, `download reported size ${started.size}, expected ${payload.length}`);
        const deadline = Date.now() + 15_000;
        while (!finished && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 50));
        assert(finished, 'the file download never finished');
        assert(!failure, `download failed: ${failure}`);
        const received = Buffer.concat(chunks);
        assert(received.length === payload.length, `received ${received.length} of ${payload.length} bytes`);
        assert(received.equals(payload), 'downloaded bytes differ from the uploaded bytes');
        return `${received.length} bytes round-tripped`;
      } finally {
        stopListening();
        // 清理临时文件，避免在容器里留下测试产物。
        const cleanupSession = 'smoke-cleanup';
        const cleanupWaiter = waitForExecDone(cleanupSession);
        await client
          .request('docker/startExec', {
            connectionId: CONNECTION_ID,
            containerId,
            sessionId: cleanupSession,
            command: ['/bin/sh', '-c', `rm -f ${target}`],
            cols: 80,
            rows: 24,
          })
          .catch(() => {});
        await cleanupWaiter.promise.catch(() => {});
      }
    });

    await check('read-only connection blocks terminal and uploads', async () => {
      const id = 'smoke-readonly';
      await client.request('connection/connect', {
        connection: connectionPayload(id, { read_only: true }),
        runtime: { host: endpoint.host, port: endpoint.port },
      });
      let terminalMessage = '';
      let uploadMessage = '';
      try {
        await client.request('docker/startExec', {
          connectionId: id,
          containerId,
          sessionId: 'smoke-readonly-exec',
          command: ['/bin/sh'],
          cols: 80,
          rows: 24,
        });
      } catch (error) {
        terminalMessage = error.message;
      }
      try {
        await client.request('docker/startFileUpload', {
          connectionId: id,
          containerId,
          path: '/tmp/dbx-smoke-readonly.txt',
          sessionId: 'smoke-readonly-upload',
          size: 4,
          mode: 'overwrite',
        });
      } catch (error) {
        uploadMessage = error.message;
      }
      await client.request('connection/disconnect', { connection: connectionPayload(id) });
      assert(terminalMessage.includes('read-only'), `expected the read-only terminal guard, got: ${terminalMessage || '(no error)'}`);
      assert(uploadMessage.includes('read-only'), `expected the read-only upload guard, got: ${uploadMessage || '(no error)'}`);
      return 'guards fired';
    });
  }

  await check('docker/getDiskUsage', async () => {
    const usage = await client.request('docker/getDiskUsage', { connectionId: CONNECTION_ID });
    assert(typeof usage.layersSize === 'number', 'missing layersSize');
    for (const key of ['images', 'containers', 'volumes', 'buildCache', 'networks']) {
      assert(usage[key] && typeof usage[key].count === 'number', `missing ${key} category`);
    }
    return `images ${usage.images.count} / volumes ${usage.volumes.count} / networks ${usage.networks.count}`;
  });

  await check('docker/tagImage + docker/untagImage', async () => {
    const images = await client.request('docker/listImages', { connectionId: CONNECTION_ID });
    const tagged = images.find((item) => (item.repoTags || []).some((tag) => tag && tag !== '<none>:<none>'));
    assert(tagged, 'no tagged image available for the tag test');
    const reference = `dbx-smoke-tag:probe`;
    try {
      const result = await client.request('docker/tagImage', {
        connectionId: CONNECTION_ID,
        imageId: tagged.id,
        repository: 'dbx-smoke-tag',
        tag: 'probe',
      });
      assert(result.reference === reference, `unexpected reference ${result.reference}`);
      const after = await client.request('docker/listImages', { connectionId: CONNECTION_ID });
      const hasTag = after.some((item) => item.id === tagged.id && (item.repoTags || []).includes(reference));
      assert(hasTag, 'the new tag is missing from the image list');
      return reference;
    } finally {
      // 无论断言是否通过都摘掉探测标签，保证机器状态干净。
      await client.request('docker/untagImage', { connectionId: CONNECTION_ID, reference }).catch(() => {});
    }
  });

  await check('docker/imageHistory', async () => {
    const images = await client.request('docker/listImages', { connectionId: CONNECTION_ID });
    const target = images.find((item) => (item.repoTags || []).some((tag) => tag && tag !== '<none>:<none>'));
    assert(target, 'no tagged image available for the history test');
    const layers = await client.request('docker/imageHistory', { connectionId: CONNECTION_ID, imageId: target.id });
    assert(Array.isArray(layers) && layers.length > 0, 'expected at least one layer');
    assert(typeof layers[0].createdBy === 'string', 'missing createdBy');
    return `${layers.length} layers`;
  });

  // 清理类用例会真实删除资源，因此默认跳过；需要验证时显式设置 SMOKE_ALLOW_PRUNE=1。
  if (process.env.SMOKE_ALLOW_PRUNE === '1') {
    await check('docker/prune (containers)', async () => {
      const result = await client.request('docker/prune', { connectionId: CONNECTION_ID, target: 'containers', all: false });
      assert(Array.isArray(result.deleted), 'expected a deleted list');
      assert(typeof result.spaceReclaimed === 'number', 'missing spaceReclaimed');
      return `${result.deleted.length} removed`;
    });
  } else {
    console.log('  skip docker/prune (set SMOKE_ALLOW_PRUNE=1 to allow deleting stopped containers)');
  }

  if (container) {
    await check('docker/renameContainer round-trip', async () => {
      const renamed = `${containerName}-smoke`;
      try {
        const result = await client.request('docker/renameContainer', {
          connectionId: CONNECTION_ID,
          containerId: container.id,
          name: renamed,
        });
        assert(result.name === renamed, 'unexpected container name');
        const containers = await client.request('docker/listContainers', { connectionId: CONNECTION_ID, all: true });
        const match = containers.find((item) => item.id === container.id);
        assert(match?.names?.some((name) => name.replace(/^\//, '') === renamed), 'rename was not reflected in the list');
        return `${containerName} → ${renamed}`;
      } finally {
        // 还原原名，避免影响后续手工验证。
        await client
          .request('docker/renameContainer', { connectionId: CONNECTION_ID, containerId: container.id, name: containerName })
          .catch(() => {});
      }
    });
  }

  await check('docker/prunePreview is read-only and never lists running containers', async () => {
    const before = await client.request('docker/listContainers', { connectionId: CONNECTION_ID, all: true });
    const preview = await client.request('docker/prunePreview', { connectionId: CONNECTION_ID, target: 'containers', all: false });
    const after = await client.request('docker/listContainers', { connectionId: CONNECTION_ID, all: true });
    assert(after.length === before.length, `prunePreview changed the container count: ${before.length} → ${after.length}`);
    assert(Array.isArray(preview.items), 'preview.items must be an array');
    assert(typeof preview.count === 'number' && typeof preview.totalSize === 'number', 'preview must report count and totalSize');
    assert(preview.truncated === true || preview.count === preview.items.length, 'preview.count must match the listed items');
    const protectedIds = new Set(
      after.filter((item) => item.state === 'running' || item.state === 'paused').map((item) => item.id.slice(0, 12)),
    );
    for (const item of preview.items) {
      assert(!protectedIds.has(item.id), `running/paused container ${item.id} must never be a prune candidate`);
    }
    return `${preview.count} candidate(s), nothing deleted`;
  });

  for (const [target, all] of [['images', false], ['images', true], ['volumes', false], ['volumes', true], ['networks', false]]) {
    await check(`docker/prunePreview (${target}${all ? ', all' : ''}) responds without deleting`, async () => {
      const preview = await client.request('docker/prunePreview', { connectionId: CONNECTION_ID, target, all });
      assert(typeof preview.count === 'number', 'missing count');
      assert(Array.isArray(preview.items), 'missing items');
      assert(typeof preview.warning === 'string' && preview.warning.length > 0, 'every target must explain what cleanup removes');
      if (all) {
        // 「包含具名/带标签」的口径必须覆盖默认口径。
        const narrow = await client.request('docker/prunePreview', { connectionId: CONNECTION_ID, target, all: false });
        assert(preview.count >= narrow.count, `all=${all} preview (${preview.count}) must include the default preview (${narrow.count})`);
        return `${preview.count} candidate(s) ⊇ ${narrow.count} default`;
      }
      return `${preview.count} candidate(s) · ${preview.totalSize} bytes`;
    });
  }

  await check('connection/disconnect', async () => {
    const result = await client.request('connection/disconnect', { connection: connectionPayload(CONNECTION_ID) });
    assert(result.success === true, 'disconnect failed');
  });
} finally {
  client.close();
}

console.log('');
if (failures > 0) {
  // sidecar 的失败/panic 信息只出现在 stderr，失败时务必带出来。
  if (client.stderr.trim()) console.error(`Sidecar stderr:\n${client.stderr.trim()}`);
  console.error(`Smoke test failed: ${failures} of ${checks.length} checks failed.`);
  process.exitCode = 1;
} else {
  console.log(`Smoke test passed: ${checks.length} checks.`);
}

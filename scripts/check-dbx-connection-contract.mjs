// Exercise the real DBX condition evaluator, localization and serializer from
// a supplied checkout/ref. No checkout changes or network requests are made.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import * as vue from 'vue'

const checkout = process.argv[2]
const ref = process.argv[3] || 'upstream/main'
if (!checkout) throw new Error('Usage: node scripts/check-dbx-connection-contract.mjs <dbx-checkout> [ref]')
const git = (...args) => execFileSync('git', ['-C', checkout, ...args], { encoding: 'utf8' }).trim()
const commit = git('rev-parse', `${ref}^{commit}`)
const base = 'apps/desktop/src/lib/plugins/'
const modules = new Map()
function load(name) {
  if (modules.has(name)) return modules.get(name)
  const source = git('show', `${commit}:${base}${name}.ts`)
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const exports = {}
  const require = (path) => {
    if (path === '@/lib/common/utils') return { uuid: randomUUID }
    if (path === './pluginData') return load('pluginData')
    if (path === 'vue') return vue
    throw new Error(`Unexpected upstream dependency: ${path}`)
  }
  new Function('require', 'exports', compiled)(require, exports)
  modules.set(name, exports)
  return exports
}
const conditions = load('pluginFieldConditions')
const frontend = load('frontendPlugin')
const manifest = JSON.parse(readFileSync(new URL('../manifest.json', import.meta.url), 'utf8'))
const common = ['display_name', 'protocol', 'api_version', 'read_only', 'is_production']
const ssh = ['ssh_host', 'ssh_port', 'ssh_username', 'ssh_private_key_path', 'ssh_password', 'ssh_known_hosts_path', 'socket_path']
const expected = {
  http: [...common, 'host', 'port', 'allow_insecure_remote_http'],
  https: [...common, 'host', 'port', 'ca_cert_path', 'client_cert_path', 'client_key_path'],
  unix: [...common, 'socket_path'],
  npipe: [...common, 'pipe_path'],
  'unix-over-nc': [...common, ...ssh],
  'unix-over-nc-sudo': [...common, ...ssh],
}
for (const locale of ['en', 'zh-CN']) {
  const registry = new frontend.FrontendPluginRegistry([{ manifest, compatibility: { compatible: true, errors: [] } }], locale)
  const provider = registry.listConnectionProviders()[0].contribution
  const defaults = frontend.pluginConnectionFormValues(provider)
  for (const [protocol, expectedFields] of Object.entries(expected)) {
    const values = { ...defaults, protocol, ssh_host: 'ssh.example.com', ssh_username: 'docker', ssh_password: 'fixture-secret' }
    const read = (key) => values[key]
    const resolve = (key) => provider.fields.find((field) => field.key === key)
    const visible = provider.fields.filter((field) => conditions.pluginFieldIsVisible(field, read, resolve))
    assert.deepEqual(visible.map((field) => field.key).sort(), [...expectedFields].sort(), `${locale}/${protocol}: visibility`)
    const required = visible.filter((field) => conditions.pluginFieldIsRequired(field, read)).map((field) => field.key)
    const wantRequired = ['protocol', ...(protocol === 'npipe' ? ['pipe_path'] : protocol.startsWith('unix') ? ['socket_path'] : ['host', 'port']), ...(protocol.startsWith('unix-over') ? ['ssh_host', 'ssh_username'] : [])]
    assert.deepEqual(required.sort(), wantRequired.sort(), `${locale}/${protocol}: required fields`)
    const saved = frontend.buildPluginConnectionConfig(manifest.id, provider, values)
    assert(saved.port > 0 && saved.port <= 65535, 'DBX validates the bound port even when hidden')
    assert.equal(saved.external_config.protocol, protocol)
    assert.equal(saved.connection_secrets.ssh_password, 'fixture-secret')
    assert.equal(saved.external_config.ssh_password, undefined, 'password must not be stored in external_config')
    const reopened = frontend.pluginConnectionFormValues(provider, saved)
    assert.equal(reopened.protocol, protocol)
    assert.equal(reopened.ssh_password, 'fixture-secret')
    // Switching protocols keeps user-entered paths without showing irrelevant fields.
    const legacy = frontend.buildPluginConnectionConfig(manifest.id, provider, { ...values, port: 12376, ca_cert_path: '/fixtures/ca.pem' })
    const switched = frontend.buildPluginConnectionConfig(manifest.id, provider, { ...frontend.pluginConnectionFormValues(provider, legacy), protocol: 'unix-over-nc' }, legacy)
    assert.equal(switched.port, 12376)
    assert.equal(switched.external_config.ca_cert_path, '/fixtures/ca.pem')
    console.log(`PASS ${locale}/${protocol}: ${visible.length} visible fields; serialization and secrets preserved`)
  }
}
console.log(`DBX connection contract passed against ${commit}`)

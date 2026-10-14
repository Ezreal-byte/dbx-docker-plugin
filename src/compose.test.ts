import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { composeFromInspects } from './compose';
describe('Compose editor round trip', () => {
  const value = { Name: '/demo-web-1', Config: { Image: 'nginx:latest', Labels: { 'com.docker.compose.service': 'web' }, Env: ['TOKEN=a:b #text', 'ENABLED=false'], Entrypoint: ['/entrypoint.sh'], Cmd: ['serve', '--name=a b'] }, HostConfig: { PortBindings: { '80/tcp': [{ HostIp: '::1', HostPort: '8080' }] } }, Mounts: [{ Type: 'volume', Name: 'demo_data', Destination: '/data', RW: false }], NetworkSettings: { Networks: { demo_default: {} } } };
  it('returns YAML and preserves quoting and exact external resource names', () => {
    const yaml = composeFromInspects([value]);
    expect(yaml).toContain('services:'); expect(yaml).not.toContain('"services":');
    const doc = parse(yaml);
    expect(doc.services.web.environment).toEqual(value.Config.Env);
    expect(doc.services.web.command).toEqual(value.Config.Cmd);
    expect(doc.services.web.entrypoint).toEqual(value.Config.Entrypoint);
    expect(doc.services.web.ports).toEqual(['[::1]:8080:80/tcp']);
    expect(doc.services.web.volumes).toEqual(['demo_data:/data:ro']);
    expect(doc.volumes.demo_data).toEqual({ external: true, name: 'demo_data' });
    expect(doc.networks.demo_default.name).toBe('demo_default');
  });
  it('does not silently overwrite replicas or drop unsupported runtime settings', () => {
    expect(() => composeFromInspects([value, value])).toThrow('multiple containers');
    const doc = parse(composeFromInspects([{ ...value, HostConfig: { Privileged: true } }]));
    expect(doc.services.web.privileged).toBe(true);
  });
});

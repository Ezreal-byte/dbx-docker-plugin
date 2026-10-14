import { stringify } from 'yaml';

// Docker stores the effective configuration, not the original Compose source.
// Existing networks/volumes must be referenced by their exact Engine names.
export function composeFromInspects(values: Record<string, any>[]): string {
  const services: Record<string, any> = Object.create(null), networks: Record<string, any> = Object.create(null), volumes: Record<string, any> = Object.create(null);
  for (const value of values) {
    const config = value.Config || {}, host = value.HostConfig || {};
    const name = config.Labels?.['com.docker.compose.service'] || value.Name?.replace(/^\//, '');
    if (!name) throw Error('Container has no Compose service name');
    if (Object.prototype.hasOwnProperty.call(services, name)) throw Error(`Service ${name} has multiple containers; editing replicas is not supported`);
    const service: Record<string, any> = { image: config.Image, container_name: value.Name?.replace(/^\//, '') };
    for (const [from, to] of [['Cmd', 'command'], ['Entrypoint', 'entrypoint'], ['Env', 'environment'], ['User', 'user'], ['WorkingDir', 'working_dir']]) {
      if (config[from]?.length) service[to] = config[from];
    }
    const labels = Object.fromEntries(Object.entries(config.Labels || {}).filter(([key]) => !key.startsWith('com.docker.compose.')));
    if (Object.keys(labels).length) service.labels = labels;
    const ports = Object.entries(host.PortBindings || {}).flatMap(([port, bindings]: [string, any]) =>
      (bindings || []).map((b: any) => `${b.HostIp?.includes(':') ? `[${b.HostIp}]` : b.HostIp || ''}${b.HostIp ? ':' : ''}${b.HostPort || ''}:${port}`));
    if (ports.length) service.ports = ports;
    const mounts = (value.Mounts || []).map((mount: any) => {
      let source = mount.Source;
      if (mount.Type === 'volume') {
        source = mount.Name;
        if (!source) throw Error(`Cannot resolve volume mounted at ${mount.Destination}`);
        volumes[source] = { external: true, name: source };
      } else if (mount.Type !== 'bind') throw Error(`Unsupported mount type: ${mount.Type}; use the original Compose file`);
      return `${source}:${mount.Destination}${mount.RW === false ? ':ro' : ''}`;
    });
    if (mounts.length) service.volumes = mounts;
    if (['host', 'none'].includes(host.NetworkMode)) service.network_mode = host.NetworkMode;
    else {
      const names = Object.keys(value.NetworkSettings?.Networks || {});
      for (const network of names) networks[network] = { external: true, name: network };
      if (names.length) service.networks = names;
      const options: Record<string, any> = {};
      for (const network of names) {
        const endpoint = value.NetworkSettings.Networks[network];
        const aliases = (endpoint.Aliases || []).filter((alias: string) => ![name, service.container_name, value.Id, value.Id?.slice(0, 12)].includes(alias));
        const ipam = endpoint.IPAMConfig;
        options[network] = { ...(aliases.length ? { aliases } : {}), ...(ipam?.IPv4Address ? { ipv4_address: ipam.IPv4Address } : {}), ...(ipam?.IPv6Address ? { ipv6_address: ipam.IPv6Address } : {}) };
      }
      if (Object.values(options).some(v => Object.keys(v).length)) service.networks = options;
    }
    if (host.RestartPolicy?.Name && host.RestartPolicy.Name !== 'no') service.restart = host.RestartPolicy.Name + (host.RestartPolicy.MaximumRetryCount ? `:${host.RestartPolicy.MaximumRetryCount}` : '');
    // Keep unsupported non-default settings visible. The backend rejects them
    // before replacement instead of silently deploying a degraded container.
    if (config.Healthcheck) {
      service.healthcheck = { test: config.Healthcheck.Test };
      for (const [from, to] of [['Interval', 'interval'], ['Timeout', 'timeout'], ['StartPeriod', 'start_period'], ['StartInterval', 'start_interval']]) {
        if (config.Healthcheck[from]) service.healthcheck[to] = `${config.Healthcheck[from]}ns`;
      }
      if (config.Healthcheck.Retries) service.healthcheck.retries = config.Healthcheck.Retries;
    }
    for (const [from, to] of [['Privileged', 'privileged'], ['ReadonlyRootfs', 'read_only'], ['Memory', 'mem_limit'], ['MemorySwap', 'memswap_limit'], ['CpuShares', 'cpu_shares'], ['CpuQuota', 'cpu_quota'], ['CpuPeriod', 'cpu_period'], ['CpusetCpus', 'cpuset'], ['CapAdd', 'cap_add'], ['CapDrop', 'cap_drop'], ['Devices', 'devices'], ['SecurityOpt', 'security_opt'], ['ExtraHosts', 'extra_hosts'], ['Dns', 'dns'], ['Tmpfs', 'tmpfs'], ['Ulimits', 'ulimits']]) {
      const v = host[from]; if (v && (!Array.isArray(v) || v.length) && (typeof v !== 'object' || Object.keys(v).length)) service[to] = v;
    }
    if (host.NanoCpus) service.cpus = host.NanoCpus / 1e9;
    services[name] = service;
  }
  return '# Generated from current containers; review before replacing. Original comments and variables are unavailable.\n' + stringify({ services, ...(Object.keys(networks).length ? { networks } : {}), ...(Object.keys(volumes).length ? { volumes } : {}) }, { indent: 2, lineWidth: 0 });
}

<script setup lang="ts">
import { computed } from 'vue';
import type { DockerContainer } from '../types';
import { local } from '../automation';
import { t } from '../i18n';
import { formatDate, shortId } from '../format';
import Icon from './Icon.vue';
import CodeEditor from './CodeEditor.vue';
import ContainerPorts from './ContainerPorts.vue';
const props = defineProps<{ container: DockerContainer; inspect: Record<string, any>; connectionId: string }>();
const emit = defineEmits<{ copy: [string] }>();
const command = computed(() => [props.inspect.Path, ...(props.inspect.Args || [])].filter(Boolean).join(' ') || props.container.command || '—');
const environment = computed(() => (props.inspect.Config?.Env || []).join('\n'));
const networks = computed(() => Object.entries(props.inspect.NetworkSettings?.Networks || {}) as [string, Record<string, any>][]);
const running = computed(() => props.container.state === 'running');
</script>
<template>
  <div class="container-overview">
    <section class="overview-hero">
      <div class="overview-symbol"><Icon name="box" /></div>
      <div class="overview-identity"><h2>{{ container.names[0]?.replace(/^\//, '') || shortId(container.id) }}</h2><div class="overview-image"><Icon name="layers" /><span>{{ container.image }}</span></div><div class="overview-id"><span :title="container.id">{{ shortId(container.id) }}</span><button class="docker-copy-button" :title="t('copy')" @click="emit('copy', container.id)"><Icon name="copy" /></button><span>· {{ t('created') }} {{ formatDate(container.created) }}</span></div></div>
      <div class="overview-state"><span class="docker-status" :class="running ? 'running' : container.state === 'paused' ? 'paused' : 'stopped'">{{ local(running ? '运行中' : container.state === 'paused' ? '已暂停' : '已停止', container.state) }}</span><span>{{ container.status }}</span><span v-if="inspect.State?.Health" class="health-status" :class="inspect.State.Health.Status">{{ t('health') }} · {{ inspect.State.Health.Status }}</span></div>
    </section>
    <div v-if="inspect.State?.Error" class="error-banner">{{ inspect.State.Error }}</div>
    <div class="overview-columns">
      <section class="overview-section">
        <h3><Icon name="settings" />{{ local('运行配置', 'Runtime') }}</h3>
        <dl class="overview-facts"><div><dt>{{ local('重启策略', 'Restart policy') }}</dt><dd>{{ inspect.HostConfig?.RestartPolicy?.Name || 'no' }}</dd></div><div><dt>{{ local('重启次数', 'Restarts') }}</dt><dd>{{ inspect.RestartCount ?? 0 }}</dd></div><div><dt>{{ local('运行用户', 'User') }}</dt><dd>{{ inspect.Config?.User || local('镜像默认', 'Image default') }}</dd></div><div><dt>{{ local('工作目录', 'Working directory') }}</dt><dd>{{ inspect.Config?.WorkingDir || '/' }}</dd></div><div><dt>{{ local('启动时间', 'Started') }}</dt><dd>{{ inspect.State?.StartedAt && !inspect.State.StartedAt.startsWith('0001') ? new Date(inspect.State.StartedAt).toLocaleString() : '—' }}</dd></div><div v-if="!running"><dt>{{ local('退出代码', 'Exit code') }}</dt><dd>{{ inspect.State?.ExitCode ?? '—' }}</dd></div></dl>
        <div class="overview-command"><div class="overview-field-title"><span>{{ t('command') }}</span><button class="docker-copy-button" :title="t('copy')" @click="emit('copy', command)"><Icon name="copy" /></button></div><code>{{ command }}</code></div>
      </section>
      <section class="overview-section">
        <h3><Icon name="network" />{{ local('网络与端口', 'Networking & ports') }}</h3>
        <div class="overview-ports"><span class="overview-field-title">{{ t('ports') }}</span><ContainerPorts :container="container" :connection-id="connectionId" /></div>
        <div v-for="[name, network] in networks" :key="name" class="overview-network"><div><strong>{{ name }}</strong><small>{{ network.MacAddress || '—' }}</small></div><code>{{ network.IPAddress || network.GlobalIPv6Address || '—' }}</code></div><div v-if="!networks.length" class="overview-empty">{{ local('未连接网络', 'No network attached') }}</div>
      </section>
      <section class="overview-section overview-wide">
        <h3><Icon name="hard-drive" />{{ t('mounts') }}<span class="overview-count">{{ inspect.Mounts?.length || 0 }}</span></h3>
        <div v-for="mount in inspect.Mounts || []" :key="`${mount.Source}-${mount.Destination}`" class="overview-mount"><span class="mount-type">{{ mount.Type }}</span><code :title="mount.Source">{{ mount.Name || mount.Source }}</code><span class="muted">→</span><code>{{ mount.Destination }}</code><span class="mount-access">{{ mount.RW ? local('读写', 'RW') : local('只读', 'RO') }}</span></div><div v-if="!inspect.Mounts?.length" class="overview-empty">{{ local('暂无挂载', 'No mounts') }}</div>
      </section>
      <details class="overview-section overview-wide overview-environment"><summary>{{ t('environment') }}<span class="overview-count">{{ inspect.Config?.Env?.length || 0 }}</span><span class="muted">{{ local('展开查看', 'Expand') }}</span></summary><CodeEditor :model-value="environment" path="container.env" read-only height="260px" /></details>
    </div>
  </div>
</template>

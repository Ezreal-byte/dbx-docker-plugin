<script setup lang="ts">
import type { DockerContainer } from '../types';
import { invoke } from '../bridge';
import { toast } from '../toast';
import { local } from '../automation';
const props = defineProps<{ container: DockerContainer; connectionId: string }>();
async function open(port: DockerContainer['ports'][number]) {
  try { await invoke(props.connectionId, 'docker/openContainerPort', { containerId: props.container.id, publicPort: port.publicPort, privatePort: port.privatePort, ip: port.ip || '' }); }
  catch (e) { toast(String(e), 6000); }
}
</script>
<template><div class="container-ports"><template v-for="(port, index) in container.ports" :key="index"><button v-if="port.publicPort && port.portType === 'tcp'" class="port-link" :title="`${port.ip || '0.0.0.0'}:${port.publicPort} → ${port.privatePort}/tcp · ${local('在浏览器打开', 'Open in browser')}`" @click.stop="open(port)">{{ port.publicPort }}<span>→{{ port.privatePort }}</span><span aria-hidden="true">↗</span></button><span v-else class="port-label">{{ port.publicPort ? `${port.publicPort}→` : '' }}{{ port.privatePort }}/{{ port.portType }}</span></template><span v-if="!container.ports.length" class="muted">—</span></div></template>

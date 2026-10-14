<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { invoke } from '../bridge';
import { local, mutationPreview, mutationExecute, type MutationPreview } from '../automation';
import type { DockerContainer } from '../types';
import { formatBytes } from '../format';
import Dialog from './Dialog.vue';
import JsonTree from './JsonTree.vue';
import LoadingSkeleton from './LoadingSkeleton.vue';
const props = defineProps<{ open: boolean; connectionId: string; kind: string; resourceId: string; readOnly: boolean }>();
const emit = defineEmits<{ 'update:open': [boolean]; changed: [] }>();
const data = ref<any>(), error = ref(''), busy = ref(false), containerId = ref('');
const containers = ref<DockerContainer[]>([]), preview = ref<MutationPreview>();
const spec = computed(() => ({ images: ['Image', 'imageId', 'remove_image'], networks: ['Network', 'networkId', 'remove_network'], volumes: ['Volume', 'name', 'remove_volume'] }[props.kind] || ['Image', 'imageId', 'remove_image']));
const args = computed(() => ({ [spec.value[1]]: props.resourceId }));
const protectedNetwork = computed(() => props.kind === 'networks' && (['bridge', 'host', 'none'].includes(data.value?.Name) || ['host', 'null'].includes(data.value?.Driver)));
const refs = computed<{ id: string; names: string[]; state: string; destination?: string }[]>(() => data.value?.containers || []);
const endpoints = computed<[string, any][]>(() => Object.entries(data.value?.Containers || {}));
const inUse = computed(() => refs.value.length > 0 || endpoints.value.length > 0);
async function load() {
  busy.value = true; error.value = ''; preview.value = undefined;
  try { data.value = await invoke(props.connectionId, `docker/inspect${spec.value[0]}`, args.value); if (props.kind === 'networks') containers.value = await invoke<DockerContainer[]>(props.connectionId, 'docker/listContainers', { all: true }); }
  catch (e) { error.value = String(e); } finally { busy.value = false; }
}
async function changeNetwork(connect: boolean) {
  busy.value = true; error.value = '';
  try { await invoke(props.connectionId, `docker/${connect ? 'connect' : 'disconnect'}Network`, { networkId: props.resourceId, containerId: containerId.value }); await load(); emit('changed'); }
  catch (e) { error.value = String(e); } finally { busy.value = false; }
}
async function remove() {
  busy.value = true; error.value = '';
  try { if (!preview.value) preview.value = await mutationPreview(props.connectionId, spec.value[2], args.value);
    else { const bound = preview.value; preview.value = undefined; await mutationExecute(props.connectionId, spec.value[2], args.value, bound); emit('changed'); emit('update:open', false); }
  } catch (e) { preview.value = undefined; error.value = String(e); } finally { busy.value = false; }
}
watch(() => [props.open, props.resourceId, props.kind, props.connectionId], () => { data.value = undefined; containerId.value = ''; if (props.open && props.resourceId) void load(); });
</script>
<template>
  <Dialog :open="open" content-class="dlg-wide automation-dialog" @update:open="emit('update:open', $event)">
    <div class="dlg-header"><div class="dlg-title">{{ local('资源详情', 'Resource details') }} · {{ kind }}</div><div class="dlg-desc break-all">{{ resourceId }}</div></div>
    <p v-if="error" class="error-banner" role="alert">{{ error }}</p>
    <LoadingSkeleton v-if="busy && !data" />
    <div v-else class="automation-scroll">
      <p v-if="kind === 'images' && data?.details" class="hint-text">{{ data.details.Os }} / {{ data.details.Architecture }} · {{ data.details.Variant || '' }}<br />{{ (data.details.RepoDigests || []).join(', ') }}</p>
      <p v-if="kind === 'volumes'" class="hint-text">{{ data?.usageKnown ? local('容量由 Docker Engine 报告。', 'Size is reported by Docker Engine.') : local('Docker 未提供该卷容量；关联列表包含已停止容器。', 'Volume size is unavailable; references include stopped containers.') }}</p>
      <p v-if="kind === 'volumes' && data?.usageKnown"><strong>{{ formatBytes(data.details.UsageData.Size) }}</strong> · {{ local('引用数', 'References') }} {{ data.details.UsageData.RefCount }}</p>
      <div v-if="kind !== 'networks' && data" class="automation-group"><strong>{{ local('关联容器', 'Referencing containers') }} ({{ refs.length }})</strong><p v-if="!refs.length" class="hint-text">{{ local('没有容器引用此资源', 'No container references this resource') }}</p><div v-for="c in refs" :key="c.id + (c.destination || '')" class="resource-reference"><strong>{{ c.names.join(', ') }}</strong><span>{{ c.state }}</span><code>{{ c.destination || c.id }}</code></div></div>
      <div v-if="kind === 'networks' && data" class="automation-group"><strong>{{ local('网络端点', 'Network endpoints') }} ({{ endpoints.length }})</strong><p v-if="!endpoints.length" class="hint-text">{{ local('尚未连接容器', 'No containers connected') }}</p><div v-for="[id, endpoint] in endpoints" :key="id" class="resource-reference"><strong>{{ endpoint.Name || id }}</strong><code>{{ endpoint.IPv4Address }} {{ endpoint.IPv6Address }}</code></div><p class="hint-text">{{ data.Driver }} · {{ (data.IPAM?.Config || []).map((v: any) => `${v.Subnet || ''} / ${v.Gateway || ''}`).join(', ') }}</p></div>
      <div v-if="kind === 'networks' && !readOnly && !protectedNetwork" class="automation-group">
        <label class="docker-field"><span>{{ local('选择容器', 'Select container') }}</span><select v-model="containerId" class="docker-select"><option value="">—</option><option v-for="c in containers" :key="c.id" :value="c.id">{{ c.names.join(', ') }} · {{ c.state }}</option></select></label>
        <div class="dlg-footer"><button class="btn btn-outline" :disabled="busy || !containerId" @click="changeNetwork(true)">{{ local('连接到网络', 'Connect') }}</button><button class="btn btn-outline" :disabled="busy || !containerId || !data?.Containers?.[containerId]" @click="changeNetwork(false)">{{ local('断开网络', 'Disconnect') }}</button></div>
      </div>
      <p v-if="protectedNetwork" class="hint-text">{{ local('Docker 内置网络受到保护。', 'Docker built-in networks are protected.') }}</p>
      <template v-if="preview"><p class="error-banner">{{ local('确认删除以下资源；数据删除无法撤销。预览五分钟内有效。', 'Confirm deletion below. Data deletion cannot be undone. Preview expires in five minutes.') }} {{ preview.isProduction ? local('生产环境', 'Production') : '' }}</p><JsonTree :value="preview.preview" :initial-expanded-depth="2" /></template>
      <details v-else-if="data"><summary>{{ local('完整 Inspect 详情', 'Full inspect details') }}</summary><JsonTree :value="data" :initial-expanded-depth="2" /></details>
    </div>
    <p v-if="inUse" class="hint-text">{{ local('资源正在被引用，需解除引用后才能删除。', 'Remove container references before deleting this resource.') }}</p>
    <div class="dlg-footer"><button class="btn btn-outline" @click="emit('update:open', false)">{{ local('关闭', 'Close') }}</button><button v-if="preview" class="btn btn-outline" @click="preview = undefined">{{ local('取消删除', 'Cancel deletion') }}</button><button v-else class="btn btn-outline" :disabled="busy" @click="load">{{ local('刷新', 'Refresh') }}</button><button class="btn btn-danger" :disabled="busy || readOnly || protectedNetwork || inUse || !data" @click="remove">{{ preview ? local('确认删除', 'Confirm deletion') : local('预览删除', 'Preview deletion') }}</button></div>
  </Dialog>
</template>
<style scoped>.resource-reference{display:flex;flex-wrap:wrap;gap:12px;padding:10px 0;border-bottom:1px solid var(--border);font-size:12px}.resource-reference code{overflow-wrap:anywhere}summary{cursor:pointer}</style>

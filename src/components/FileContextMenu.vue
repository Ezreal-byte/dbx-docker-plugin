<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import { local } from '../automation';
import type { DockerFileEntry } from '../types';
import Icon from './Icon.vue';
const props = defineProps<{ open: boolean; x: number; y: number; entry?: DockerFileEntry; readOnly: boolean }>();
const emit = defineEmits<{ close: []; action: [string] }>();
const menu = ref<HTMLElement>();
const position = computed(() => ({ left: `${Math.max(8, Math.min(props.x, window.innerWidth - 218))}px`, top: `${Math.max(8, Math.min(props.y, window.innerHeight - 350))}px` }));
const items = computed(() => [
  ...(props.entry ? [{ id: 'open', icon: props.entry.kind === 'directory' ? 'folder' : 'file', label: local('打开 / 预览', 'Open / preview') }, { id: 'download', icon: 'download', label: props.entry.kind === 'directory' ? local('下载文件夹（tar）', 'Download folder (tar)') : local('下载', 'Download') }] : []),
  { id: 'copy', icon: 'copy', label: local('复制路径', 'Copy path') },
  { id: 'upload', icon: 'upload', label: local('上传文件', 'Upload files'), write: true },
  { id: 'mkdir', icon: 'folder', label: local('新建文件夹', 'New folder'), write: true },
  { id: 'create', icon: 'file', label: local('新建文件', 'New file'), write: true },
  ...(props.entry ? [{ id: 'rename', icon: 'pencil', label: local('重命名', 'Rename'), write: true }, { id: 'delete', icon: 'trash-2', label: local('删除', 'Delete'), write: true }] : []),
  { id: 'refresh', icon: 'refresh-cw', label: local('刷新', 'Refresh') },
]);
watch(() => props.open, async value => { if (value) { await nextTick(); menu.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus(); } });
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape' || event.key === 'Tab') { emit('close'); return; }
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const buttons = Array.from(menu.value?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') || []);
  const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (i + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
  buttons[next]?.focus();
}
</script>
<template>
  <Teleport to="body">
    <div v-if="open" class="context-backdrop" @pointerdown.self="emit('close')" @contextmenu.prevent.self="emit('close')" @keydown="keydown">
      <div ref="menu" class="file-context-menu" role="menu" :style="position">
        <button v-for="item in items" :key="item.id" type="button" role="menuitem" :disabled="item.write && readOnly" :class="{ 'context-danger': item.id === 'delete' }" @click="emit('action', item.id); emit('close')"><Icon :name="item.icon" />{{ item.label }}</button>
      </div>
    </div>
  </Teleport>
</template>

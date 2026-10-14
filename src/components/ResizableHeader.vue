<script setup lang="ts">
import { onUnmounted, ref } from 'vue';
import { local } from '../automation';
const props = defineProps<{ width: number }>();
const emit = defineEmits<{ resize: [number]; commit: [] }>();
const header = ref<HTMLTableCellElement>();
let stop: (() => void) | undefined;
function begin(event: PointerEvent) {
  if (event.button !== 0) return;
  stop?.();
  const x = event.clientX, width = header.value?.getBoundingClientRect().width || props.width;
  const move = (e: PointerEvent) => emit('resize', Math.max(64, Math.min(1600, Math.round(width + e.clientX - x))));
  const cursor = document.body.style.cursor, select = document.body.style.userSelect;
  document.body.style.cursor = 'col-resize'; document.body.style.userSelect = 'none';
  stop = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', finish); window.removeEventListener('pointercancel', finish); window.removeEventListener('blur', finish); document.body.style.cursor = cursor; document.body.style.userSelect = select; stop = undefined; };
  const finish = () => { stop?.(); emit('commit'); };
  window.addEventListener('pointermove', move); window.addEventListener('pointerup', finish); window.addEventListener('pointercancel', finish); window.addEventListener('blur', finish);
}
function resizeKey(event: KeyboardEvent) { if (!['ArrowLeft','ArrowRight'].includes(event.key)) return; event.preventDefault(); emit('resize', Math.max(64, Math.min(1600, props.width + (event.key === 'ArrowLeft' ? -20 : 20)))); emit('commit'); }
onUnmounted(() => stop?.());
</script>
<template><th ref="header"><slot /><span class="column-resizer" role="separator" aria-orientation="vertical" :aria-label="local('调整列宽', 'Resize column')" :aria-valuenow="width" :aria-valuemin="64" :aria-valuemax="1600" tabindex="0" :title="local('拖动调整列宽，或使用左右方向键', 'Drag to resize, or use the arrow keys')" @pointerdown.prevent.stop="begin" @click.stop @keydown="resizeKey" /></th></template>

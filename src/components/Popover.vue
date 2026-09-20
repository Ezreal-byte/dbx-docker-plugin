<template>
  <div class="pop-root" ref="root">
    <div @click="toggle">
      <slot name="trigger" />
    </div>
    <Teleport to="body">
      <div
        v-if="open"
        class="pop-content"
        :style="popStyle"
      >
        <slot />
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, type CSSProperties } from 'vue';

const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ 'update:open': [value: boolean] }>();

const root = ref<HTMLElement>();
const triggerRect = ref<DOMRect | null>(null);

function measure() {
  // 打开时必须记录锚点位置：既覆盖点击打开，也覆盖外部代码直接置 open=true
  // （否则 popStyle 为空对象，内容会以未定位的形态渲染，看起来像没反应）。
  if (root.value) triggerRect.value = root.value.getBoundingClientRect();
}

function toggle() {
  if (!props.open) measure();
  emit('update:open', !props.open);
}

watch(
  () => props.open,
  (open) => {
    if (open) measure();
  },
);

const popStyle = computed<CSSProperties>(() => {
  if (!triggerRect.value) return {};
  const rect = triggerRect.value;
  return {
    position: 'fixed',
    top: `${rect.bottom + 6}px`,
    right: `${window.innerWidth - rect.right}px`,
    zIndex: 1000,
  };
});

function onDocClick(event: MouseEvent) {
  if (!props.open) return;
  if (root.value && root.value.contains(event.target as Node)) return;
  if ((event.target as HTMLElement).closest('.pop-content')) return;
  emit('update:open', false);
}

onMounted(() => {
  document.addEventListener('mousedown', onDocClick, true);
  window.addEventListener('resize', measure);
  window.addEventListener('scroll', measure, true);
  if (props.open) measure();
});
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocClick, true);
  window.removeEventListener('resize', measure);
  window.removeEventListener('scroll', measure, true);
});
</script>

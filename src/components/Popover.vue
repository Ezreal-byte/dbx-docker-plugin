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
import { computed, onBeforeUnmount, onMounted, ref, type CSSProperties } from 'vue';

const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ 'update:open': [value: boolean] }>();

const root = ref<HTMLElement>();
const triggerRect = ref<DOMRect | null>(null);

function toggle() {
  if (!props.open && root.value) {
    triggerRect.value = root.value.getBoundingClientRect();
  }
  emit('update:open', !props.open);
}

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

onMounted(() => document.addEventListener('mousedown', onDocClick, true));
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocClick, true));
</script>

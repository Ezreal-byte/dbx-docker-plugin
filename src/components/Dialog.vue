<template>
  <Teleport to="body">
    <div ref="overlay" class="dlg-overlay" :class="{ 'dlg-elevated': elevated }" v-if="open" @mousedown.self="close">
      <div class="dlg-content" :class="[contentClass, { 'dlg-dismissible': dismissible }]" role="dialog" aria-modal="true">
        <button v-if="dismissible" type="button" class="icon-btn dlg-close" :aria-label="local('关闭', 'Close')" :title="local('关闭', 'Close')" @click="close"><Icon name="x" /></button>
        <slot />
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import Icon from './Icon.vue';
import { local } from '../automation';
import { nextTick, onUnmounted, ref, watch } from 'vue';
const props = defineProps<{ open: boolean; contentClass?: string; dismissible?: boolean; elevated?: boolean }>();
const emit = defineEmits<{ 'update:open': [value: boolean] }>();
const overlay = ref<HTMLElement>();
let previousFocus: HTMLElement | null = null;
function isTopDialog() {
  const layers = [...document.querySelectorAll<HTMLElement>('.dlg-overlay')];
  layers.sort((a, b) => Number(getComputedStyle(a).zIndex) - Number(getComputedStyle(b).zIndex));
  return layers[layers.length - 1] === overlay.value;
}
function focusables() {
  return [...(overlay.value?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]') || [])].filter(el => el.getClientRects().length > 0);
}
function keydown(event: KeyboardEvent) {
  if (!props.open || !isTopDialog()) return;
  if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); close(); return; }
  if (event.key !== 'Tab') return;
  const elements = focusables();
  const index = elements.indexOf(document.activeElement as HTMLElement);
  if (!elements.length) { event.preventDefault(); return; }
  if (event.shiftKey ? index <= 0 : index < 0 || index === elements.length - 1) {
    event.preventDefault(); elements[event.shiftKey ? elements.length - 1 : 0]?.focus();
  }
}
watch(() => props.open, async open => {
  if (open) {
    previousFocus = document.activeElement as HTMLElement;
    document.addEventListener('keydown', keydown, true);
    await nextTick();
    if (props.open && isTopDialog()) (overlay.value?.querySelector<HTMLElement>('[autofocus]') || focusables()[0])?.focus();
  } else {
    document.removeEventListener('keydown', keydown, true);
    if (previousFocus?.isConnected) previousFocus.focus();
  }
}, { immediate: true });
onUnmounted(() => document.removeEventListener('keydown', keydown, true));

function close() {
  emit('update:open', false);
}
</script>

<template>
  <Teleport to="body">
    <div class="dlg-overlay" v-if="open" @mousedown.self="close">
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
const props = defineProps<{ open: boolean; contentClass?: string; dismissible?: boolean }>();
const emit = defineEmits<{ 'update:open': [value: boolean] }>();

function close() {
  emit('update:open', false);
}
</script>

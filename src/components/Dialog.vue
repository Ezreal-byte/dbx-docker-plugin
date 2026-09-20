<template>
  <Teleport to="body">
    <div class="dlg-overlay" v-if="open" @mousedown.self="close">
      <div class="dlg-content" :class="contentClass">
        <slot />
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
const props = defineProps<{ open: boolean; contentClass?: string }>();
const emit = defineEmits<{ 'update:open': [value: boolean] }>();

function close() {
  emit('update:open', false);
}
</script>

// 危险操作确认对话框（对应分支 DangerConfirmDialog 的用法）。
<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <div class="dlg-header">
      <div class="dlg-title">{{ message }}</div>
    </div>
    <div class="dlg-footer">
      <button class="btn btn-outline" @click="emit('update:open', false)">{{ cancelLabel }}</button>
      <button class="btn btn-danger" @click="onConfirm">{{ confirmLabel }}</button>
    </div>
  </Dialog>
</template>

<script setup lang="ts">
import Dialog from './Dialog.vue';

defineProps<{
  open: boolean;
  message: string;
  confirmLabel: string;
  cancelLabel: string;
}>();

const emit = defineEmits<{
  'update:open': [value: boolean];
  confirm: [];
}>();

function onConfirm() {
  emit('confirm');
  emit('update:open', false);
}
</script>

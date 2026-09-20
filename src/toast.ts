// 全局 toast：极简实现（挂载在 document.body 的固定容器）。
import { reactive } from 'vue';

export interface ToastItem {
  id: number;
  message: string;
  expiresAt: number;
}

export const toasts = reactive<ToastItem[]>([]);
let nextId = 1;

export function toast(message: string, durationMs = 3000) {
  const id = nextId++;
  toasts.push({ id, message, expiresAt: Date.now() + durationMs });
  window.setTimeout(() => {
    const idx = toasts.findIndex((t) => t.id === id);
    if (idx >= 0) toasts.splice(idx, 1);
  }, durationMs);
}

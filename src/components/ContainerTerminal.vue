<script setup lang="ts">
// 容器内交互式终端：xterm.js 前端 + 后端 docker exec attach（Tty）双向通道。
// 输入经 binary channel docker-exec 上行，输出经同一 channel 下行。
import { onMounted, onUnmounted, ref } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { t } from '../i18n';
import { newSessionId } from '../pullTask';
import { registerExecStream, unregisterStream, type ExecEvent } from '../streams';
import { sendExecInput } from '../exec';
import * as api from '../api';

const props = defineProps<{
  connectionId: string;
  containerId: string;
  readOnly: boolean;
  running: boolean;
}>();

const host = ref<HTMLDivElement>();
const command = ref('/bin/sh');
const status = ref<'idle' | 'starting' | 'running' | 'closed' | 'error'>('idle');
const error = ref('');

let term: Terminal | undefined;
let fit: FitAddon | undefined;
let sessionId = '';
let resizeObserver: ResizeObserver | undefined;
let dataDisposable: { dispose: () => void } | undefined;
let resizeDisposable: { dispose: () => void } | undefined;
let fitTimer: number | undefined;

function parseCommand(raw: string): string[] {
  return raw.trim().split(/\s+/).filter(Boolean);
}

function writeNotice(text: string) {
  term?.write(`\r\n\x1b[2m${text}\x1b[0m\r\n`);
}

function syncSize() {
  if (!fit || !term || status.value !== 'running' || !sessionId) return;
  try {
    fit.fit();
  } catch {
    // 终端尚未布局完成时 fit 会抛错，忽略即可。
    return;
  }
  void api.execResize(props.connectionId, sessionId, term.cols, term.rows).catch(() => {
    // 尺寸同步失败不影响交互，下一次 fit 会重试。
  });
}

function handleExecEvent(event: ExecEvent) {
  if (event.chunk) term?.write(event.chunk);
  if (!event.done) return;
  status.value = event.error ? 'error' : 'closed';
  if (event.error) error.value = event.error;
  const code = event.exitCode ?? 0;
  writeNotice(t('terminalClosed', { code }));
}

async function attach() {
  if (!term) return;
  if (props.readOnly) {
    status.value = 'error';
    error.value = t('terminalReadOnly');
    writeNotice(t('terminalReadOnly'));
    return;
  }
  if (!props.running) {
    status.value = 'error';
    error.value = t('terminalNotRunning');
    writeNotice(t('terminalNotRunning'));
    return;
  }
  const argv = parseCommand(command.value);
  if (!argv.length) return;

  detach();
  status.value = 'starting';
  error.value = '';
  term.reset();
  term.focus();

  sessionId = newSessionId();
  registerExecStream(sessionId, handleExecEvent);
  try {
    await api.startExec(props.connectionId, props.containerId, sessionId, argv, term.cols || 80, term.rows || 24);
    status.value = 'running';
    syncSize();
  } catch (cause: any) {
    unregisterStream(sessionId);
    sessionId = '';
    status.value = 'error';
    error.value = cause?.message || String(cause);
    writeNotice(error.value);
  }
}

function detach() {
  if (!sessionId) return;
  const closing = sessionId;
  sessionId = '';
  unregisterStream(closing);
  void api.stopExec(props.connectionId, closing).catch(() => {
    // 会话可能已由后端结束，忽略。
  });
}

onMounted(() => {
  if (!host.value) return;
  term = new Terminal({
    convertEol: false,
    cursorBlink: true,
    fontSize: 12,
    scrollback: 5000,
    theme: { background: '#00000000' },
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  term.open(host.value);
  dataDisposable = term.onData((data) => {
    if (status.value !== 'running') return;
    void sendExecInput(sessionId, data).catch((cause: any) => {
      status.value = 'error';
      error.value = cause?.message || String(cause);
    });
  });
  resizeDisposable = term.onResize(() => syncSize());
  resizeObserver = new ResizeObserver(() => {
    if (fitTimer) window.clearTimeout(fitTimer);
    fitTimer = window.setTimeout(syncSize, 80);
  });
  resizeObserver.observe(host.value);
  void attach();
});

onUnmounted(() => {
  if (fitTimer) window.clearTimeout(fitTimer);
  resizeObserver?.disconnect();
  dataDisposable?.dispose();
  resizeDisposable?.dispose();
  detach();
  term?.dispose();
  term = undefined;
});
</script>

<template>
  <div class="terminal-pane">
    <div class="terminal-toolbar">
      <input
        v-model="command"
        class="input terminal-command"
        :placeholder="t('terminalCommandPlaceholder')"
        :disabled="readOnly"
        @keydown.enter.prevent="attach"
      />
      <button class="btn btn-outline btn-sm" :disabled="readOnly || status === 'starting'" @click="attach">
        {{ status === 'idle' ? t('terminalOpen') : t('terminalReconnect') }}
      </button>
      <span class="terminal-status" :class="`terminal-status-${status}`">{{ t(`terminalStatus.${status}`) }}</span>
      <span class="terminal-hint">{{ t('terminalHint') }}</span>
    </div>
    <div v-if="error" class="error-text terminal-error">{{ error }}</div>
    <div ref="host" class="terminal-host" />
  </div>
</template>

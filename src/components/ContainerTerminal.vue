<script setup lang="ts">
// 容器内交互式终端：xterm.js + 后端 docker exec(Tty) 双向 binary channel。
// 输入经 binary channel docker-exec 上行，输出经同一 channel 下行。
// 主题直接读 DBX 注入的 --color-* token，随宿主明暗/配色切换实时更新。
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { Terminal, type ITheme } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { t } from '../i18n';
import { toast } from '../toast';
import { newSessionId } from '../pullTask';
import { registerExecStream, unregisterStream, type ExecEvent } from '../streams';
import { sendExecInput } from '../exec';
import * as api from '../api';
import Icon from './Icon.vue';

const props = defineProps<{
  connectionId: string;
  containerId: string;
  containerName: string;
  readOnly: boolean;
  running: boolean;
}>();

const host = ref<HTMLDivElement>();
const command = ref('/bin/sh');
const status = ref<'idle' | 'starting' | 'running' | 'closed' | 'error'>('idle');
const error = ref('');
const shellPresets = ['/bin/sh', '/bin/bash', '/bin/ash', '/bin/zsh'];

let term: Terminal | undefined;
let fit: FitAddon | undefined;
let sessionId = '';
let resizeObserver: ResizeObserver | undefined;
let themeObserver: MutationObserver | undefined;
let dataDisposable: { dispose: () => void } | undefined;
let resizeDisposable: { dispose: () => void } | undefined;
let fitTimer: number | undefined;

const statusClass = computed(() => `terminal-status-${status.value}`);
const statusLabel = computed(() => t(`terminalStatus.${status.value}`));

function readToken(name: string, fallback: string): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  // 自定义属性可能仍是未解析的 var() 链，此时退回兜底色。
  return raw && !raw.includes('var(') ? raw : fallback;
}

const ANSI_DARK = {
  black: '#3f3f46', red: '#f87171', green: '#4ade80', yellow: '#fbbf24',
  blue: '#60a5fa', magenta: '#c084fc', cyan: '#22d3ee', white: '#e4e4e7',
  brightBlack: '#71717a', brightRed: '#fca5a5', brightGreen: '#86efac', brightYellow: '#fcd34d',
  brightBlue: '#93c5fd', brightMagenta: '#d8b4fe', brightCyan: '#67e8f9', brightWhite: '#fafafa',
} as const;

const ANSI_LIGHT = {
  black: '#18181b', red: '#dc2626', green: '#16a34a', yellow: '#ca8a04',
  blue: '#2563eb', magenta: '#9333ea', cyan: '#0891b2', white: '#a1a1aa',
  brightBlack: '#52525b', brightRed: '#ef4444', brightGreen: '#22c55e', brightYellow: '#eab308',
  brightBlue: '#3b82f6', brightMagenta: '#a855f7', brightCyan: '#06b6d4', brightWhite: '#f4f4f5',
} as const;

function buildTheme(): ITheme {
  const appearance = document.documentElement.dataset.dbxTheme;
  const dark = appearance ? appearance === 'dark' : !window.matchMedia('(prefers-color-scheme: dark)').matches;
  const palette = dark ? ANSI_DARK : ANSI_LIGHT;
  const background = readToken('--background', dark ? '#18181b' : '#ffffff');
  const foreground = readToken('--foreground', dark ? '#e4e4e7' : '#18181b');
  const accent = readToken('--primary', '#2563eb');
  return {
    ...palette,
    background,
    foreground,
    cursor: foreground,
    cursorAccent: background,
    selectionBackground: accent,
    selectionForeground: undefined,
  };
}

function applyTheme() {
  if (!term) return;
  term.options.theme = buildTheme();
  term.options.fontFamily = readToken('--mono-font', 'Consolas, "Cascadia Mono", Monaco, monospace');
  term.options.fontSize = Number.parseInt(readToken('--dbx-plugin-terminal-font-size', '13'), 10) || 13;
}

function parseCommand(raw: string): string[] {
  return raw.trim().split(/\s+/).filter(Boolean);
}

function writeNotice(text: string, kind: 'info' | 'error' = 'info') {
  const color = kind === 'error' ? '\x1b[31m' : '\x1b[90m';
  term?.write(`\r\n${color}${text}\x1b[0m\r\n`);
}

function syncSize() {
  if (!fit || !term || status.value !== 'running' || !sessionId) return;
  try {
    fit.fit();
  } catch {
    // 终端尚未完成布局，下一次 resize 会重试。
    return;
  }
  // 尺寸经 term.onResize 统一上报，这里只负责按容器尺寸重新排版。
}

async function pushResize(cols: number, rows: number) {
  if (status.value !== 'running' || !sessionId) return;
  try {
    await api.execResize(props.connectionId, sessionId, cols, rows);
  } catch {
    // 会话可能刚好结束，忽略。
  }
}

function handleExecEvent(event: ExecEvent) {
  if (event.chunk) term?.write(event.chunk);
  if (!event.done) return;
  status.value = event.error ? 'error' : 'closed';
  if (event.error) error.value = event.error;
  writeNotice(t('terminalClosed', { code: event.exitCode ?? 0 }), event.error ? 'error' : 'info');
  writeNotice(t('terminalPressEnter'), 'info');
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

async function attach() {
  if (!term) return;
  if (props.readOnly) {
    status.value = 'error';
    error.value = t('terminalReadOnly');
    writeNotice(t('terminalReadOnly'), 'error');
    return;
  }
  if (!props.running) {
    status.value = 'error';
    error.value = t('terminalNotRunning');
    writeNotice(t('terminalNotRunning'), 'error');
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
    await pushResize(term.cols, term.rows);
  } catch (cause: any) {
    unregisterStream(sessionId);
    sessionId = '';
    status.value = 'error';
    error.value = cause?.message || String(cause);
    writeNotice(error.value, 'error');
    writeNotice(t('terminalPressEnter'), 'info');
  }
}

function clearScreen() {
  term?.clear();
  term?.focus();
}

onMounted(() => {
  if (!host.value) return;
  term = new Terminal({
    cursorBlink: true,
    cursorStyle: 'bar',
    scrollback: 10000,
    fontSize: 13,
    lineHeight: 1.15,
    allowTransparency: false,
    convertEol: false,
    macOptionIsMeta: true,
    rightClickSelectsWord: true,
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  applyTheme();
  term.open(host.value);
  applyTheme();

  // Ctrl+Shift+C 复制选中内容；普通 Ctrl+C 仍作为 SIGINT 发给容器。
  term.attachCustomKeyEventHandler((event) => {
    if (event.type !== 'keydown' || !event.ctrlKey || !event.shiftKey) return true;
    if (event.key.toLowerCase() !== 'c') return true;
    const selection = term?.getSelection();
    if (!selection) return true;
    void navigator.clipboard
      .writeText(selection)
      .then(() => toast(t('copied'), 1500))
      .catch(() => toast(t('copyFailed'), 2400));
    return false;
  });

  dataDisposable = term.onData((data) => {
    if (status.value !== 'running') {
      // 会话结束后按回车直接重连，贴近 SSH 客户端的习惯。
      if (data === '\r') void attach();
      return;
    }
    void sendExecInput(sessionId, data).catch((cause: any) => {
      status.value = 'error';
      error.value = cause?.message || String(cause);
    });
  });
  resizeDisposable = term.onResize(({ cols, rows }) => void pushResize(cols, rows));

  resizeObserver = new ResizeObserver(() => {
    if (fitTimer) window.clearTimeout(fitTimer);
    fitTimer = window.setTimeout(syncSize, 80);
  });
  resizeObserver.observe(host.value);

  // 宿主切换明暗或配色时会改写 :root 上的属性/内联 token，这里跟随更新。
  themeObserver = new MutationObserver(() => applyTheme());
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-dbx-theme', 'style', 'class'] });

  void attach();
});

onUnmounted(() => {
  if (fitTimer) window.clearTimeout(fitTimer);
  resizeObserver?.disconnect();
  themeObserver?.disconnect();
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
      <Icon name="square" class="terminal-prompt-icon" />
      <span class="terminal-target truncate" :title="containerName">{{ containerName }}</span>
      <input
        v-model="command"
        class="input terminal-command"
        :placeholder="t('terminalCommandPlaceholder')"
        :disabled="readOnly"
        @keydown.enter.prevent="attach"
      />
      <div class="terminal-presets">
        <button
          v-for="preset in shellPresets"
          :key="preset"
          class="terminal-preset"
          :class="{ active: command === preset }"
          :disabled="readOnly"
          @click="command = preset; attach()"
        >
          {{ preset.replace('/bin/', '') }}
        </button>
      </div>
      <button class="btn btn-outline btn-sm" :disabled="readOnly || status === 'starting'" @click="attach">
        <Icon v-if="status === 'starting'" name="loader-circle" class="spin" />
        {{ status === 'idle' ? t('terminalOpen') : t('terminalReconnect') }}
      </button>
      <button class="btn btn-ghost btn-sm" @click="clearScreen">{{ t('clear') }}</button>
      <span class="terminal-status" :class="statusClass"><i class="terminal-status-dot" />{{ statusLabel }}</span>
    </div>
    <div v-if="error" class="error-text terminal-error">{{ error }}</div>
    <div class="terminal-stage">
      <div ref="host" class="terminal-host" />
      <div v-if="status === 'starting'" class="terminal-overlay">
        <Icon name="loader-circle" class="spin" />
        <span>{{ t('terminalConnecting') }}</span>
      </div>
    </div>
    <div class="terminal-footer">{{ t('terminalHint') }}</div>
  </div>
</template>

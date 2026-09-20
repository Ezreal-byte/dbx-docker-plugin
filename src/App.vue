<script setup lang="ts">
// 主工作台：逐区块复刻 dbx docker 分支 DockerWorkbench.vue。
// 差异：api.docker* → 本插件 bridge.invoke('docker/*')；Tauri 事件流 → binary channel；
// ui/* 组件 → 本地组件 + .docker-* CSS（style.css）；read_only/is_production 从后端快照获取。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { currentLocale, getPlugin, type PluginEnvPayload } from './bridge';
import { setLocale, t } from './i18n';
import * as api from './api';
import { initStreams, registerExportStream, registerTransferStream, startLogStream, unregisterStream, type ExportProgress } from './streams';
import { createDockerProgressParseState, parseDockerProgressEvent, type DockerProgressParseState } from './progress';
import { createPendingDockerPullTask, newSessionId } from './pullTask';
import { copyToClipboard, directoryOf, formatBytes, formatDate, savedPathKind, shortId } from './format';
import { toast, toasts } from './toast';
import type {
  DockerComposeApplyRequest,
  DockerContainer,
  DockerContainerAction,
  DockerContainerStats,
  DockerCreateContainerRequest,
  DockerCreateNetworkRequest,
  DockerCreateVolumeRequest,
  DockerDiskUsage,
  DockerDiskUsageCategory,
  DockerEngineDetails,
  DockerFileEntry,
  DockerFilePreview,
  DockerImage,
  DockerImageLayer,
  DockerNetwork,
  DockerPruneTarget,
  DockerRegistryAuth,
  DockerStreamHandle,
  DockerTransferProgress,
  DockerVolume,
} from './types';
import Icon from './components/Icon.vue';
import Dialog from './components/Dialog.vue';
import Popover from './components/Popover.vue';
import Switch from './components/Switch.vue';
import LineChart from './components/LineChart.vue';
import JsonTree from './components/JsonTree.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';
import ContainerTerminal from './components/ContainerTerminal.vue';

type ResourceKind = 'containers' | 'images' | 'volumes' | 'networks';
type ContainerFilter = 'all' | 'running' | 'stopped';
type DetailTab = 'overview' | 'logs' | 'monitoring' | 'files' | 'terminal';
type TrendPoint = DockerContainerStats;
type SortDirection = 'asc' | 'desc';

const resource = ref<ResourceKind>('containers');
const filter = ref<ContainerFilter>('all');
const loading = ref(false);
const error = ref('');
const query = ref('');
const connectionId = ref('');
const isReadOnly = ref(false);
const isProduction = ref(false);
const engineInfo = ref<api.ConnectionInfoResult['info']>();
const engineDetails = ref<DockerEngineDetails>();
const engineDetailsLoading = ref(false);
const engineJsonOpen = ref(false);
const engineSummaryOpen = ref(false);
const engineJsonSearch = ref('');
const containers = ref<DockerContainer[]>([]);
const images = ref<DockerImage[]>([]);
const volumes = ref<DockerVolume[]>([]);
const networks = ref<DockerNetwork[]>([]);
const listStats = ref<Record<string, DockerContainerStats>>({});
const expandedProjects = ref(new Set<string>());
const selectedContainerId = ref('');
const detailTab = ref<DetailTab>('overview');
const inspect = ref<Record<string, any>>({});
const trend = ref<TrendPoint[]>([]);
const actionInFlight = ref<Record<string, string | undefined>>({});
const imageActionInFlight = ref<Record<string, string | undefined>>({});
type TransferTask = DockerTransferProgress & {
  startedAt: number;
  handle?: DockerStreamHandle;
  savedPath?: string;
  savedPathKind?: 'desktop' | 'web';
};
const transfers = ref<TransferTask[]>([]);
const cancelledTransferIds = new Set<string>();
const transferParseStates = new Map<string, DockerProgressParseState>();
const transferOpen = ref(false);
const pushImageOpen = ref(false);
const pushDraft = ref({ sourceImageId: '', targetReference: '', serverAddress: '', username: '', password: '' });
const autoRefresh = ref(true);
const refreshCountdown = ref(10);
const lastRefreshAt = ref<Date>();
const refreshInFlight = ref(false);
const columnWidths = ref<Record<ResourceKind, number[]>>({
  containers: [230, 110, 190, 150, 80, 160, 95, 260],
  images: [280, 140, 110, 180, 260],
  volumes: [220, 140, 140, 380],
  networks: [220, 150, 120, 120, 100, 100],
});
const sortState = ref<{ key: string; direction: SortDirection }>({ key: 'name', direction: 'asc' });
const dangerOpen = ref(false);
const dangerMessage = ref('');
let dangerResolve: ((confirmed: boolean) => void) | undefined;

const createContainerOpen = ref(false);
const createMode = ref<'form' | 'compose'>('form');
const composeEditingProject = ref('');
const composeDraft = ref({
  projectName: '',
  content: `services:
  app:
    image: nginx:latest
    ports:
      - "8080:80"
`,
});
const pullImageOpen = ref(false);
const createVolumeOpen = ref(false);
const createNetworkOpen = ref(false);
const submitting = ref(false);
const pulling = ref(false);
const pullProgress = ref('');
const createContainerDraft = ref({
  name: '',
  image: '',
  command: '',
  environment: '',
  ports: '',
  mounts: '',
  network: '',
  restartPolicy: 'no',
  start: true,
});
const pullDraft = ref({ image: '', serverAddress: '', username: '', password: '' });
const volumeDraft = ref({ name: '', driver: 'local', labels: '', driverOptions: '' });
const networkDraft = ref({ name: '', driver: 'bridge', internal: false, attachable: false, subnet: '', gateway: '' });

// ---------- 磁盘占用与清理 ----------
const diskUsageOpen = ref(false);
const diskUsage = ref<DockerDiskUsage>();
const diskUsageLoading = ref(false);
const diskUsageError = ref('');
const pruneInFlight = ref<DockerPruneTarget | ''>('');

// ---------- 重命名 / 打标签 / 分层历史 ----------
const renameOpen = ref(false);
const renameDraft = ref({ containerId: '', name: '' });
const renameSubmitting = ref(false);
const tagOpen = ref(false);
const tagDraft = ref({ imageId: '', reference: '', repository: '', tag: 'latest' });
const tagSubmitting = ref(false);
const historyOpen = ref(false);
const historyImage = ref('');
const historyLayers = ref<DockerImageLayer[]>([]);
const historyLoading = ref(false);
const historyError = ref('');

const logText = ref('');
const pendingLogText = ref('');
const logPaused = ref(false);
const logSearch = ref('');
const logStream = ref<DockerStreamHandle>();
const pullStream = ref<DockerStreamHandle>();
const logError = ref('');
const logAutoFollow = ref(true);
const logOutput = ref<HTMLPreElement>();
const filePath = ref('/');
const fileEntries = ref<DockerFileEntry[]>([]);
const filePreview = ref<DockerFilePreview>();
const fileLoading = ref(false);
const fileError = ref('');
let listStatsTimer: number | undefined;
let detailStatsTimer: number | undefined;
let resourceRefreshTimer: number | undefined;

const selectedContainer = computed(() => containers.value.find((container) => container.id === selectedContainerId.value));
const normalizedQuery = computed(() => query.value.trim().toLowerCase());

function containerName(container: DockerContainer): string {
  return container.labels['com.docker.compose.container-number'] && container.labels['com.docker.compose.service']
    ? container.labels['com.docker.compose.service']
    : container.names[0]?.replace(/^\//, '') || container.id.slice(0, 12);
}

function isRunning(container: DockerContainer): boolean {
  return container.state.toLowerCase() === 'running';
}

function isPaused(container: DockerContainer): boolean {
  return container.state.toLowerCase() === 'paused';
}

// 终端标签只在容器运行时出现：docker exec 对已停止容器没有意义。
const detailTabs = computed<DetailTab[]>(() => {
  const tabs: DetailTab[] = ['overview', 'logs', 'monitoring', 'files'];
  if (selectedContainer.value && isRunning(selectedContainer.value)) tabs.push('terminal');
  return tabs;
});

const terminalTabRequested = ref(false);

async function openTerminalTab() {
  const container = selectedContainer.value;
  if (!container || isReadOnly.value || !isRunning(container)) return;
  if (terminalTabRequested.value) {
    detailTab.value = 'terminal';
    return;
  }
  if (isProduction.value && !(await requestConfirmation(t('confirmTerminal', { name: containerName(container) })))) return;
  terminalTabRequested.value = true;
  detailTab.value = 'terminal';
}

// ---------- 磁盘占用与清理 ----------

async function loadDiskUsage() {
  diskUsageLoading.value = true;
  diskUsageError.value = '';
  try {
    diskUsage.value = await api.getDiskUsage(connectionId.value);
  } catch (cause: any) {
    diskUsageError.value = cause?.message || String(cause);
  } finally {
    diskUsageLoading.value = false;
  }
}

function openDiskUsage() {
  diskUsageOpen.value = true;
  void loadDiskUsage();
}

async function runPrune(target: DockerPruneTarget, all: boolean, actionLabel: string) {
  if (isReadOnly.value) {
    toast(t('readOnly'), 2400);
    return;
  }
  if (!(await requestConfirmation(t('confirmPrune', { action: actionLabel })))) return;
  pruneInFlight.value = target;
  try {
    const result = await api.prune(connectionId.value, target, all);
    const count = result.deleted?.length ?? 0;
    if (!count && !result.spaceReclaimed) toast(t('pruneNothing'), 2400);
    else toast(t('pruneDone', { count, size: formatBytes(result.spaceReclaimed || 0) }), 3600);
    await loadDiskUsage();
    await loadResource();
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    pruneInFlight.value = '';
  }
}

// ---------- 容器重命名 ----------

function openRename(container: DockerContainer) {
  if (isReadOnly.value) {
    toast(t('readOnly'), 2400);
    return;
  }
  renameDraft.value = { containerId: container.id, name: containerName(container) };
  renameOpen.value = true;
}

async function submitRename() {
  const name = renameDraft.value.name.trim();
  if (!name || renameSubmitting.value) return;
  renameSubmitting.value = true;
  try {
    await api.renameContainer(connectionId.value, renameDraft.value.containerId, name);
    toast(t('containerRenamed', { name }), 2400);
    renameOpen.value = false;
    await loadContainers();
    if (selectedContainerId.value === renameDraft.value.containerId) {
      inspect.value = (await api.inspectContainer(connectionId.value, renameDraft.value.containerId)) as Record<string, any>;
    }
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    renameSubmitting.value = false;
  }
}

// ---------- 镜像打标签 / 分层历史 ----------

function openTagImage(item: DockerImage) {
  if (isReadOnly.value) {
    toast(t('readOnly'), 2400);
    return;
  }
  tagDraft.value = {
    imageId: item.id,
    reference: item.repoTags.find((tag) => tag && tag !== '<none>:<none>') || shortId(item.id),
    repository: '',
    tag: 'latest',
  };
  tagOpen.value = true;
}

async function submitTagImage() {
  const repository = tagDraft.value.repository.trim();
  if (!repository || tagSubmitting.value) return;
  tagSubmitting.value = true;
  try {
    const result = await api.tagImage(
      connectionId.value,
      tagDraft.value.imageId,
      repository,
      tagDraft.value.tag.trim() || 'latest',
    );
    toast(t('imageTagged', { reference: result.reference }), 3000);
    tagOpen.value = false;
    await loadResource('images');
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    tagSubmitting.value = false;
  }
}

const tagDraftTags = computed(() => {
  const image = images.value.find((item) => item.id === tagDraft.value.imageId);
  return (image?.repoTags ?? []).filter((tag) => tag && tag !== '<none>:<none>');
});

async function removeImageTag(reference: string) {
  if (isReadOnly.value) {
    toast(t('readOnly'), 2400);
    return;
  }
  if (!(await requestConfirmation(t('confirmRemoveTag', { reference })))) return;
  try {
    await api.untagImage(connectionId.value, reference);
    toast(t('tagRemoved', { reference }), 3000);
    await loadResource('images');
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  }
}

async function openImageHistory(item: DockerImage) {
  const tagged = item.repoTags.find((tag) => tag && tag !== '<none>:<none>');
  historyImage.value = tagged || shortId(item.id);
  historyLayers.value = [];
  historyError.value = '';
  historyOpen.value = true;
  historyLoading.value = true;
  try {
    historyLayers.value = await api.imageHistory(connectionId.value, item.id);
  } catch (cause: any) {
    historyError.value = cause?.message || String(cause);
  } finally {
    historyLoading.value = false;
  }
}

function formatPorts(container: DockerContainer): string {
  return container.ports.map((port) => `${port.ip || ''}${port.publicPort ? `:${port.publicPort}→` : ''}${port.privatePort}/${port.portType}`).join(', ') || '—';
}

function toggleSort(key: string) {
  sortState.value = sortState.value.key === key ? { key, direction: sortState.value.direction === 'asc' ? 'desc' : 'asc' } : { key, direction: 'asc' };
}

function sortedBy<T>(values: T[], getter: (value: T, key: string) => string | number | boolean): T[] {
  const { key, direction } = sortState.value;
  const factor = direction === 'asc' ? 1 : -1;
  return [...values].sort((left, right) => {
    const a = getter(left, key);
    const b = getter(right, key);
    if (typeof a === 'number' && typeof b === 'number') return (a - b) * factor;
    return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' }) * factor;
  });
}

function containerSortValue(container: DockerContainer, key: string): string | number {
  if (key === 'name') return containerName(container);
  if (key === 'id') return container.id;
  if (key === 'image') return container.image;
  if (key === 'status') return isRunning(container) ? 2 : isPaused(container) ? 1 : 0;
  if (key === 'ports') return formatPorts(container);
  if (key === 'cpu') return listStats.value[container.id]?.cpuPercent ?? -1;
  if (key === 'memory') return listStats.value[container.id]?.memoryUsage ?? -1;
  return '';
}

async function copyValue(value: string) {
  await copyToClipboard(value);
  toast(t('copied'), 1400);
}

function containerStatusLabel(container: DockerContainer): string {
  if (isRunning(container)) return t('running');
  if (isPaused(container)) return t('paused');
  return t('stopped');
}

const matchingContainers = computed(() =>
  sortedBy(
    containers.value.filter((container) => {
      if (filter.value === 'running' && !isRunning(container) && !isPaused(container)) return false;
      if (filter.value === 'stopped' && (isRunning(container) || isPaused(container))) return false;
      if (!normalizedQuery.value) return true;
      return [container.id, container.image, container.state, container.status, ...container.names, ...Object.values(container.labels)]
        .join(' ')
        .toLowerCase()
        .includes(normalizedQuery.value);
    }),
    containerSortValue,
  ),
);

const composeGroups = computed(() => {
  const groups = new Map<string, DockerContainer[]>();
  for (const container of matchingContainers.value) {
    const project = container.labels['com.docker.compose.project'];
    if (!project) continue;
    const values = groups.get(project) ?? [];
    values.push(container);
    groups.set(project, values);
  }
  return [...groups.entries()].sort(([left], [right]) => left.localeCompare(right));
});

const standaloneContainers = computed(() => matchingContainers.value.filter((container) => !container.labels['com.docker.compose.project']));

const filteredImages = computed(() =>
  sortedBy(
    images.value.filter((item) =>
      !normalizedQuery.value ? true : [item.id, ...item.repoTags, ...item.repoDigests].join(' ').toLowerCase().includes(normalizedQuery.value),
    ),
    (item, key) => {
      if (key === 'name') return item.repoTags.join(',');
      if (key === 'id') return item.id;
      if (key === 'size') return item.size;
      if (key === 'created') return item.created;
      return '';
    },
  ),
);
const filteredVolumes = computed(() =>
  sortedBy(
    volumes.value.filter((item) => !normalizedQuery.value || [item.name, item.driver, item.mountpoint].join(' ').toLowerCase().includes(normalizedQuery.value)),
    (item, key) => String((item as any)[key] ?? ''),
  ),
);
const filteredNetworks = computed(() =>
  sortedBy(
    networks.value.filter((item) => !normalizedQuery.value || [item.id, item.name, item.driver].join(' ').toLowerCase().includes(normalizedQuery.value)),
    (item, key) => String((item as any)[key] ?? ''),
  ),
);
const visibleLogs = computed(() => {
  if (!logSearch.value.trim()) return logText.value;
  const needle = logSearch.value.toLowerCase();
  return logText.value
    .split('\n')
    .filter((line) => line.toLowerCase().includes(needle))
    .join('\n');
});
const trendLabels = computed(() => trend.value.map((point) => new Date(point.readAt || Date.now()).toLocaleTimeString()));
const cpuSeries = computed(() => [{ name: 'CPU', data: trend.value.map((point) => point.cpuPercent), color: '#3b82f6' }]);
const memorySeries = computed(() => [{ name: t('memory'), data: trend.value.map((point) => point.memoryPercent), color: '#8b5cf6' }]);
const engineJson = computed(() => ({ version: engineDetails.value?.version ?? {}, info: engineDetails.value?.info ?? {} }));
const filteredEngineJson = computed(() => {
  const text = JSON.stringify(engineJson.value, null, 2);
  const needle = engineJsonSearch.value.trim().toLowerCase();
  return needle
    ? text
        .split('\n')
        .filter((line) => line.toLowerCase().includes(needle))
        .join('\n')
    : text;
});
const runningTransfers = computed(() => transfers.value.filter((task) => task.status === 'running').length);

interface DiskUsageRow {
  key: 'images' | 'containers' | 'volumes' | 'networks' | 'buildCache';
  count: number;
  size: number;
  reclaimable: number;
  target?: DockerPruneTarget;
  allTarget?: DockerPruneTarget;
  pruneLabel: string;
}

const diskUsageRows = computed<DiskUsageRow[]>(() => {
  const usage = diskUsage.value;
  if (!usage) return [];
  const category = (value: DockerDiskUsageCategory | undefined): DockerDiskUsageCategory =>
    value ?? { count: 0, size: 0, reclaimable: 0 };
  return [
    { key: 'images', ...category(usage.images), target: 'images', allTarget: 'images', pruneLabel: t('pruneImages') },
    { key: 'containers', ...category(usage.containers), target: 'containers', pruneLabel: t('pruneContainers') },
    { key: 'volumes', ...category(usage.volumes), target: 'volumes', pruneLabel: t('pruneVolumes') },
    { key: 'networks', ...category(usage.networks), target: 'networks', pruneLabel: t('pruneNetworks') },
    { key: 'buildCache', ...category(usage.buildCache), pruneLabel: '' },
  ];
});

function tableStyle(kind: ResourceKind) {
  return { minWidth: `${columnWidths.value[kind].reduce((sum, width) => sum + width, 0)}px` };
}

function handleHeaderPointer(event: PointerEvent, kind: ResourceKind) {
  if (event.button !== 0) return;
  const header = (event.target as HTMLElement).closest('th');
  if (!(header instanceof HTMLTableCellElement)) return;
  const bounds = header.getBoundingClientRect();
  if (bounds.right - event.clientX > 8) return;
  const index = header.cellIndex;
  const initialWidth = columnWidths.value[kind][index];
  if (initialWidth == null) return;
  const initialX = event.clientX;
  event.preventDefault();
  event.stopPropagation();
  const move = (moveEvent: PointerEvent) => {
    const next = [...columnWidths.value[kind]];
    next[index] = Math.max(64, initialWidth + moveEvent.clientX - initialX);
    columnWidths.value = { ...columnWidths.value, [kind]: next };
  };
  const stop = () => {
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', stop);
  };
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', stop, { once: true });
}

function upsertTransfer(progress: DockerTransferProgress, handle?: DockerStreamHandle) {
  const index = transfers.value.findIndex((task) => task.sessionId === progress.sessionId);
  if (index >= 0) {
    const next = [...transfers.value];
    next[index] = { ...next[index], ...progress, handle: handle ?? next[index].handle };
    transfers.value = next;
  } else {
    transfers.value = [{ ...progress, startedAt: Date.now(), handle }, ...transfers.value].slice(0, 50);
  }
}

// 记录下载落到磁盘的真实路径：桌面宿主返回绝对路径，Web 宿主只返回文件名。
function setTransferSavedPath(sessionId: string, savedPath: string, hostKind: 'desktop' | 'web') {
  transfers.value = transfers.value.map((task) =>
    task.sessionId === sessionId ? { ...task, savedPath, savedPathKind: hostKind } : task,
  );
}

async function copySavedPath(task: TransferTask) {
  if (!task.savedPath) return;
  await copyToClipboard(task.savedPath);
  toast(t('pathCopied'), 1800);
}

function transferPercent(task: TransferTask): number | undefined {
  if (task.bytesTotal && task.bytesTotal > 0) return Math.min(100, (task.bytesCompleted / task.bytesTotal) * 100);
  if (task.layersTotal && task.layersTotal > 0 && task.layersCompleted != null) {
    return Math.min(100, (task.layersCompleted / task.layersTotal) * 100);
  }
  return undefined;
}

function dockerProgressFromChunk(
  sessionId: string,
  kind: 'pull' | 'push',
  image: string,
  chunk: string,
  done: boolean,
  error?: string | null,
): DockerTransferProgress {
  const current = transfers.value.find((task) => task.sessionId === sessionId);
  const state = transferParseStates.get(sessionId) ?? createDockerProgressParseState();
  transferParseStates.set(sessionId, state);
  const result = parseDockerProgressEvent(state, {
    sessionId,
    kind,
    image,
    chunk,
    done,
    error,
    cancelled: cancelledTransferIds.has(sessionId),
    current,
  });
  if (result.status !== 'running') {
    transferParseStates.delete(sessionId);
  }
  return result;
}

async function loadEngineInfo() {
  try {
    const result = await api.getConnectionInfo(connectionId.value);
    engineInfo.value = result.info;
    isReadOnly.value = result.readOnly;
    isProduction.value = result.isProduction;
  } catch (cause: any) {
    error.value = cause?.message || String(cause);
  }
}

async function loadEngineDetails(target: 'json' | 'summary') {
  if (target === 'json') engineJsonOpen.value = true;
  else engineSummaryOpen.value = true;
  if (engineDetails.value || engineDetailsLoading.value) return;
  engineDetailsLoading.value = true;
  try {
    engineDetails.value = await api.getEngineDetails(connectionId.value);
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    engineDetailsLoading.value = false;
  }
}

async function loadContainers() {
  containers.value = await api.listContainers(connectionId.value, true);
  for (const [project] of composeGroups.value) expandedProjects.value.add(project);
}

async function loadResource(kind = resource.value) {
  if (refreshInFlight.value) return;
  refreshInFlight.value = true;
  loading.value = true;
  error.value = '';
  try {
    if (kind === 'containers') await loadContainers();
    if (kind === 'images') images.value = await api.listImages(connectionId.value);
    if (kind === 'volumes') volumes.value = await api.listVolumes(connectionId.value);
    if (kind === 'networks') networks.value = await api.listNetworks(connectionId.value);
    if (kind === 'containers') {
      lastRefreshAt.value = new Date();
      refreshCountdown.value = 10;
    }
  } catch (cause: any) {
    error.value = cause?.message || String(cause);
  } finally {
    loading.value = false;
    refreshInFlight.value = false;
  }
}

async function selectResource(kind: ResourceKind) {
  await closeDetail();
  resource.value = kind;
  query.value = '';
  sortState.value = { key: 'name', direction: 'asc' };
  await loadResource(kind);
}

function toggleProject(project: string) {
  const next = new Set(expandedProjects.value);
  if (next.has(project)) next.delete(project);
  else next.add(project);
  expandedProjects.value = next;
}

async function openDetail(container: DockerContainer) {
  selectedContainerId.value = container.id;
  detailTab.value = 'overview';
  terminalTabRequested.value = false;
  inspect.value = (await api.inspectContainer(connectionId.value, container.id)) as Record<string, any>;
  trend.value = [];
  restartDetailSampling();
}

async function closeDetail() {
  stopDetailSampling();
  await stopLogs();
  selectedContainerId.value = '';
  detailTab.value = 'overview';
  terminalTabRequested.value = false;
  inspect.value = {};
  fileEntries.value = [];
  filePreview.value = undefined;
}

function requestConfirmation(message: string): Promise<boolean> {
  dangerMessage.value = message;
  dangerOpen.value = true;
  return new Promise((resolve) => {
    dangerResolve = resolve;
  });
}

function settleConfirmation(confirmed: boolean) {
  const resolve = dangerResolve;
  dangerResolve = undefined;
  dangerOpen.value = false;
  resolve?.(confirmed);
}

async function confirmAction(container: DockerContainer, action: DockerContainerAction | 'remove'): Promise<boolean> {
  const dangerous = isProduction.value || ['stop', 'restart', 'remove'].includes(action);
  if (!dangerous) return true;
  return requestConfirmation(t('confirmAction', { action: t(`action.${action}`), name: containerName(container) }));
}

async function confirmProductionMutation(action: string): Promise<boolean> {
  return !isProduction.value || requestConfirmation(t('confirmProductionMutation', { action }));
}

async function runAction(container: DockerContainer, action: DockerContainerAction) {
  if (isReadOnly.value || actionInFlight.value[container.id]) {
    if (isReadOnly.value) toast(t('readOnly'), 2400);
    return;
  }
  if (!(await confirmAction(container, action))) return;
  actionInFlight.value = { ...actionInFlight.value, [container.id]: action };
  try {
    await api.containerAction(connectionId.value, container.id, action);
    toast(t('actionSucceeded', { action: t(`action.${action}`), name: containerName(container) }), 2400);
    await loadContainers();
    lastRefreshAt.value = new Date();
    if (selectedContainerId.value === container.id) {
      inspect.value = (await api.inspectContainer(connectionId.value, container.id)) as Record<string, any>;
    }
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    actionInFlight.value = { ...actionInFlight.value, [container.id]: undefined };
  }
}

async function removeContainer(container: DockerContainer) {
  if (isReadOnly.value || actionInFlight.value[container.id]) return;
  if (!(await confirmAction(container, 'remove'))) return;
  actionInFlight.value = { ...actionInFlight.value, [container.id]: 'remove' };
  try {
    await api.removeContainer(connectionId.value, container.id);
    toast(t('containerRemoved', { name: containerName(container) }), 2400);
    await loadContainers();
    lastRefreshAt.value = new Date();
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    actionInFlight.value = { ...actionInFlight.value, [container.id]: undefined };
  }
}

function parseKeyValues(text: string): Record<string, string> {
  return Object.fromEntries(
    text
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)
      .map((line) => {
        const index = line.indexOf('=');
        return index < 0 ? [line, ''] : [line.slice(0, index).trim(), line.slice(index + 1).trim()];
      }),
  );
}

function createContainerRequest(): DockerCreateContainerRequest {
  const ports = createContainerDraft.value.ports
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [mapping, protocol = 'tcp'] = line.split('/');
      const parts = mapping.split(':');
      const containerPort = Number(parts.pop());
      const hostPortText = parts.pop();
      const hostIp = parts.join(':');
      return {
        containerPort,
        protocol: protocol.toLowerCase() === 'udp' ? ('udp' as const) : ('tcp' as const),
        hostIp,
        hostPort: hostPortText ? Number(hostPortText) : undefined,
      };
    });
  const mounts = createContainerDraft.value.mounts
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const parts = line.split(':');
      const readOnly = parts[parts.length - 1] === 'ro';
      if (readOnly) parts.pop();
      const source = parts.shift() || '';
      const target = parts.join(':');
      return { type: source.startsWith('/') || /^[A-Za-z]:[\\/]/.test(source) ? ('bind' as const) : ('volume' as const), source, target, readOnly };
    });
  return {
    name: createContainerDraft.value.name.trim(),
    image: createContainerDraft.value.image.trim(),
    command: createContainerDraft.value.command
      .split(/\r?\n/)
      .map((value) => value.trim())
      .filter(Boolean),
    environment: createContainerDraft.value.environment
      .split(/\r?\n/)
      .map((value) => value.trim())
      .filter(Boolean),
    ports,
    mounts,
    labels: {},
    network: createContainerDraft.value.network || undefined,
    restartPolicy: createContainerDraft.value.restartPolicy as DockerCreateContainerRequest['restartPolicy'],
    start: createContainerDraft.value.start,
  };
}

async function createContainer() {
  if (!(await confirmProductionMutation(t('createContainer')))) return;
  submitting.value = true;
  try {
    await api.createContainer(connectionId.value, createContainerRequest());
    createContainerOpen.value = false;
    toast(t('containerCreated'), 2400);
    await loadContainers();
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    submitting.value = false;
  }
}

async function openCreateContainer() {
  if (!networks.value.length) {
    try {
      networks.value = await api.listNetworks(connectionId.value);
    } catch {
      // 表单仍可用（Docker 默认网络）。
    }
  }
  createMode.value = 'form';
  composeEditingProject.value = '';
  createContainerOpen.value = true;
}

function composePortLines(value: any): string[] {
  const bindings = value?.HostConfig?.PortBindings ?? {};
  return Object.entries(bindings).flatMap(([containerPort, entries]: [string, any]) => {
    const [port, protocol = 'tcp'] = containerPort.split('/');
    if (!Array.isArray(entries) || !entries.length) return [`${port}/${protocol}`];
    return entries.map((entry) => {
      const host = [entry.HostIp, entry.HostPort].filter(Boolean).join(':');
      return `${host ? `${host}:` : ''}${port}/${protocol}`;
    });
  });
}

async function openComposeEditor(project = '') {
  createMode.value = 'compose';
  composeEditingProject.value = project;
  composeDraft.value.projectName = project;
  if (project) {
    const projectContainers = containers.value.filter((container) => container.labels['com.docker.compose.project'] === project);
    const services: Record<string, any> = {};
    for (const container of projectContainers) {
      const value: any = await api.inspectContainer(connectionId.value, container.id);
      const service = container.labels['com.docker.compose.service'] || containerName(container);
      const mounts = (value.Mounts ?? []).map((mount: any) => `${mount.Name || mount.Source}:${mount.Destination}${mount.RW === false ? ':ro' : ''}`);
      const networkNames = Object.keys(value.NetworkSettings?.Networks ?? {}).map((name) => (name.startsWith(`${project}_`) ? name.slice(project.length + 1) : name));
      services[service] = {
        image: value.Config?.Image || container.image,
        container_name: value.Name?.replace(/^\//, '') || containerName(container),
        ...(value.Config?.Cmd?.length ? { command: value.Config.Cmd } : {}),
        ...(value.Config?.Env?.length ? { environment: value.Config.Env } : {}),
        ...(composePortLines(value).length ? { ports: composePortLines(value) } : {}),
        ...(mounts.length ? { volumes: mounts } : {}),
        ...(networkNames.length ? { networks: networkNames } : {}),
        ...(value.HostConfig?.RestartPolicy?.Name && value.HostConfig.RestartPolicy.Name !== 'no' ? { restart: value.HostConfig.RestartPolicy.Name } : {}),
      };
    }
    composeDraft.value.content = JSON.stringify({ services }, null, 2);
  } else {
    composeDraft.value = {
      projectName: '',
      content: `services:
  app:
    image: nginx:latest
    ports:
      - "8080:80"
`,
    };
  }
  createContainerOpen.value = true;
}

async function applyCompose() {
  const editing = !!composeEditingProject.value;
  if (!(await confirmProductionMutation(editing ? t('editCompose') : t('createCompose')))) return;
  if (editing && !(await requestConfirmation(t('confirmComposeReplace', { project: composeEditingProject.value })))) return;
  submitting.value = true;
  try {
    const request: DockerComposeApplyRequest = {
      projectName: composeDraft.value.projectName.trim(),
      content: composeDraft.value.content,
      replaceExisting: editing,
    };
    const result = await api.applyCompose(connectionId.value, request);
    createContainerOpen.value = false;
    toast(t(editing ? 'composeUpdated' : 'composeCreated', { count: result.containerIds.length }), 3000);
    if (result.warnings.length) toast(result.warnings.join('\n'), 5000);
    await loadContainers();
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    submitting.value = false;
  }
}

async function pullImage() {
  if (pulling.value) return;
  if (!(await confirmProductionMutation(t('pullImage')))) return;
  if (pulling.value) return;
  const imageReference = pullDraft.value.image.trim();
  if (!imageReference) return;
  const auth: DockerRegistryAuth | undefined =
    pullDraft.value.serverAddress || pullDraft.value.username || pullDraft.value.password
      ? {
          serverAddress: pullDraft.value.serverAddress,
          username: pullDraft.value.username,
          password: pullDraft.value.password,
        }
      : undefined;
  const pending = createPendingDockerPullTask(imageReference);
  pulling.value = true;
  pullProgress.value = '';
  transferOpen.value = true;
  pullImageOpen.value = false;
  pullDraft.value.password = '';
  const handle = registerTransferStream(pending.sessionId, (event) => {
    if (event.chunk) pullProgress.value = `${pullProgress.value}${event.chunk}`.slice(-20_000);
    const previous = transfers.value.find((task) => task.sessionId === pending.sessionId);
    const progress = dockerProgressFromChunk(pending.sessionId, 'pull', imageReference, event.chunk, event.done, event.error);
    upsertTransfer(progress, pullStream.value);
    if (progress.status === 'error') {
      if (previous?.status !== 'error') toast(progress.error || t('transferFailed'), 5000);
      pulling.value = false;
      pullStream.value = undefined;
      resetPullDraft();
    }
    if (event.done && progress.status === 'done') {
      pulling.value = false;
      pullStream.value = undefined;
      if (cancelledTransferIds.has(pending.sessionId)) return;
      toast(t('imagePulled'), 2400);
      resetPullDraft();
      void loadResource('images');
    }
  });
  pullStream.value = handle;
  upsertTransfer(pending.progress, handle);
  try {
    await api.pullImage(connectionId.value, imageReference, auth, pending.sessionId);
  } catch (cause: any) {
    pulling.value = false;
    pullStream.value = undefined;
    unregisterStream(pending.sessionId);
    if (cancelledTransferIds.has(pending.sessionId)) return;
    const message = cause?.message || String(cause);
    upsertTransfer({ ...pending.progress, status: 'error', error: message }, handle);
    resetPullDraft();
    toast(message, 5000);
  }
}

function resetPullDraft() {
  pullDraft.value = { image: '', serverAddress: '', username: '', password: '' };
  pullProgress.value = '';
}

async function stopImagePull() {
  const stream = pullStream.value;
  pullStream.value = undefined;
  pulling.value = false;
  if (stream) await stream.stop().catch(() => undefined);
}

async function exportImage(item: DockerImage) {
  if (imageActionInFlight.value[item.id]) return;
  imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: 'export' };
  transferOpen.value = true;
  const taggedReference = item.repoTags.find((tag) => tag && tag !== '<none>:<none>');
  const exportReference = taggedReference || item.id;
  const baseName = (taggedReference || shortId(item.id)).replace(/[\\/:*?"<>|]+/g, '_');
  const fileName = `${baseName}.tar`;
  const sessionId = newSessionId();
  try {
    const handle = registerExportStream(sessionId, exportReference, (progress: ExportProgress) => {
      upsertTransfer(progress, handle);
      if (progress.status === 'done') {
        imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: undefined };
        void saveExport(progress, fileName, item.id, sessionId);
      }
      if (progress.status === 'error' || progress.status === 'cancelled') {
        imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: undefined };
        if (progress.status === 'error') toast(progress.error || t('transferFailed'), 5000);
      }
    });
    upsertTransfer(
      { sessionId, kind: 'export', direction: 'download', image: exportReference, status: 'running', bytesCompleted: 0 },
      handle,
    );
    await api.exportImage(connectionId.value, exportReference, sessionId);
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
    imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: undefined };
    unregisterStream(sessionId);
  }
}

async function saveExport(progress: ExportProgress, fileName: string, imageId: string, sessionId: string) {
  try {
    const chunks = progress.dataChunks;
    const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
    const merged = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      merged.set(chunk, offset);
      offset += chunk.length;
    }
    const plugin = getPlugin();
    if (plugin.saveFile) {
      // 桌面宿主会弹原生保存对话框并回传绝对路径；Web 宿主只回传文件名。
      const result = (await plugin.saveFile({ suggestedName: fileName, data: merged.buffer })) as
        | { path?: string }
        | null
        | undefined;
      const savedPath = typeof result?.path === 'string' ? result.path : '';
      if (savedPath) {
        const kind = savedPathKind(savedPath, fileName);
        setTransferSavedPath(sessionId, savedPath, kind);
        toast(kind === 'desktop' ? t('savedTo', { path: directoryOf(savedPath) }) : t('imageExported'), kind === 'desktop' ? 6000 : 2400);
      } else {
        // 用户在原生保存对话框中取消。
        toast(t('imageExported'), 2400);
      }
    } else {
      // 兜底（dev-host 浏览器环境）：走 Blob 下载。
      const blob = new Blob([merged], { type: 'application/octet-stream' });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = fileName;
      anchor.click();
      URL.revokeObjectURL(url);
      setTransferSavedPath(sessionId, fileName, 'web');
      toast(t('imageExported'), 2400);
    }
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    imageActionInFlight.value = { ...imageActionInFlight.value, [imageId]: undefined };
  }
}

function openPushImage(item: DockerImage) {
  pushDraft.value = {
    sourceImageId: item.id,
    targetReference: item.repoTags[0] && item.repoTags[0] !== '<none>:<none>' ? item.repoTags[0] : '',
    serverAddress: '',
    username: '',
    password: '',
  };
  pushImageOpen.value = true;
}

async function pushImage() {
  if (!(await confirmProductionMutation(t('pushImage')))) return;
  const targetReference = pushDraft.value.targetReference.trim();
  if (!targetReference) return;
  transferOpen.value = true;
  pushImageOpen.value = false;
  const auth: DockerRegistryAuth | undefined =
    pushDraft.value.serverAddress || pushDraft.value.username || pushDraft.value.password
      ? {
          serverAddress: pushDraft.value.serverAddress,
          username: pushDraft.value.username,
          password: pushDraft.value.password,
        }
      : undefined;
  pushDraft.value.password = '';
  const sessionId = newSessionId();
  const handle = registerTransferStream(sessionId, (event) => {
    const progress = dockerProgressFromChunk(sessionId, 'push', targetReference, event.chunk, event.done, event.error);
    upsertTransfer(progress, handle);
    if (progress.status === 'done') {
      toast(t('imagePushed'), 2400);
      void loadResource('images');
    } else if (progress.status === 'error') {
      toast(progress.error || t('transferFailed'), 5000);
    }
  });
  upsertTransfer(
    { sessionId, kind: 'push', direction: 'upload', image: targetReference, status: 'running', bytesCompleted: 0 },
    handle,
  );
  try {
    await api.pushImage(connectionId.value, pushDraft.value.sourceImageId, targetReference, auth, sessionId);
  } catch (cause: any) {
    unregisterStream(sessionId);
    upsertTransfer({
      sessionId,
      kind: 'push',
      direction: 'upload',
      image: targetReference,
      status: 'error',
      bytesCompleted: 0,
      error: cause?.message || String(cause),
    });
    toast(cause?.message || String(cause), 5000);
  }
}

async function cancelTransfer(task: TransferTask) {
  cancelledTransferIds.add(task.sessionId);
  await task.handle?.stop().catch(() => undefined);
  upsertTransfer({ ...task, status: 'cancelled' });
  if (task.kind === 'pull') {
    pulling.value = false;
    pullStream.value = undefined;
  }
}

async function cancelActiveTransfers() {
  await Promise.all(transfers.value.filter((task) => task.status === 'running').map(cancelTransfer));
}

async function removeImage(item: DockerImage) {
  if (imageActionInFlight.value[item.id]) return;
  if (!(await requestConfirmation(t('confirmImageRemove', { name: item.repoTags[0] || shortId(item.id) })))) return;
  imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: 'remove' };
  try {
    await api.removeImage(connectionId.value, item.id);
    toast(t('imageRemoved'), 2400);
    await loadResource('images');
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: undefined };
  }
}

async function createVolume() {
  if (!(await confirmProductionMutation(t('createVolume')))) return;
  submitting.value = true;
  try {
    const request: DockerCreateVolumeRequest = {
      name: volumeDraft.value.name,
      driver: volumeDraft.value.driver || 'local',
      labels: parseKeyValues(volumeDraft.value.labels),
      driverOptions: parseKeyValues(volumeDraft.value.driverOptions),
    };
    await api.createVolume(connectionId.value, request);
    createVolumeOpen.value = false;
    toast(t('volumeCreated'), 2400);
    await loadResource('volumes');
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    submitting.value = false;
  }
}

async function createNetwork() {
  if (!(await confirmProductionMutation(t('createNetwork')))) return;
  submitting.value = true;
  try {
    const request: DockerCreateNetworkRequest = {
      name: networkDraft.value.name,
      driver: networkDraft.value.driver || 'bridge',
      internal: networkDraft.value.internal,
      attachable: networkDraft.value.attachable,
      subnet: networkDraft.value.subnet || undefined,
      gateway: networkDraft.value.gateway || undefined,
    };
    await api.createNetwork(connectionId.value, request);
    createNetworkOpen.value = false;
    toast(t('networkCreated'), 2400);
    await loadResource('networks');
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    submitting.value = false;
  }
}

function appendLogs(chunk: string) {
  if (logPaused.value) {
    pendingLogText.value += chunk;
    if (pendingLogText.value.length > 5 * 1024 * 1024) pendingLogText.value = pendingLogText.value.slice(-5 * 1024 * 1024);
    return;
  }
  logText.value += chunk;
  const lines = logText.value.split('\n');
  if (lines.length > 10_000) logText.value = lines.slice(-10_000).join('\n');
  if (logText.value.length > 5 * 1024 * 1024) logText.value = logText.value.slice(-5 * 1024 * 1024);
  if (logAutoFollow.value) void nextTick(scrollLogsToBottom);
}

function scrollLogsToBottom() {
  const output = logOutput.value;
  if (output) output.scrollTop = output.scrollHeight;
}

function handleLogScroll() {
  const output = logOutput.value;
  if (!output) return;
  logAutoFollow.value = output.scrollHeight - output.scrollTop - output.clientHeight < 24;
}

async function startLogs() {
  if (!selectedContainer.value || logStream.value) return;
  logError.value = '';
  logAutoFollow.value = true;
  const sessionId = newSessionId();
  try {
    const handle = startLogStream(sessionId, (event) => {
      if (event.chunk) appendLogs(event.chunk);
      if (event.error) logError.value = event.error;
      if (event.done) logStream.value = undefined;
    });
    await api.startLogs(connectionId.value, selectedContainer.value.id, { tail: 500, timestamps: false }, sessionId);
    logStream.value = handle;
  } catch (cause: any) {
    unregisterStream(sessionId);
    logError.value = cause?.message || String(cause);
  }
}

async function stopLogs() {
  const stream = logStream.value;
  logStream.value = undefined;
  if (stream) await stream.stop().catch(() => undefined);
}

function toggleLogPause() {
  logPaused.value = !logPaused.value;
  if (!logPaused.value && pendingLogText.value) {
    const pending = pendingLogText.value;
    pendingLogText.value = '';
    appendLogs(pending);
  }
}

function clearLogs() {
  logText.value = '';
  pendingLogText.value = '';
  if (logAutoFollow.value) void nextTick(scrollLogsToBottom);
}

async function loadFiles(path = filePath.value) {
  if (!selectedContainer.value) return;
  fileLoading.value = true;
  fileError.value = '';
  filePreview.value = undefined;
  try {
    filePath.value = path;
    fileEntries.value = await api.listContainerFiles(connectionId.value, selectedContainer.value.id, path);
  } catch (cause: any) {
    fileError.value = cause?.message || String(cause);
  } finally {
    fileLoading.value = false;
  }
}

function parentPath(path: string): string {
  if (path === '/') return '/';
  const result = path.replace(/\/+$/, '').replace(/\/[^/]+$/, '');
  return result || '/';
}

async function openFile(entry: DockerFileEntry) {
  if (!selectedContainer.value) return;
  if (entry.kind === 'directory') {
    await loadFiles(entry.path);
    return;
  }
  fileLoading.value = true;
  try {
    filePreview.value = await api.previewContainerFile(connectionId.value, selectedContainer.value.id, entry.path);
  } catch (cause: any) {
    fileError.value = cause?.message || String(cause);
  } finally {
    fileLoading.value = false;
  }
}

async function sampleVisibleContainers() {
  if (document.hidden || resource.value !== 'containers' || selectedContainer.value) return;
  const ids = matchingContainers.value.filter(isRunning).map((container) => container.id);
  if (!ids.length) {
    listStats.value = {};
    return;
  }
  try {
    const stats = await api.containerStats(connectionId.value, ids);
    listStats.value = Object.fromEntries(stats.map((value) => [value.containerId, value]));
  } catch {
    // 指标不可用时由资源刷新兜底。
  }
}

async function sampleSelectedContainer() {
  const container = selectedContainer.value;
  if (!container || !isRunning(container) || document.hidden || detailTab.value !== 'monitoring') return;
  try {
    const [point] = await api.containerStats(connectionId.value, [container.id]);
    if (!point) return;
    const cutoff = Date.now() - 15 * 60 * 1000;
    trend.value = [...trend.value, point].filter((value) => new Date(value.readAt || Date.now()).getTime() >= cutoff);
  } catch {
    // 保留最近一次成功采样。
  }
}

function restartListSampling() {
  if (listStatsTimer) window.clearInterval(listStatsTimer);
  listStatsTimer = window.setInterval(() => void sampleVisibleContainers(), 5000);
  void sampleVisibleContainers();
}

function restartResourceRefresh() {
  if (resourceRefreshTimer) window.clearInterval(resourceRefreshTimer);
  resourceRefreshTimer = undefined;
  refreshCountdown.value = 10;
  if (!autoRefresh.value) return;
  resourceRefreshTimer = window.setInterval(() => {
    const active = autoRefresh.value && !document.hidden && resource.value === 'containers' && !selectedContainer.value;
    if (!active) {
      refreshCountdown.value = 10;
      return;
    }
    if (refreshInFlight.value) return;
    refreshCountdown.value -= 1;
    if (refreshCountdown.value <= 0) {
      refreshCountdown.value = 10;
      void loadResource('containers');
    }
  }, 1000);
}

function handleVisibilityChange() {
  restartDetailSampling();
  restartResourceRefresh();
}

function stopDetailSampling() {
  if (detailStatsTimer) window.clearInterval(detailStatsTimer);
  detailStatsTimer = undefined;
}

function restartDetailSampling() {
  stopDetailSampling();
  detailStatsTimer = window.setInterval(() => void sampleSelectedContainer(), 2000);
  void sampleSelectedContainer();
}

watch(detailTab, async (tab) => {
  if (tab === 'logs') await startLogs();
  else await stopLogs();
  if (tab === 'files' && !fileEntries.value.length) await loadFiles('/');
  restartDetailSampling();
});

// 容器在终端打开期间被停止时，终端标签会从列表消失；同步把面板切回概览，
// 避免出现「标签已消失但面板仍停留」的残缺状态。
watch(detailTabs, (tabs) => {
  if (detailTab.value === 'terminal' && !tabs.includes('terminal')) detailTab.value = 'overview';
});

watch(resource, () => {
  restartListSampling();
  restartResourceRefresh();
});
watch(autoRefresh, restartResourceRefresh);
watch(selectedContainerId, restartResourceRefresh);
watch(pullImageOpen, (open) => {
  if (!open) pullProgress.value = '';
});
watch(dangerOpen, (open) => {
  if (!open && dangerResolve) settleConfirmation(false);
});

// 宿主在 window.dbxPlugin.locale 上暴露当前语言，但该值只在 init 消息到达后
// 才是真实值（沙箱里的初值是 "en"）。因此必须先 await ready 再取语言，
// 并订阅 dbx-plugin-init / dbx-plugin-env 以跟随 DBX 的语言切换。
function applyHostLocale(payload?: PluginEnvPayload) {
  const next = typeof payload?.locale === 'string' ? payload.locale : currentLocale();
  setLocale(next);
}

function handleHostEnv(event: Event) {
  applyHostLocale((event as CustomEvent<PluginEnvPayload>).detail);
}

onMounted(async () => {
  const plugin = getPlugin();
  document.addEventListener('dbx-plugin-init', handleHostEnv);
  document.addEventListener('dbx-plugin-env', handleHostEnv);
  await plugin.ready;
  applyHostLocale();
  const context = plugin.context;
  connectionId.value = context.connectionId || context.connection?.id || '';
  initStreams(connectionId.value);
  document.addEventListener('visibilitychange', handleVisibilityChange);
  if (!connectionId.value) {
    error.value = t('missingConnection');
    return;
  }
  await Promise.all([loadEngineInfo(), loadResource('containers')]);
  restartListSampling();
  restartResourceRefresh();
});

onUnmounted(() => {
  document.removeEventListener('dbx-plugin-init', handleHostEnv);
  document.removeEventListener('dbx-plugin-env', handleHostEnv);
  document.removeEventListener('visibilitychange', handleVisibilityChange);
  if (listStatsTimer) window.clearInterval(listStatsTimer);
  if (resourceRefreshTimer) window.clearInterval(resourceRefreshTimer);
  stopDetailSampling();
  void stopLogs();
  void stopImagePull();
  void cancelActiveTransfers();
});
</script>

<template>
  <div class="workbench">
    <header class="docker-header">
      <nav class="header-tabs">
        <button v-for="kind in ['containers', 'images', 'volumes', 'networks'] as ResourceKind[]" :key="kind" class="docker-main-tab" :class="{ active: resource === kind }" @click="selectResource(kind)">
          {{ t(kind) }}
        </button>
      </nav>
      <div class="header-actions">
        <button class="icon-btn icon-cyan" :title="t('engineJson')" @click="loadEngineDetails('json')"><Icon name="settings" /></button>
        <button class="icon-btn icon-amber" :title="t('engineInformation')" @click="loadEngineDetails('summary')"><Icon name="circle-help" /></button>
        <Popover :open="diskUsageOpen" @update:open="diskUsageOpen = $event">
          <template #trigger>
            <button class="icon-btn icon-emerald" :title="t('diskUsage')" @click="openDiskUsage"><Icon name="hard-drive" /></button>
          </template>
          <div class="disk-panel">
            <div class="disk-title">{{ t('diskUsage') }}</div>
            <div class="disk-desc">{{ t('diskUsageDescription') }}</div>
            <div v-if="diskUsageError" class="error-text disk-error">{{ diskUsageError }}</div>
            <div v-else-if="diskUsageLoading && !diskUsage" class="muted-text disk-loading">{{ t('waitingForLogs') }}</div>
            <table v-else-if="diskUsage" class="disk-table">
              <tbody>
                <tr v-for="row in diskUsageRows" :key="row.key">
                  <td class="disk-name">{{ t(row.key) }}</td>
                  <td class="disk-count">{{ row.count }}</td>
                  <td class="disk-size">{{ formatBytes(row.size) }}</td>
                  <td class="disk-reclaim">{{ formatBytes(row.reclaimable) }}</td>
                  <td class="disk-action">
                    <button
                      v-if="row.target"
                      class="btn btn-outline btn-sm"
                      :disabled="isReadOnly || pruneInFlight === row.target"
                      @click="runPrune(row.target, false, row.pruneLabel)"
                    >
                      <Icon v-if="pruneInFlight === row.target" name="loader-circle" class="spin" />
                      {{ t('prune') }}
                    </button>
                    <button
                      v-else-if="row.allTarget"
                      class="btn btn-outline btn-sm"
                      :disabled="isReadOnly || pruneInFlight === row.allTarget"
                      @click="runPrune(row.allTarget, true, t('pruneImagesAll'))"
                    >
                      <Icon v-if="pruneInFlight === row.allTarget" name="loader-circle" class="spin" />
                      {{ t('pruneImagesAll') }}
                    </button>
                  </td>
                </tr>
              </tbody>
              <tfoot>
                <tr>
                  <td colspan="2">{{ t('storageTotal') }}</td>
                  <td colspan="2">{{ formatBytes(diskUsage.layersSize) }}</td>
                  <td>
                    <button class="btn btn-ghost btn-sm" :disabled="diskUsageLoading" @click="loadDiskUsage">
                      <Icon name="refresh-cw" :class="{ spin: diskUsageLoading }" />
                    </button>
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>
        </Popover>
        <Popover :open="transferOpen" @update:open="transferOpen = $event">
          <template #trigger>
            <button class="icon-btn icon-blue" :title="t('transfers')">
              <Icon name="list-checks" />
              <span v-if="runningTransfers" class="transfer-dot" />
            </button>
          </template>
          <div class="transfer-panel">
            <div class="transfer-title">{{ t('transfers') }}</div>
            <div v-if="!transfers.length" class="transfer-empty">{{ t('noTransfers') }}</div>
            <div v-else class="transfer-list">
              <div v-for="task in transfers" :key="task.sessionId" class="transfer-item">
                <div class="transfer-row">
                  <Icon v-if="task.direction === 'upload'" name="file-up" class="transfer-icon-up" />
                  <Icon v-else name="file-down" class="transfer-icon-down" />
                  <span class="transfer-image">{{ task.image }}</span>
                  <span class="transfer-direction">{{ task.direction === 'upload' ? t('upload') : t('download') }}</span>
                  <span class="transfer-percent">{{ transferPercent(task) == null ? '—' : `${Math.round(transferPercent(task)!)}%` }}</span>
                  <button v-if="task.status === 'running'" class="icon-btn icon-xs" @click="cancelTransfer(task)"><Icon name="x" /></button>
                </div>
                <div class="transfer-bar">
                  <div v-if="transferPercent(task) != null" class="transfer-bar-fill" :style="{ width: `${transferPercent(task)}%` }" />
                  <div v-else-if="task.status === 'running'" class="docker-indeterminate transfer-bar-fill transfer-bar-ind" />
                </div>
                <div class="transfer-meta">
                  <span class="truncate">{{ t(`transferStatus.${task.status}`) }}</span>
                  <span class="transfer-bytes">{{ formatBytes(task.bytesCompleted) }}<template v-if="task.bytesTotal"> / {{ formatBytes(task.bytesTotal) }}</template></span>
                </div>
                <div v-if="task.savedPath" class="transfer-saved" :title="task.savedPath">
                  <Icon name="folder" class="transfer-saved-icon" />
                  <span class="transfer-saved-label">{{ t('savedPathLabel') }}</span>
                  <span class="transfer-saved-path">{{ task.savedPathKind === 'web' ? t('browserDownloadHint') : directoryOf(task.savedPath) }}</span>
                  <button class="docker-copy-button" :title="t('copyPath')" @click="copySavedPath(task)"><Icon name="copy" /></button>
                </div>
                <div v-if="task.error" class="transfer-error">{{ task.error }}</div>
              </div>
            </div>
          </div>
        </Popover>
      </div>
    </header>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <main v-else class="main-area">
      <template v-if="selectedContainer">
        <div class="detail-header">
          <button class="btn btn-ghost btn-sm" @click="closeDetail"><Icon name="arrow-left" />{{ t('backToContainers') }}</button>
          <span class="detail-divider" />
          <div class="detail-title">
            <div class="truncate detail-name">{{ containerName(selectedContainer) }}</div>
            <div class="detail-id">{{ shortId(selectedContainer.id) }}</div>
          </div>
        </div>
        <div class="detail-tabs">
          <button v-for="tab in detailTabs" :key="tab" class="docker-detail-tab" :class="{ active: detailTab === tab }" @click="tab === 'terminal' ? openTerminalTab() : (detailTab = tab)">
            {{ t(`detail.${tab}`) }}
          </button>
        </div>
        <div class="detail-body">
          <div v-if="detailTab === 'overview'" class="detail-overview">
            <section class="overview-grid">
              <div class="docker-card">
                <span>{{ t('fullId') }}</span>
                <strong class="mono-xs break-all">{{ selectedContainer.id }}</strong>
              </div>
              <div class="docker-card">
                <span>{{ t('image') }}</span>
                <strong>{{ selectedContainer.image }}</strong>
                <small class="break-all mono-xs">{{ selectedContainer.imageId }}</small>
              </div>
              <div class="docker-card">
                <span>{{ t('status') }}</span>
                <strong>{{ selectedContainer.state }}</strong>
                <small>{{ selectedContainer.status }}</small>
              </div>
              <div class="docker-card">
                <span>{{ t('health') }}</span>
                <strong>{{ inspect.State?.Health?.Status || '—' }}</strong>
              </div>
              <div class="docker-card">
                <span>{{ t('command') }}</span>
                <strong class="mono-xs">{{ [inspect.Path, ...(inspect.Args || [])].filter(Boolean).join(' ') || selectedContainer.command }}</strong>
              </div>
              <div class="docker-card">
                <span>{{ t('created') }}</span>
                <strong>{{ formatDate(selectedContainer.created) }}</strong>
              </div>
            </section>
            <section>
              <h3 class="section-title">{{ t('environment') }}</h3>
              <pre class="docker-code">{{ (inspect.Config?.Env || []).join('\n') || '—' }}</pre>
            </section>
            <section>
              <h3 class="section-title">{{ t('mounts') }}</h3>
              <div class="stack">
                <div v-for="mount in inspect.Mounts || []" :key="`${mount.Source}-${mount.Destination}`" class="docker-row-value">
                  {{ mount.Source }} → {{ mount.Destination }} <span class="muted">({{ mount.Mode || mount.Type }})</span>
                </div>
                <div v-if="!inspect.Mounts?.length" class="docker-row-value">—</div>
              </div>
            </section>
            <section>
              <h3 class="section-title">{{ t('networks') }}</h3>
              <div class="stack">
                <div v-for="(network, name) in inspect.NetworkSettings?.Networks || {}" :key="String(name)" class="docker-row-value">{{ name }} · {{ network.IPAddress || '—' }}</div>
              </div>
            </section>
          </div>

          <div v-else-if="detailTab === 'logs'" class="logs-pane">
            <div class="logs-toolbar">
              <div class="search-box">
                <Icon name="search" class="search-icon" />
                <input v-model="logSearch" class="input search-input" :placeholder="t('searchLogs')" />
              </div>
              <button class="btn btn-outline btn-sm" @click="toggleLogPause"><Icon :name="logPaused ? 'play' : 'pause'" />{{ logPaused ? t('resume') : t('pause') }}</button>
              <label class="check-label"><input v-model="logAutoFollow" type="checkbox" @change="logAutoFollow && scrollLogsToBottom()" />{{ t('autoFollowLogs') }}</label>
              <button class="btn btn-outline btn-sm" @click="clearLogs">{{ t('clear') }}</button>
              <span v-if="pendingLogText" class="warn-text">{{ t('bufferedLogs') }}</span>
            </div>
            <div v-if="logError" class="error-text">{{ logError }}</div>
            <pre ref="logOutput" class="log-output" @scroll.passive="handleLogScroll">{{ visibleLogs || t('waitingForLogs') }}</pre>
          </div>

          <div v-else-if="detailTab === 'monitoring'" class="monitoring-grid">
            <LineChart title="CPU %" :data="cpuSeries[0].data" :color="cpuSeries[0].color" :value-formatter="(value) => `${value.toFixed(1)}%`" />
            <LineChart :title="`${t('memory')} %`" :data="memorySeries[0].data" :color="memorySeries[0].color" :value-formatter="(value) => `${value.toFixed(1)}%`" />
          </div>

          <div v-else-if="detailTab === 'terminal'" class="terminal-pane-wrapper">
            <ContainerTerminal
              v-if="terminalTabRequested && selectedContainer"
              :key="selectedContainer.id"
              :connection-id="connectionId"
              :container-id="selectedContainer.id"
              :container-name="containerName(selectedContainer)"
              :read-only="isReadOnly"
              :running="isRunning(selectedContainer)"
            />
          </div>

          <div v-else class="files-pane">
            <div class="files-list">
              <div class="files-toolbar">
                <button class="btn btn-ghost btn-sm" :disabled="filePath === '/'" @click="loadFiles(parentPath(filePath))"><Icon name="arrow-left" /></button>
                <span class="files-path">{{ filePath }}</span>
                <button class="btn btn-ghost btn-sm" :disabled="fileLoading" @click="loadFiles()"><Icon name="refresh-cw" :class="{ spin: fileLoading }" /></button>
              </div>
              <div v-if="fileError" class="error-text files-error">{{ fileError }}</div>
              <div v-else class="files-scroll">
                <button v-for="entry in fileEntries" :key="entry.path" class="file-row" @dblclick="openFile(entry)">
                  <Icon v-if="entry.kind === 'directory'" name="folder" class="file-icon-folder" />
                  <Icon v-else name="file" class="file-icon-file" />
                  <span class="file-name">{{ entry.name }}</span>
                  <span class="file-size">{{ entry.kind === 'directory' ? '' : formatBytes(entry.size) }}</span>
                </button>
              </div>
            </div>
            <div class="file-preview">
              <div v-if="filePreview?.binary" class="muted-text">{{ t('binaryPreviewUnsupported') }}</div>
              <pre v-else-if="filePreview" class="file-preview-content">{{ filePreview.content }}<template v-if="filePreview.truncated">…</template></pre>
              <div v-else class="file-preview-empty">{{ t('selectFile') }}</div>
            </div>
          </div>
        </div>
      </template>

      <template v-else>
        <div class="toolbar">
          <div class="search-box">
            <Icon name="search" class="search-icon" />
            <input v-model="query" class="input search-input" :placeholder="t('search')" />
          </div>
          <template v-if="resource === 'containers'">
            <button class="btn btn-primary" :disabled="isReadOnly" @click="openCreateContainer"><Icon name="plus" />{{ t('createContainer') }}</button>
            <div class="filter-group">
              <button v-for="value in ['all', 'running', 'stopped'] as ContainerFilter[]" :key="value" class="filter-btn" :class="{ active: filter === value }" @click="filter = value">
                {{ t(`filter.${value}`) }}
              </button>
            </div>
          </template>
          <template v-else-if="resource === 'images'">
            <button class="btn btn-primary" :disabled="isReadOnly" @click="pullImageOpen = true"><Icon name="download" />{{ t('pullImage') }}</button>
          </template>
          <button v-else-if="resource === 'volumes'" class="btn btn-primary" :disabled="isReadOnly" @click="createVolumeOpen = true"><Icon name="plus" />{{ t('createVolume') }}</button>
          <button v-else class="btn btn-primary" :disabled="isReadOnly" @click="createNetworkOpen = true"><Icon name="plus" />{{ t('createNetwork') }}</button>
          <template v-if="resource === 'containers'">
            <span class="last-refresh">{{ t('lastRefreshed') }}: {{ lastRefreshAt?.toLocaleTimeString() || '—' }}</span>
            <label class="check-label"><Switch v-model="autoRefresh" />{{ t('autoRefresh', { seconds: refreshCountdown }) }}</label>
          </template>
          <button class="btn btn-ghost" :class="{ 'ml-auto': resource !== 'containers' }" :disabled="loading" @click="loadResource()"><Icon name="refresh-cw" :class="{ spin: loading }" />{{ t('refresh') }}</button>
        </div>

        <div class="table-area">
          <table v-if="resource === 'containers'" class="docker-table" :style="tableStyle('containers')">
            <colgroup>
              <col v-for="(width, index) in columnWidths.containers" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <thead @pointerdown="handleHeaderPointer($event, 'containers')">
              <tr>
                <th><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('image')">{{ t('image') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('ports')">{{ t('ports') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('cpu')">CPU<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('memory')">{{ t('memory') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('status')">{{ t('status') }}<Icon name="arrow-up-down" /></button></th>
                <th class="th-right">{{ t('actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="[project, values] in composeGroups" :key="project">
                <tr class="compose-header">
                  <td colspan="7">
                    <button class="compose-toggle" @click="toggleProject(project)">
                      <Icon :name="expandedProjects.has(project) ? 'chevron-down' : 'chevron-right'" />
                      <Icon name="box" class="compose-icon" />{{ project }}<span class="compose-count">{{ values.length }}</span>
                    </button>
                  </td>
                  <td>
                    <div class="row-actions">
                      <button class="btn btn-ghost btn-sm" :disabled="isReadOnly" @click="openComposeEditor(project)"><Icon name="pencil" />{{ t('editCompose') }}</button>
                    </div>
                  </td>
                </tr>
                <tr v-for="container in expandedProjects.has(project) ? values : []" :key="container.id">
                  <td>
                    <div class="docker-copy-cell cell-indent">
                      <span class="status-dot" :class="isRunning(container) ? 'dot-running' : isPaused(container) ? 'dot-paused' : 'dot-stopped'" /><button class="link-btn" @click="openDetail(container)">{{ containerName(container) }}</button
                      ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(containerName(container))"><Icon name="copy" /></button>
                    </div>
                  </td>
                  <td>
                    <div class="docker-copy-cell mono-xs">
                      <span>{{ shortId(container.id) }}</span
                      ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(container.id)"><Icon name="copy" /></button>
                    </div>
                  </td>
                  <td :title="container.image">
                    <div class="docker-copy-cell docker-truncated-cell">
                      <span class="truncate">{{ container.image }}</span
                      ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(container.image)"><Icon name="copy" /></button>
                    </div>
                  </td>
                  <td :title="formatPorts(container)"><div class="docker-truncated-cell mono-xs">{{ formatPorts(container) }}</div></td>
                  <td>{{ listStats[container.id] ? `${listStats[container.id].cpuPercent.toFixed(1)}%` : '—' }}</td>
                  <td>{{ listStats[container.id] ? `${formatBytes(listStats[container.id].memoryUsage)} / ${formatBytes(listStats[container.id].memoryLimit)}` : '—' }}</td>
                  <td><span class="docker-status" :class="isRunning(container) ? 'running' : isPaused(container) ? 'paused' : 'stopped'">{{ containerStatusLabel(container) }}</span></td>
                  <td>
                    <div class="row-actions">
                      <button v-if="isRunning(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('pause')" @click="runAction(container, 'pause')"
                        ><Icon v-if="actionInFlight[container.id] === 'pause'" name="loader-circle" class="spin" /><Icon v-else name="pause" /></button
                      ><button v-if="isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('resume')" @click="runAction(container, 'unpause')"
                        ><Icon v-if="actionInFlight[container.id] === 'unpause'" name="loader-circle" class="spin" /><Icon v-else name="play" /></button
                      ><button v-if="isRunning(container) || isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('restart')" @click="runAction(container, 'restart')"
                        ><Icon v-if="actionInFlight[container.id] === 'restart'" name="loader-circle" class="spin" /><Icon v-else name="rotate-cw" /></button
                      ><button v-if="isRunning(container) || isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('stop')" @click="runAction(container, 'stop')"
                        ><Icon v-if="actionInFlight[container.id] === 'stop'" name="loader-circle" class="spin" /><Icon v-else name="square" /></button
                      ><button v-if="!isRunning(container) && !isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('start')" @click="runAction(container, 'start')"
                        ><Icon v-if="actionInFlight[container.id] === 'start'" name="loader-circle" class="spin" /><Icon v-else name="play" /></button
                      ><button v-if="!isRunning(container) && !isPaused(container) && !isReadOnly" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('remove')" @click="removeContainer(container)"
                        ><Icon v-if="actionInFlight[container.id] === 'remove'" name="loader-circle" class="spin" /><Icon v-else name="trash-2" /></button
                      ><button v-if="!isReadOnly" class="icon-btn icon-sm" :title="t('rename')" @click="openRename(container)"><Icon name="pencil" /></button
                      ><button class="btn btn-ghost btn-sm" @click="openDetail(container)">{{ t('details') }}</button>
                    </div>
                  </td>
                </tr>
              </template>
              <tr v-for="container in standaloneContainers" :key="container.id">
                <td>
                  <div class="docker-copy-cell">
                    <span class="status-dot" :class="isRunning(container) ? 'dot-running' : isPaused(container) ? 'dot-paused' : 'dot-stopped'" /><button class="link-btn" @click="openDetail(container)">{{ containerName(container) }}</button
                    ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(containerName(container))"><Icon name="copy" /></button>
                  </div>
                </td>
                <td>
                  <div class="docker-copy-cell mono-xs">
                    <span>{{ shortId(container.id) }}</span
                    ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(container.id)"><Icon name="copy" /></button>
                  </div>
                </td>
                <td :title="container.image">
                  <div class="docker-copy-cell docker-truncated-cell">
                    <span class="truncate">{{ container.image }}</span
                    ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(container.image)"><Icon name="copy" /></button>
                  </div>
                </td>
                <td :title="formatPorts(container)"><div class="docker-truncated-cell mono-xs">{{ formatPorts(container) }}</div></td>
                <td>{{ listStats[container.id] ? `${listStats[container.id].cpuPercent.toFixed(1)}%` : '—' }}</td>
                <td>{{ listStats[container.id] ? `${formatBytes(listStats[container.id].memoryUsage)} / ${formatBytes(listStats[container.id].memoryLimit)}` : '—' }}</td>
                <td><span class="docker-status" :class="isRunning(container) ? 'running' : isPaused(container) ? 'paused' : 'stopped'">{{ containerStatusLabel(container) }}</span></td>
                <td>
                  <div class="row-actions">
                    <button v-if="isRunning(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('pause')" @click="runAction(container, 'pause')"
                      ><Icon v-if="actionInFlight[container.id] === 'pause'" name="loader-circle" class="spin" /><Icon v-else name="pause" /></button
                    ><button v-if="isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('resume')" @click="runAction(container, 'unpause')"
                      ><Icon v-if="actionInFlight[container.id] === 'unpause'" name="loader-circle" class="spin" /><Icon v-else name="play" /></button
                    ><button v-if="isRunning(container) || isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('restart')" @click="runAction(container, 'restart')"
                      ><Icon v-if="actionInFlight[container.id] === 'restart'" name="loader-circle" class="spin" /><Icon v-else name="rotate-cw" /></button
                    ><button v-if="isRunning(container) || isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('stop')" @click="runAction(container, 'stop')"
                      ><Icon v-if="actionInFlight[container.id] === 'stop'" name="loader-circle" class="spin" /><Icon v-else name="square" /></button
                    ><button v-if="!isRunning(container) && !isPaused(container)" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('start')" @click="runAction(container, 'start')"
                      ><Icon v-if="actionInFlight[container.id] === 'start'" name="loader-circle" class="spin" /><Icon v-else name="play" /></button
                    ><button v-if="!isRunning(container) && !isPaused(container) && !isReadOnly" class="icon-btn icon-sm" :disabled="!!actionInFlight[container.id]" :title="t('remove')" @click="removeContainer(container)"
                      ><Icon v-if="actionInFlight[container.id] === 'remove'" name="loader-circle" class="spin" /><Icon v-else name="trash-2" /></button
                    ><button v-if="!isReadOnly" class="icon-btn icon-sm" :title="t('rename')" @click="openRename(container)"><Icon name="pencil" /></button
                    ><button class="btn btn-ghost btn-sm" @click="openDetail(container)">{{ t('details') }}</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>

          <table v-else-if="resource === 'images'" class="docker-table" :style="tableStyle('images')">
            <colgroup>
              <col v-for="(width, index) in columnWidths.images" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <thead @pointerdown="handleHeaderPointer($event, 'images')">
              <tr>
                <th><button class="docker-sort" @click="toggleSort('name')">{{ t('repositoryTag') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('size')">{{ t('size') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('created')">{{ t('created') }}<Icon name="arrow-up-down" /></button></th>
                <th class="th-right">{{ t('actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredImages" :key="item.id">
                <td :title="item.repoTags.join(', ')">
                  <div class="docker-copy-cell docker-truncated-cell">
                    <span class="truncate">{{ item.repoTags.join(', ') || '<none>' }}</span
                    ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(item.repoTags.join(', ') || item.id)"><Icon name="copy" /></button>
                  </div>
                </td>
                <td class="mono-xs">
                  <div class="docker-copy-cell">
                    <span>{{ shortId(item.id) }}</span
                    ><button class="docker-copy-button" :title="t('copy')" @click="copyValue(item.id)"><Icon name="copy" /></button>
                  </div>
                </td>
                <td>{{ formatBytes(item.size) }}</td>
                <td>{{ formatDate(item.created) }}</td>
                <td>
                  <div class="row-actions">
                    <button class="btn btn-ghost btn-sm" :disabled="isReadOnly || !!imageActionInFlight[item.id]" @click="openPushImage(item)"><Icon name="upload" />{{ t('push') }}</button
                    ><button class="btn btn-ghost btn-sm" :disabled="!!imageActionInFlight[item.id]" @click="exportImage(item)"
                      ><Icon v-if="imageActionInFlight[item.id] === 'export'" name="loader-circle" class="spin" /><Icon v-else name="download" />{{ t('export') }}</button
                    ><button class="icon-btn icon-sm" :title="t('imageHistory')" @click="openImageHistory(item)"><Icon name="layers" /></button
                    ><button v-if="!isReadOnly" class="icon-btn icon-sm" :title="t('tagImage')" @click="openTagImage(item)"><Icon name="tag" /></button
                    ><button class="icon-btn icon-sm" :disabled="isReadOnly || !!imageActionInFlight[item.id]" :title="t('remove')" @click="removeImage(item)"
                      ><Icon v-if="imageActionInFlight[item.id] === 'remove'" name="loader-circle" class="spin" /><Icon v-else name="trash-2" /></button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>

          <table v-else-if="resource === 'volumes'" class="docker-table" :style="tableStyle('volumes')">
            <colgroup>
              <col v-for="(width, index) in columnWidths.volumes" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <thead @pointerdown="handleHeaderPointer($event, 'volumes')">
              <tr>
                <th><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('driver')">{{ t('driver') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('scope')">{{ t('scope') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('mountpoint')">{{ t('mountpoint') }}<Icon name="arrow-up-down" /></button></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredVolumes" :key="item.name">
                <td class="cell-strong" :title="item.name"><div class="docker-truncated-cell">{{ item.name }}</div></td>
                <td><div class="docker-truncated-cell">{{ item.driver }}</div></td>
                <td><div class="docker-truncated-cell">{{ item.scope }}</div></td>
                <td class="mono-xs" :title="item.mountpoint"><div class="docker-truncated-cell">{{ item.mountpoint }}</div></td>
              </tr>
            </tbody>
          </table>

          <table v-else class="docker-table" :style="tableStyle('networks')">
            <colgroup>
              <col v-for="(width, index) in columnWidths.networks" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <thead @pointerdown="handleHeaderPointer($event, 'networks')">
              <tr>
                <th><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('driver')">{{ t('driver') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('scope')">{{ t('scope') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('internal')">{{ t('internal') }}<Icon name="arrow-up-down" /></button></th>
                <th><button class="docker-sort" @click="toggleSort('attachable')">{{ t('attachable') }}<Icon name="arrow-up-down" /></button></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredNetworks" :key="item.id">
                <td class="cell-strong">{{ item.name }}</td>
                <td class="mono-xs">{{ shortId(item.id) }}</td>
                <td>{{ item.driver }}</td>
                <td>{{ item.scope }}</td>
                <td>{{ item.internal ? '✓' : '—' }}</td>
                <td>{{ item.attachable ? '✓' : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </main>

    <Dialog :open="createContainerOpen" content-class="dlg-wide" @update:open="createContainerOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('createContainer') }}</div>
        <div class="dlg-desc">{{ t('createContainerDescription') }}</div>
      </div>
      <div class="filter-group">
        <button class="filter-btn" :class="{ active: createMode === 'form' }" @click="createMode = 'form'">{{ t('formMode') }}</button>
        <button class="filter-btn" :class="{ active: createMode === 'compose' }" @click="openComposeEditor(composeEditingProject)">{{ t('composeMode') }}</button>
      </div>
      <div v-if="createMode === 'form'" class="form-grid">
        <label class="docker-field"><span>{{ t('name') }}</span><input v-model="createContainerDraft.name" class="input" /></label>
        <label class="docker-field"><span>{{ t('image') }}</span><input v-model="createContainerDraft.image" class="input" placeholder="nginx:latest" /></label>
        <label class="docker-field"><span>{{ t('commandLines') }}</span><textarea v-model="createContainerDraft.command" rows="4" class="docker-textarea" /></label>
        <label class="docker-field"><span>{{ t('environmentLines') }}</span><textarea v-model="createContainerDraft.environment" rows="4" class="docker-textarea" placeholder="KEY=value" /></label>
        <label class="docker-field"><span>{{ t('portLines') }}</span><textarea v-model="createContainerDraft.ports" rows="4" class="docker-textarea" placeholder="127.0.0.1:8080:80/tcp" /></label>
        <label class="docker-field"><span>{{ t('mountLines') }}</span><textarea v-model="createContainerDraft.mounts" rows="4" class="docker-textarea" placeholder="volume-name:/data:ro" /></label>
        <label class="docker-field">
          <span>{{ t('network') }}</span>
          <select v-model="createContainerDraft.network" class="docker-select">
            <option value="">{{ t('defaultNetwork') }}</option>
            <option v-for="item in networks" :key="item.id" :value="item.name">{{ item.name }}</option>
          </select>
        </label>
        <label class="docker-field">
          <span>{{ t('restartPolicy') }}</span>
          <select v-model="createContainerDraft.restartPolicy" class="docker-select">
            <option value="no">no</option>
            <option value="always">always</option>
            <option value="unless-stopped">unless-stopped</option>
            <option value="on-failure">on-failure</option>
          </select>
        </label>
        <label class="check-label"><input v-model="createContainerDraft.start" type="checkbox" />{{ t('startAfterCreate') }}</label>
      </div>
      <div v-else class="stack">
        <label class="docker-field"><span>{{ t('composeProject') }}</span><input v-model="composeDraft.projectName" class="input" :disabled="!!composeEditingProject" placeholder="my-project" /></label>
        <label class="docker-field"><span>compose.yaml</span><textarea v-model="composeDraft.content" rows="20" class="docker-textarea compose-editor" spellcheck="false" /></label>
        <p class="hint-text">{{ t('composeSubsetHint') }}</p>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="createContainerOpen = false">{{ t('cancel') }}</button>
        <button v-if="createMode === 'form'" class="btn btn-primary" :disabled="submitting || !createContainerDraft.name.trim() || !createContainerDraft.image.trim()" @click="createContainer">
          <Icon v-if="submitting" name="loader-circle" class="spin" />{{ t('create') }}
        </button>
        <button v-else class="btn btn-primary" :disabled="submitting || !composeDraft.projectName.trim() || !composeDraft.content.trim()" @click="applyCompose">
          <Icon v-if="submitting" name="loader-circle" class="spin" />{{ composeEditingProject ? t('saveCompose') : t('create') }}
        </button>
      </div>
    </Dialog>

    <Dialog :open="pullImageOpen" @update:open="pullImageOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('pullImage') }}</div>
        <div class="dlg-desc">{{ t('registryCredentialsTemporary') }}</div>
      </div>
      <div class="stack">
        <label class="docker-field"><span>{{ t('image') }}</span><input v-model="pullDraft.image" class="input" placeholder="nginx:latest" /></label>
        <label class="docker-field"><span>{{ t('registry') }}</span><input v-model="pullDraft.serverAddress" class="input" placeholder="registry.example.com" /></label>
        <div class="form-grid">
          <label class="docker-field"><span>{{ t('username') }}</span><input v-model="pullDraft.username" class="input" /></label>
          <label class="docker-field"><span>{{ t('password') }}</span><input v-model="pullDraft.password" class="input" type="password" /></label>
        </div>
        <pre v-if="pullProgress" class="pull-progress">{{ pullProgress }}</pre>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="pullImageOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="pulling || !pullDraft.image.trim()" @click="pullImage">{{ t('pull') }}</button>
      </div>
    </Dialog>

    <Dialog :open="pushImageOpen" @update:open="pushImageOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('pushImage') }}</div>
        <div class="dlg-desc">{{ t('registryCredentialsTemporaryPush') }}</div>
      </div>
      <div class="stack">
        <label class="docker-field"><span>{{ t('targetReference') }}</span><input v-model="pushDraft.targetReference" class="input" placeholder="registry.example.com/team/image:tag" /></label>
        <label class="docker-field"><span>{{ t('registry') }}</span><input v-model="pushDraft.serverAddress" class="input" placeholder="registry.example.com" /></label>
        <div class="form-grid">
          <label class="docker-field"><span>{{ t('username') }}</span><input v-model="pushDraft.username" class="input" /></label>
          <label class="docker-field"><span>{{ t('password') }}</span><input v-model="pushDraft.password" class="input" type="password" /></label>
        </div>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="pushImageOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="!pushDraft.targetReference.trim()" @click="pushImage"><Icon name="upload" />{{ t('push') }}</button>
      </div>
    </Dialog>

    <Dialog :open="engineJsonOpen" content-class="dlg-xwide" @update:open="engineJsonOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('engineJson') }}</div>
        <div class="dlg-desc">{{ t('engineJsonReadonlyHint') }}</div>
      </div>
      <div class="engine-toolbar">
        <div class="search-box grow">
          <Icon name="search" class="search-icon" />
          <input v-model="engineJsonSearch" class="input search-input" :placeholder="t('searchEngineJson')" />
        </div>
        <button class="btn btn-outline btn-sm" @click="copyValue(JSON.stringify(engineJson, null, 2))"><Icon name="copy" />{{ t('copy') }}</button>
      </div>
      <div class="engine-json-area">
        <div v-if="engineDetailsLoading" class="center-box"><Icon name="loader-circle" class="spin" /></div>
        <pre v-else-if="engineJsonSearch" class="mono-xs wrap-all">{{ filteredEngineJson }}</pre>
        <JsonTree v-else :value="engineJson" :initial-expanded-depth="2" />
      </div>
    </Dialog>

    <Dialog :open="engineSummaryOpen" content-class="dlg-medium" @update:open="engineSummaryOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('engineInformation') }}</div>
      </div>
      <div v-if="engineDetailsLoading" class="center-box"><Icon name="loader-circle" class="spin" /></div>
      <div v-else-if="engineDetails" class="summary-grid">
        <div
          v-for="[label, value] in [
            [t('engine'), engineDetails.summary.engineVersion],
            [t('apiVersion'), engineDetails.summary.apiVersion],
            [t('minimumApiVersion'), engineDetails.summary.minimumApiVersion],
            [t('operatingSystem'), engineDetails.summary.operatingSystem],
            [t('architecture'), engineDetails.summary.architecture],
            [t('kernelVersion'), engineDetails.summary.kernelVersion],
            [t('storageDriver'), engineDetails.summary.storageDriver],
            [t('containers'), engineDetails.summary.containers],
            [t('running'), engineDetails.summary.containersRunning],
            [t('paused'), engineDetails.summary.containersPaused],
            [t('stopped'), engineDetails.summary.containersStopped],
            [t('images'), engineDetails.summary.images],
            [t('rootDir'), engineDetails.summary.dockerRootDir],
          ]"
          :key="String(label)"
          class="docker-card"
        >
          <span>{{ label }}</span>
          <strong>{{ value ?? '—' }}</strong>
        </div>
        <div class="docker-card span-2">
          <span>{{ t('securityOptions') }}</span>
          <strong class="wrap-text xs-strong">{{ engineDetails.summary.securityOptions.join('\n') || '—' }}</strong>
        </div>
        <div v-if="engineDetails.summary.warnings.length" class="span-2 warnings-box">{{ engineDetails.summary.warnings.join('\n') }}</div>
      </div>
    </Dialog>

    <Dialog :open="createVolumeOpen" @update:open="createVolumeOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('createVolume') }}</div>
      </div>
      <div class="stack">
        <label class="docker-field"><span>{{ t('name') }}</span><input v-model="volumeDraft.name" class="input" /></label>
        <label class="docker-field"><span>{{ t('driver') }}</span><input v-model="volumeDraft.driver" class="input" /></label>
        <label class="docker-field"><span>{{ t('labelsOptional') }}</span><textarea v-model="volumeDraft.labels" rows="3" class="docker-textarea" placeholder="key=value" /></label>
        <label class="docker-field"><span>{{ t('driverOptions') }}</span><textarea v-model="volumeDraft.driverOptions" rows="3" class="docker-textarea" placeholder="key=value" /></label>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="createVolumeOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="submitting || !volumeDraft.name.trim()" @click="createVolume">{{ t('create') }}</button>
      </div>
    </Dialog>

    <Dialog :open="createNetworkOpen" @update:open="createNetworkOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('createNetwork') }}</div>
      </div>
      <div class="form-grid">
        <label class="docker-field"><span>{{ t('name') }}</span><input v-model="networkDraft.name" class="input" /></label>
        <label class="docker-field"><span>{{ t('driver') }}</span><input v-model="networkDraft.driver" class="input" /></label>
        <label class="docker-field"><span>{{ t('subnet') }}</span><input v-model="networkDraft.subnet" class="input" placeholder="172.28.0.0/16" /></label>
        <label class="docker-field"><span>{{ t('gateway') }}</span><input v-model="networkDraft.gateway" class="input" placeholder="172.28.0.1" /></label>
        <label class="check-label"><input v-model="networkDraft.internal" type="checkbox" />{{ t('internal') }}</label>
        <label class="check-label"><input v-model="networkDraft.attachable" type="checkbox" />{{ t('attachable') }}</label>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="createNetworkOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="submitting || !networkDraft.name.trim()" @click="createNetwork">{{ t('create') }}</button>
      </div>
    </Dialog>

    <Dialog :open="renameOpen" @update:open="renameOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('renameContainer') }}</div>
        <div class="dlg-desc">{{ t('renameContainerDescription') }}</div>
      </div>
      <label class="docker-field"><span>{{ t('containerNameLabel') }}</span><input v-model="renameDraft.name" class="input" @keydown.enter.prevent="submitRename" /></label>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="renameOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="renameSubmitting || !renameDraft.name.trim()" @click="submitRename">{{ t('confirm') }}</button>
      </div>
    </Dialog>

    <Dialog :open="tagOpen" @update:open="tagOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('tagImage') }}</div>
        <div class="dlg-desc">{{ t('tagImageDescription') }}</div>
      </div>
      <div class="stack">
        <div class="muted-text mono-xs break-all">{{ tagDraft.reference }}</div>
        <div class="docker-field">
          <span>{{ t('existingTags') }}</span>
          <div v-if="tagDraftTags.length" class="tag-chip-list">
            <span v-for="tag in tagDraftTags" :key="tag" class="tag-chip">
              <span class="mono-xs">{{ tag }}</span>
              <button v-if="!isReadOnly" class="tag-chip-remove" :title="t('removeTag')" @click="removeImageTag(tag)"><Icon name="x" /></button>
            </span>
          </div>
          <div v-else class="muted-text xs-strong">{{ t('noTags') }}</div>
        </div>
        <label class="docker-field"><span>{{ t('repositoryLabel') }}</span><input v-model="tagDraft.repository" class="input" placeholder="registry.example.com/team/app" /></label>
        <label class="docker-field"><span>{{ t('tagLabel') }}</span><input v-model="tagDraft.tag" class="input" placeholder="latest" /></label>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="tagOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-primary" :disabled="tagSubmitting || !tagDraft.repository.trim()" @click="submitTagImage">{{ t('confirm') }}</button>
      </div>
    </Dialog>

    <Dialog :open="historyOpen" content-class="dlg-wide" @update:open="historyOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('imageHistory') }}</div>
        <div class="dlg-desc">{{ historyImage }} · {{ t('imageHistoryDescription') }}</div>
      </div>
      <div v-if="historyLoading" class="muted-text">{{ t('waitingForLogs') }}</div>
      <div v-else-if="historyError" class="error-text">{{ historyError }}</div>
      <div v-else-if="!historyLayers.length" class="muted-text">{{ t('imageHistoryEmpty') }}</div>
      <div v-else class="layers-list">
        <div v-for="(layer, index) in historyLayers" :key="`${layer.id}-${index}`" class="layer-row">
          <div class="layer-head">
            <span class="layer-index mono-xs">{{ historyLayers.length - index }}</span>
            <span class="layer-size mono-xs">{{ formatBytes(layer.size) }}</span>
            <span class="layer-date muted-text xs-strong">{{ formatDate(layer.created) }}</span>
          </div>
          <div class="layer-command mono-xs">{{ layer.createdBy || '—' }}</div>
          <div v-if="layer.tags.length" class="layer-tags">
            <span v-for="tag in layer.tags" :key="tag" class="layer-tag">{{ tag }}</span>
          </div>
        </div>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="historyOpen = false">{{ t('cancel') }}</button>
      </div>
    </Dialog>

    <ConfirmDialog
      :open="dangerOpen"
      :message="dangerMessage"
      :confirm-label="t('confirm')"
      :cancel-label="t('cancel')"
      @update:open="dangerOpen = $event"
      @confirm="settleConfirmation(true)"
    />

    <div class="toast-container">
      <div v-for="item in toasts" :key="item.id" class="toast">{{ item.message }}</div>
    </div>
  </div>
</template>

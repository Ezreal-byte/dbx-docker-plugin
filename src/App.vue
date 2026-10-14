<script setup lang="ts">
// 主工作台：逐区块复刻 dbx docker 分支 DockerWorkbench.vue。
// 差异：api.docker* → 本插件 bridge.invoke('docker/*')；Tauri 事件流 → binary channel；
// ui/* 组件 → 本地组件 + .docker-* CSS（style.css）；read_only/is_production 从后端快照获取。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { currentLocale, getPlugin, type PluginEnvPayload } from './bridge';
import { locale, setLocale, t } from './i18n';
import * as api from './api';
import { initStreams, registerExecStream, registerExportStream, registerFileStream, registerTransferStream, startLogStream, unregisterStream, type ExportProgress } from './streams';
import { sendExecBytes } from './exec';
import { createDockerProgressParseState, parseDockerProgressEvent, type DockerProgressParseState } from './progress';
import { createPendingDockerPullTask, newSessionId } from './pullTask';
import { copyToClipboard, directoryOf, formatBytes, formatDate, savedPathKind, shortId } from './format';
import { computeRate, formatRate, timeLabels } from './metrics';
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
  DockerPrunePreview,
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
import MetricChart, { type MetricSeries } from './components/MetricChart.vue';
import JsonTree from './components/JsonTree.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';
import ContainerTerminal from './components/ContainerTerminal.vue';
import AutomationPanel from './components/AutomationPanel.vue';
import ResourceDetails from './components/ResourceDetails.vue';
import LoadingSkeleton from './components/LoadingSkeleton.vue';
import FileContextMenu from './components/FileContextMenu.vue';
import CodeEditor from './components/CodeEditor.vue';
import ResizableHeader from './components/ResizableHeader.vue';
import ContainerPorts from './components/ContainerPorts.vue';
import ContainerOverview from './components/ContainerOverview.vue';
import { composeFromInspects } from './compose';
import { local, recommendations, mutationPreview, mutationExecute, type MutationPreview, type Task } from './automation';
import { invoke } from './bridge';

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
const connectionName = ref('');
const resourceDetailsOpen = ref(false);
const resourceDetailsKind = ref('images');
const resourceDetailsId = ref('');
function openResourceDetails(kind: string, id: string) { selectedRowKey.value = id; resourceDetailsKind.value = kind; resourceDetailsId.value = id; resourceDetailsOpen.value = true; }
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
// 表格行选中：同一时刻只展示一张表，用单个 key 即可（切换资源时清空）。
const selectedRowKey = ref('');
const detailTab = ref<DetailTab>('overview');
const inspect = ref<Record<string, any>>({});
const detailLoading = ref(false);
const trend = ref<TrendPoint[]>([]);
const actionInFlight = ref<Record<string, string | undefined>>({});
const imageActionInFlight = ref<Record<string, string | undefined>>({});
type TransferTask = DockerTransferProgress & {
  startedAt: number;
  contextLabel?: string;
  containerPath?: string;
  saving?: boolean;
  handle?: DockerStreamHandle;
  savedPath?: string;
  savedPathKind?: 'desktop' | 'web';
};

interface FileTransferTask {
  sessionId: string;
  name: string;
  /** 容器内路径；下载完成后替换为本地保存路径。 */
  path: string;
  direction: 'download' | 'upload';
  status: 'running' | 'done' | 'error' | 'cancelled';
  bytes: number;
  total: number;
  error?: string;
  savedPath?: string;
  savedPathKind?: 'desktop' | 'web';
}
const transfers = ref<TransferTask[]>([]);
const cancelledTransferIds = new Set<string>();
const transferParseStates = new Map<string, DockerProgressParseState>();
const transferOpen = ref(false);
let workbenchClosed = false;
const pushImageOpen = ref(false);
const pushDraft = ref({ sourceImageId: '', targetReference: '', serverAddress: '', username: '', password: '' });
const autoRefresh = ref(true);
const refreshCountdown = ref(10);
const lastRefreshAt = ref<Date>();
const refreshInFlight = ref(false);
const columnWidths = ref<Record<ResourceKind, number[]>>({
  containers: [230, 120, 220, 260, 80, 180, 100, 260],
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
const composeLoading = ref(false), composeError = ref('');
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
// 清理是不可恢复操作，因此走弹窗 + 预览 + 输入确认短语三重门禁，不再提供一键按钮。
type PruneChoice = DockerPruneTarget | 'images-all' | 'volumes-all';

const diskUsageOpen = ref(false);
const diskUsage = ref<DockerDiskUsage>();
const diskUsageLoading = ref(false);
const diskUsageError = ref('');
const pruneChoice = ref<PruneChoice>('containers');
const prunePreview = ref<DockerPrunePreview>();
const boundPrunePreview = ref<MutationPreview>();
const prunePreviewLoading = ref(false);
const prunePreviewError = ref('');
const pruneConfirmText = ref('');
const pruneRunning = ref(false);

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
const filePreviewLoading = ref(false);
const fileMenu = ref({ open: false, x: 0, y: 0, entry: undefined as DockerFileEntry | undefined });
const fileEdit = ref({ open: false, operation: '', path: '', name: '', busy: false, error: '' });
const fileEditTitle = computed(() => ({ mkdir: local('新建文件夹', 'New folder'), create: local('新建文件', 'New file'), rename: local('重命名', 'Rename') }[fileEdit.value.operation] || ''));
const fileError = ref('');
const fileSelection = ref('');
const uploadInput = ref<HTMLInputElement>();


/** 与后端 maxFileTransferBytes 保持一致。 */
const MAX_FILE_TRANSFER_BYTES = 256 * 1024 * 1024;
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
  const tabs: DetailTab[] = ['overview', 'monitoring', 'logs', 'files'];
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

const pruneChoices = computed<{ value: PruneChoice; label: string; hint: string; risky?: boolean }[]>(() => [
  { value: 'containers', label: t('pruneContainers'), hint: t('pruneContainersHint') },
  { value: 'images', label: t('pruneImages'), hint: t('pruneImagesHint') },
  { value: 'images-all', label: t('pruneImagesAll'), hint: t('pruneImagesAllHint'), risky: true },
  { value: 'volumes', label: t('pruneVolumes'), hint: t('pruneVolumesHint') },
  { value: 'volumes-all', label: t('pruneVolumesAll'), hint: t('pruneVolumesAllHint'), risky: true },
  { value: 'networks', label: t('pruneNetworks'), hint: t('pruneNetworksHint') },
]);

const pruneConfirmPhrase = computed(() => t('pruneConfirmPhrase'));
const prunePhraseMatches = computed(() => pruneConfirmText.value.trim() === pruneConfirmPhrase.value);
const activePruneChoice = computed(() => pruneChoices.value.find((item) => item.value === pruneChoice.value));
const canExecutePrune = computed(
  () =>
    !isReadOnly.value &&
    !pruneRunning.value &&
    prunePhraseMatches.value &&
    !!prunePreview.value &&
    prunePreview.value.count > 0,
);

/** 去掉 -all 后缀得到 Docker API 的清理目标，后缀本身代表「包含具名/带标签资源」。 */
function apiPruneTarget(choice: PruneChoice): DockerPruneTarget {
  if (choice === 'images-all') return 'images';
  if (choice === 'volumes-all') return 'volumes';
  return choice;
}

function pruneChoiceIsAll(choice: PruneChoice): boolean {
  return choice === 'images-all' || choice === 'volumes-all';
}

async function loadPrunePreview() {
  prunePreviewLoading.value = true;
  prunePreviewError.value = '';
  try {
    boundPrunePreview.value = undefined;
    if (isReadOnly.value) prunePreview.value = await api.prunePreview(connectionId.value, apiPruneTarget(pruneChoice.value), pruneChoiceIsAll(pruneChoice.value));
    else {
      const preview = await mutationPreview(connectionId.value, 'prune_resources', { target: apiPruneTarget(pruneChoice.value), all: pruneChoiceIsAll(pruneChoice.value) });
      boundPrunePreview.value = preview; prunePreview.value = preview.preview as DockerPrunePreview;
    }
  } catch (cause: any) {
    prunePreview.value = undefined;
    prunePreviewError.value = cause?.message || String(cause);
  } finally {
    prunePreviewLoading.value = false;
  }
}

function openDiskUsage() {
  diskUsageOpen.value = true;
  pruneChoice.value = 'containers';
  pruneConfirmText.value = '';
  void loadDiskUsage();
  void loadPrunePreview();
}

async function selectPruneChoice(choice: PruneChoice) {
  if (pruneChoice.value === choice) return;
  pruneChoice.value = choice;
  // 确认短语是针对「具体这一批资源」的，换目标必须重新输入。
  pruneConfirmText.value = '';
  prunePreview.value = undefined;
  await loadPrunePreview();
}

async function executePrune() {
  if (!canExecutePrune.value) return;
  const choice = pruneChoice.value;
  const expected = prunePreview.value?.count ?? 0;
  pruneRunning.value = true;
  try {
    if (!boundPrunePreview.value) return;
    const result = await mutationExecute<{ taskId: string }>(connectionId.value, 'prune_resources', { target: apiPruneTarget(choice), all: pruneChoiceIsAll(choice) }, boundPrunePreview.value);
    boundPrunePreview.value = undefined; prunePreview.value = undefined; pruneConfirmText.value = '';
    diskUsageOpen.value = false;
    notifyTask(result.taskId);
  } catch (cause: any) {
    toast(cause?.message || String(cause), 5000);
  } finally {
    pruneRunning.value = false;
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
const MIB = 1024 * 1024;

const monitorLabels = computed(() => timeLabels(trend.value.map((point) => point.readAt)));
const cpuChartSeries = computed<MetricSeries[]>(() => [
  { name: 'CPU %', color: '#3b82f6', data: trend.value.map((point) => Number((point.cpuPercent ?? 0).toFixed(2))) },
]);
const memoryChartSeries = computed<MetricSeries[]>(() => [
  { name: t('memoryUsage'), color: '#8b5cf6', data: trend.value.map((point) => point.memoryUsage / MIB) },
  { name: t('memoryLimit'), color: '#c4b5fd', data: trend.value.map((point) => point.memoryLimit / MIB) },
]);
const networkChartSeries = computed<MetricSeries[]>(() => [
  { name: t('networkRx'), color: '#0ea5e9', data: computeRate(trend.value.map((point) => ({ readAt: point.readAt, value: point.networkRx }))) },
  { name: t('networkTx'), color: '#06b6d4', data: computeRate(trend.value.map((point) => ({ readAt: point.readAt, value: point.networkTx }))) },
]);
const blockIoChartSeries = computed<MetricSeries[]>(() => [
  { name: t('blockRead'), color: '#f59e0b', data: computeRate(trend.value.map((point) => ({ readAt: point.readAt, value: point.blockRead }))) },
  { name: t('blockWrite'), color: '#ef4444', data: computeRate(trend.value.map((point) => ({ readAt: point.readAt, value: point.blockWrite }))) },
]);
const latestSample = computed(() => trend.value[trend.value.length - 1]);
const latestNetworkRate = computed(() => {
  const rx = networkChartSeries.value[0]?.data ?? [];
  const tx = networkChartSeries.value[1]?.data ?? [];
  return { rx: rx[rx.length - 1] ?? 0, tx: tx[tx.length - 1] ?? 0 };
});
const latestBlockRate = computed(() => {
  const read = blockIoChartSeries.value[0]?.data ?? [];
  const write = blockIoChartSeries.value[1]?.data ?? [];
  return { read: read[read.length - 1] ?? 0, write: write[write.length - 1] ?? 0 };
});
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
  return { width: `${columnWidths.value[kind].reduce((sum, width) => sum + width, 0)}px`, minWidth: '100%' };
}
let columnWidthsEdited = false;
function resizeColumn(kind: ResourceKind, index: number, width: number) {
  columnWidthsEdited = true;
  columnWidths.value[kind][index] = width;
}
function saveColumnWidths() {
  if (!getPlugin().capabilities?.storage) return;
  void getPlugin().storage?.set('workbench-column-widths-v1', JSON.parse(JSON.stringify(columnWidths.value))).catch(() => {});
}
async function loadColumnWidths() {
  if (!getPlugin().capabilities?.storage) return;
  try {
    const saved = await getPlugin().storage?.get('workbench-column-widths-v1') as Record<string, unknown> | undefined;
    if (columnWidthsEdited) return;
    for (const kind of Object.keys(columnWidths.value) as ResourceKind[]) {
      const widths = saved?.[kind];
      if (Array.isArray(widths) && widths.length === columnWidths.value[kind].length && widths.every(n => typeof n === 'number' && n >= 64 && n <= 1600)) columnWidths.value[kind] = widths;
    }
  } catch { /* Older hosts keep widths for the current workbench. */ }
}

function upsertTransfer(progress: DockerTransferProgress, handle?: DockerStreamHandle) {
  const index = transfers.value.findIndex((task) => task.sessionId === progress.sessionId);
  if (index >= 0) {
    const next = [...transfers.value];
    next[index] = { ...next[index], ...progress, handle: handle ?? next[index].handle };
    transfers.value = next;
  } else {
    transfers.value = [{ ...progress, startedAt: Date.now(), handle }, ...transfers.value].filter((task, index) => task.status === 'running' || index < 50);
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
  if (task.status === 'done') return 100;
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

let resourceLoadSequence = 0;
let loadingResource: ResourceKind | undefined;
async function loadResource(kind = resource.value) {
  if (refreshInFlight.value && loadingResource === kind) return;
  const request = ++resourceLoadSequence;
  loadingResource = kind;
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
    if (request === resourceLoadSequence) { loading.value = false; refreshInFlight.value = false; loadingResource = undefined; }
  }
}

async function selectResource(kind: ResourceKind) {
  await closeDetail();
  resource.value = kind;
  selectedRowKey.value = '';
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
  fileSelection.value = '';
  filePath.value = '/';
  inspect.value = {};
  detailLoading.value = true;
  try {
    const result = await api.inspectContainer(connectionId.value, container.id);
    if (selectedContainerId.value !== container.id) return;
    inspect.value = result as Record<string, any>;
  } catch (e) { toast(String(e), 5000); }
  finally { if (selectedContainerId.value === container.id) detailLoading.value = false; }
  trend.value = [];
  restartDetailSampling();
}

async function closeDetail() {
  ++fileLoadSequence; ++filePreviewSequence;
  fileLoading.value = false; filePreviewLoading.value = false;
  fileMenu.value.open = false; fileEdit.value.open = false;
  stopDetailSampling();
  await stopLogs();
  selectedContainerId.value = '';
  detailTab.value = 'overview';
  terminalTabRequested.value = false;
  inspect.value = {};
  fileEntries.value = [];
  filePreview.value = undefined;
  fileSelection.value = '';
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
  actionInFlight.value = { ...actionInFlight.value, [container.id]: 'remove' };
  try {
    const args = { containerId: container.id };
    const preview = await previewAndConfirm('remove_container', args); if (!preview) return;
    await mutationExecute(connectionId.value, 'remove_container', args, preview);
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

async function openComposeEditor(project = '') {
  createMode.value = 'compose';
  composeEditingProject.value = project;
  composeDraft.value.projectName = project;
  createContainerOpen.value = true;
  composeError.value = '';
  composeLoading.value = true;
  composeDraft.value.content = '';
  try {
    if (project) {
      const projectContainers = containers.value.filter(c => c.labels['com.docker.compose.project'] === project);
      const values = [];
      for (const container of projectContainers) values.push(await api.inspectContainer(connectionId.value, container.id) as Record<string, any>);
      composeDraft.value.content = composeFromInspects(values);
    } else {
      composeDraft.value.content = 'services:\n  app:\n    image: nginx:latest\n    ports:\n      - "8080:80"\n';
    }
  } catch (e) { composeError.value = String(e); }
  finally { composeLoading.value = false; }
}
async function applyCompose() {
  const editing = !!composeEditingProject.value;
  submitting.value = true;
  try {
    const request: DockerComposeApplyRequest = {
      projectName: composeDraft.value.projectName.trim(),
      content: composeDraft.value.content,
      replaceExisting: editing,
    };
    const preview = await previewAndConfirm('apply_compose', { request }); if (!preview) return;
    const result = await mutationExecute<{ taskId: string }>(connectionId.value, 'apply_compose', { request }, preview);
    createContainerOpen.value = false; notifyTask(result.taskId);
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
      const result = await plugin.saveFile({ fileName, contentType: 'application/x-tar' }, merged.buffer);
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
  upsertTransfer({ ...task, status: 'cancelled' });
  await task.handle?.stop().catch(() => undefined);
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
  imageActionInFlight.value = { ...imageActionInFlight.value, [item.id]: 'remove' };
  try {
    const args = { imageId: item.id };
    const preview = await previewAndConfirm('remove_image', args); if (!preview) return;
    await mutationExecute(connectionId.value, 'remove_image', args, preview);
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
  const containerId = selectedContainer.value.id;
  const request = ++fileLoadSequence;
  ++filePreviewSequence;
  filePreviewLoading.value = false;
  fileLoading.value = true;
  fileError.value = '';
  filePreview.value = undefined;
  try {
    filePath.value = path;
    fileEntries.value = [];
    fileSelection.value = '';
    const entries = await api.listContainerFiles(connectionId.value, containerId, path);
    if (request === fileLoadSequence && selectedContainerId.value === containerId) fileEntries.value = entries;
  } catch (cause: any) {
    if (request === fileLoadSequence && selectedContainerId.value === containerId) fileError.value = cause?.message || String(cause);
  } finally {
    if (request === fileLoadSequence) fileLoading.value = false;
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
  const request = ++filePreviewSequence;
  const containerId = selectedContainer.value.id;
  filePreviewLoading.value = true;
  fileError.value = '';
  fileSelection.value = entry.path;
  try {
    const preview = await api.previewContainerFile(connectionId.value, containerId, entry.path);
    if (request === filePreviewSequence && selectedContainerId.value === containerId) filePreview.value = preview;
  } catch (cause: any) {
    if (request === filePreviewSequence && selectedContainerId.value === containerId) fileError.value = cause?.message || String(cause);
  } finally {
    if (request === filePreviewSequence) filePreviewLoading.value = false;
  }
}

let fileLoadSequence = 0, filePreviewSequence = 0;
function openFileMenu(event: MouseEvent | KeyboardEvent, entry?: DockerFileEntry) {
  if (fileLoading.value) return;
  fileSelection.value = entry?.path || '';
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  fileMenu.value = { open: true, x: 'clientX' in event ? event.clientX : rect.left + 24, y: 'clientY' in event ? event.clientY : rect.top + 24, entry };
}
async function fileMenuAction(action: string) {
  const entry = fileMenu.value.entry;
  fileMenu.value.open = false;
  if (action === 'open' && entry) return openFile(entry);
  if (action === 'download' && entry) return downloadFile(entry);
  if (action === 'copy') return copyValue(entry?.path || filePath.value);
  if (action === 'refresh') return loadFiles();
  if (isReadOnly.value) return;
  if (action === 'upload') return triggerUpload();
  if (action === 'delete' && entry && selectedContainer.value) {
    const containerId = selectedContainer.value.id;
    if (!await requestConfirmation(local(`删除 ${entry.path}${entry.kind === 'directory' ? ' 及其全部内容' : ''}？此操作无法撤销。`, `Delete ${entry.path}${entry.kind === 'directory' ? ' and all its contents' : ''}? This cannot be undone.`))) return;
    if (containerId !== selectedContainerId.value) return;
    try { await invoke(connectionId.value, 'docker/fileOperation', { containerId, operation: action, path: entry.path }); await loadFiles(); }
    catch (e) { toast(String(e), 5000); }
    return;
  }
  if (['mkdir', 'create', 'rename'].includes(action)) fileEdit.value = { open: true, operation: action, path: action === 'rename' ? entry!.path : filePath.value, name: action === 'rename' ? entry!.name : '', busy: false, error: '' };
}
async function saveFileEdit() {
  if (isReadOnly.value || !selectedContainer.value || fileEdit.value.busy) return;
  const draft = fileEdit.value;
  if (!draft.name || draft.name === '.' || draft.name === '..' || /[\/\0\r\n]/.test(draft.name)) { draft.error = local('请输入有效名称，不能包含斜杠或换行。', 'Enter a valid name without slashes or newlines.'); return; }
  draft.busy = true; draft.error = '';
  try {
    await invoke(connectionId.value, 'docker/fileOperation', { containerId: selectedContainer.value.id, operation: draft.operation, path: draft.operation === 'rename' ? draft.path : joinContainerPath(draft.path, draft.name), name: draft.name });
    draft.open = false; await loadFiles();
  } catch (e) { draft.error = String(e); } finally { draft.busy = false; }
}

// ---------- 容器文件传输 ----------

function upsertFileTransfer(task: FileTransferTask, target: { connectionId: string; containerId: string; name: string }, stop: () => Promise<void>) {
  upsertTransfer({ sessionId: task.sessionId, kind: task.direction === 'upload' ? 'file-upload' : 'file-download', direction: task.direction, image: task.name, status: task.status, bytesCompleted: task.bytes, bytesTotal: task.total }, { sessionId: task.sessionId, stop });
  transfers.value = transfers.value.map(item => item.sessionId === task.sessionId ? { ...item, contextLabel: target.name, containerPath: task.path } : item);
  transferOpen.value = true;
}
function patchFileTransfer(sessionId: string, patch: Partial<FileTransferTask> & { saving?: boolean }) {
  transfers.value = transfers.value.map(item => item.sessionId !== sessionId || cancelledTransferIds.has(sessionId) ? item : {
    ...item,
    ...(patch.bytes !== undefined ? { bytesCompleted: patch.bytes } : {}),
    ...(patch.total !== undefined ? { bytesTotal: patch.total } : {}),
    ...(patch.status !== undefined ? { status: patch.status, ...(patch.status !== 'running' ? { handle: undefined, saving: false } : {}) } : {}),
    ...(patch.error !== undefined ? { error: patch.error } : {}),
    ...(patch.savedPath !== undefined ? { savedPath: patch.savedPath, savedPathKind: patch.savedPathKind } : {}),
    ...(patch.saving !== undefined ? { saving: patch.saving } : {}),
  });
}

function joinContainerPath(directory: string, name: string): string {
  return directory.endsWith('/') ? `${directory}${name}` : `${directory}/${name}`;
}

async function downloadFile(entry: DockerFileEntry) {
  if (!selectedContainer.value) return;
  const target = { connectionId: connectionId.value, containerId: selectedContainer.value.id, name: containerName(selectedContainer.value) };
  const archive = entry.kind === 'directory';
  const fileName = archive ? `${entry.name}.tar` : entry.name;
  const sessionId = newSessionId();
  const chunks: Uint8Array[] = [];
  upsertFileTransfer({
    sessionId,
    name: fileName,
    path: entry.path,
    direction: 'download',
    status: 'running',
    bytes: 0,
    total: archive ? 0 : entry.size,
  }, target, async () => { unregisterStream(sessionId); chunks.length = 0; await api.stopStream(target.connectionId, sessionId); });
  registerFileStream(sessionId, (event) => {
    if (cancelledTransferIds.has(sessionId)) return;
    if (event.chunk.length) chunks.push(event.chunk);
    patchFileTransfer(sessionId, { bytes: event.bytesReceived, total: event.size || (archive ? 0 : entry.size) });
    if (!event.done) return;
    if (event.cancelled) { chunks.length = 0; patchFileTransfer(sessionId, { status: 'cancelled' }); return; }
    if (event.error) {
      unregisterStream(sessionId);
      chunks.length = 0;
      patchFileTransfer(sessionId, { status: 'error', error: event.error });
      toast(event.error, 5000);
      return;
    }
    void saveDownloadedFile(sessionId, fileName, chunks);
  });
  try {
    await api.startFileDownload(target.connectionId, target.containerId, entry.path, sessionId, archive);
    if (cancelledTransferIds.has(sessionId)) await api.stopStream(target.connectionId, sessionId);
  } catch (cause: any) {
    unregisterStream(sessionId);
    patchFileTransfer(sessionId, { status: 'error', error: cause?.message || String(cause) });
    toast(cause?.message || String(cause), 5000);
  }
}

async function saveDownloadedFile(sessionId: string, fileName: string, chunks: Uint8Array[]) {
  unregisterStream(sessionId);
  if (cancelledTransferIds.has(sessionId)) return;
  patchFileTransfer(sessionId, { saving: true });
  try {
    const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
    const merged = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      merged.set(chunk, offset);
      offset += chunk.length;
    }
    chunks.length = 0;
    const plugin = getPlugin();
    if (!plugin.saveFile) throw new Error(t('saveUnsupported'));
    const result = await plugin.saveFile({ fileName, contentType: 'application/octet-stream' }, merged.buffer);
    const savedPath = typeof result?.path === 'string' ? result.path : '';
    if (!savedPath) {
      // 用户在原生保存对话框中取消。
      patchFileTransfer(sessionId, { status: 'cancelled' });
      return;
    }
    const kind = savedPathKind(savedPath, fileName);
    patchFileTransfer(sessionId, { status: 'done', bytes: total, total, savedPath, savedPathKind: kind });
    toast(kind === 'desktop' ? t('savedTo', { path: directoryOf(savedPath) }) : t('fileDownloaded'), kind === 'desktop' ? 6000 : 2400);
  } catch (cause: any) {
    patchFileTransfer(sessionId, { status: 'error', error: cause?.message || String(cause) });
    toast(cause?.message || String(cause), 5000);
  }
}

function triggerUpload() {
  if (isReadOnly.value) {
    toast(t('readOnly'), 2400);
    return;
  }
  uploadInput.value?.click();
}

async function handleUploadSelection(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files ?? []);
  input.value = '';
  if (!files.length || !selectedContainer.value) return;
  const directory = filePath.value;
  const target = { connectionId: connectionId.value, containerId: selectedContainer.value.id, name: containerName(selectedContainer.value) };
  for (const file of files) { if (workbenchClosed) break; await uploadOne(file, joinContainerPath(directory, file.name), target); }
}

async function uploadOne(file: File, targetPath: string, target: { connectionId: string; containerId: string; name: string }) {
  if (file.size > MAX_FILE_TRANSFER_BYTES) {
    toast(t('fileTooLarge', { size: formatBytes(MAX_FILE_TRANSFER_BYTES) }), 5000);
    return;
  }
  const sessionId = newSessionId();
  let exitCode: number | undefined;
  let stderr = '';
  const abort = new AbortController();
  let finishUpload: (() => void) | undefined;
  let completionTimer: ReturnType<typeof setTimeout> | undefined;
  upsertFileTransfer({
    sessionId,
    name: file.name,
    path: targetPath,
    direction: 'upload',
    status: 'running',
    bytes: 0,
    total: file.size,
  }, target, async () => { abort.abort(); finishUpload?.(); unregisterStream(sessionId); await api.stopExec(target.connectionId, sessionId); });

  // 上传结束信号来自 docker-exec 通道的 done 帧（后端会带上退出码）。
  const finished = new Promise<void>((resolve) => {
    finishUpload = resolve;
    registerExecStream(sessionId, (event) => {
      if (event.chunk) stderr = (stderr + event.chunk).slice(-8192);
      if (event.error) { stderr = event.error; exitCode = 1; }
      if (!event.error && typeof event.exitCode === 'number') exitCode = event.exitCode;
      if (event.done) resolve();
    });
  });

  try {
    await api.startFileUpload(target.connectionId, target.containerId, targetPath, sessionId, file.size);
    if (abort.signal.aborted) { await api.stopExec(target.connectionId, sessionId); return; }
    const buffer = new Uint8Array(await file.arrayBuffer());
    await sendExecBytes(sessionId, buffer, (sent) => patchFileTransfer(sessionId, { bytes: sent }), abort.signal);
    completionTimer = setTimeout(() => { exitCode = 1; stderr = 'Upload completion timed out'; finishUpload?.(); }, 120000);
    await finished;
    if (abort.signal.aborted) return;
    if ((exitCode ?? 0) !== 0) {
      throw new Error(stderr.trim() || t('uploadFailed', { code: exitCode ?? -1 }));
    }
    patchFileTransfer(sessionId, { status: 'done', bytes: file.size, total: file.size });
    toast(t('fileUploaded', { name: file.name }), 3000);
    if (selectedContainerId.value === target.containerId && connectionId.value === target.connectionId && filePath.value === parentPath(targetPath)) await loadFiles();
  } catch (cause: any) {
    void api.stopExec(target.connectionId, sessionId).catch(() => undefined);
    if (abort.signal.aborted) return;
    const message = cause?.message || String(cause);
    patchFileTransfer(sessionId, { status: 'error', error: message });
    toast(message, 5000);
  } finally {
    clearTimeout(completionTimer);
    unregisterStream(sessionId);
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
  void loadColumnWidths();
  applyHostLocale();
  const context = plugin.context;
  connectionName.value = context.connection?.name || '';
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
  workbenchClosed = true;
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
watch([connectionId, resource, selectedRowKey, selectedContainerId, locale], () => {
  const plugin = getPlugin();
  if (!connectionId.value || !plugin.capabilities?.aiRecommendations || !plugin.ai) return;
  const id = selectedContainerId.value || selectedRowKey.value;
  void plugin.ai.setRecommendations(recommendations(resource.value, id, locale.value === 'zh-CN')).catch(() => {});
});
onUnmounted(() => { const plugin = getPlugin(); void plugin.ai?.clearRecommendations().catch(() => {}); });

const mutationDialog = ref(false), pendingPreview = ref<MutationPreview>(), pendingOperation = ref('');
let resolveMutation: ((confirmed: boolean) => void) | undefined;
async function previewAndConfirm(operation: string, args: Record<string, unknown>) {
  const preview = await mutationPreview(connectionId.value, operation, args);
  pendingPreview.value = preview; pendingOperation.value = operation; mutationDialog.value = true;
  return (await new Promise<boolean>(resolve => { resolveMutation = resolve; })) ? preview : undefined;
}
function settleMutation(confirmed: boolean) { mutationDialog.value = false; resolveMutation?.(confirmed); resolveMutation = undefined; }
function notifyTask(id: string) { toast(local('任务已提交：', 'Task started: ') + id, 4000); window.dispatchEvent(new CustomEvent('docker-task-started')); }
watch(mutationDialog, value => { if (!value) settleMutation(false); });
onUnmounted(() => settleMutation(false));
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
        <AutomationPanel :connection-id="connectionId" :name="connectionName" :read-only="isReadOnly" :production="isProduction" />
        <button class="icon-btn icon-cyan" :title="t('engineJson')" @click="loadEngineDetails('json')"><Icon name="settings" /></button>
        <button class="icon-btn icon-amber" :title="t('engineInformation')" @click="loadEngineDetails('summary')"><Icon name="circle-help" /></button>
        <button class="icon-btn icon-emerald" :title="t('diskUsage')" @click="openDiskUsage"><Icon name="hard-drive" /></button>
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
                  <span class="transfer-image" :title="task.containerPath || task.image">{{ task.image }}</span>
                  <span class="transfer-direction">{{ task.direction === 'upload' ? t('upload') : t('download') }}</span>
                  <span class="transfer-percent">{{ transferPercent(task) == null ? '—' : `${Math.round(transferPercent(task)!)}%` }}</span>
                  <button v-if="task.status === 'running' && !task.saving" class="icon-btn icon-xs" :title="t('cancel')" :aria-label="t('cancel')" @click="cancelTransfer(task)"><Icon name="x" /></button>
                </div>
                <div class="transfer-bar">
                  <div v-if="transferPercent(task) != null" class="transfer-bar-fill" :style="{ width: `${transferPercent(task)}%` }" />
                  <div v-else-if="task.status === 'running'" class="docker-indeterminate transfer-bar-fill transfer-bar-ind" />
                </div>
                <div v-if="task.contextLabel" class="transfer-context truncate" :title="task.containerPath">{{ task.contextLabel }} · {{ task.containerPath }}</div>
                <div class="transfer-meta">
                  <span class="truncate">{{ task.saving && task.status === 'running' ? local('等待保存', 'Waiting to save') : t(`transferStatus.${task.status}`) }}</span>
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
          <LoadingSkeleton v-if="detailTab === 'overview' && detailLoading" :rows="8" :columns="2" />
          <ContainerOverview v-else-if="detailTab === 'overview'" :container="selectedContainer" :inspect="inspect" :connection-id="connectionId" @copy="copyValue" />

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

          <div v-else-if="detailTab === 'monitoring'" class="monitoring-pane">
            <div class="monitor-heading"><div><h2>{{ local('资源监控', 'Resource monitoring') }}</h2><p>{{ local('CPU、内存与网络 / 磁盘吞吐', 'CPU, memory and network / disk throughput') }}</p></div><span class="monitor-live"><i :class="{ active: isRunning(selectedContainer) }" />{{ isRunning(selectedContainer) ? local('每 2 秒采样', 'Every 2 seconds') : local('容器已停止', 'Container stopped') }}</span></div>
            <div class="monitor-summary">
              <div class="docker-card">
                <span>CPU</span>
                <strong>{{ latestSample ? `${latestSample.cpuPercent.toFixed(1)}%` : '—' }}</strong>
              </div>
              <div class="docker-card">
                <span>{{ t('memory') }}</span>
                <strong>{{ latestSample ? `${formatBytes(latestSample.memoryUsage)} / ${formatBytes(latestSample.memoryLimit)}` : '—' }}</strong>
                <small v-if="latestSample">{{ latestSample.memoryPercent.toFixed(1) }}%</small>
              </div>
              <div class="docker-card">
                <span>{{ t('networkIo') }}</span>
                <strong class="mono-xs">↓ {{ formatRate(latestNetworkRate.rx) }} · ↑ {{ formatRate(latestNetworkRate.tx) }}</strong>
              </div>
              <div class="docker-card">
                <span>{{ t('blockIo') }}</span>
                <strong class="mono-xs">{{ t('blockReadShort') }} {{ formatRate(latestBlockRate.read) }} · {{ t('blockWriteShort') }} {{ formatRate(latestBlockRate.write) }}</strong>
              </div>
            </div>
            <LoadingSkeleton v-if="!trend.length && isRunning(selectedContainer)" :rows="6" :columns="2" /><div v-else-if="!trend.length" class="muted-text monitor-empty">{{ local('启动容器后可查看实时指标', 'Start the container to view live metrics') }}</div>
            <div v-else class="monitoring-grid">
              <MetricChart
                title="CPU %"
                :labels="monitorLabels"
                :series="cpuChartSeries"
                :max="Math.max(100, Math.ceil(Math.max(...trend.map(point => point.cpuPercent)) / 100) * 100)"
                :axis-interval="25"
                :value-formatter="(value) => `${value.toFixed(0)}%`"
              />
              <MetricChart
                :title="t('memory')"
                :labels="monitorLabels"
                :series="memoryChartSeries"
                :value-formatter="(value) => `${value.toFixed(0)} MiB`"
              />
              <MetricChart :title="t('networkIo')" :labels="monitorLabels" :series="networkChartSeries" :value-formatter="formatRate" />
              <MetricChart :title="t('blockIo')" :labels="monitorLabels" :series="blockIoChartSeries" :value-formatter="formatRate" />
            </div>
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
                <span class="files-path" :title="filePath">{{ filePath }}</span>
                <button class="btn btn-outline btn-sm" :disabled="isReadOnly || !selectedContainer" :title="t('uploadFile')" @click="triggerUpload">
                  <Icon name="upload" />{{ t('uploadFile') }}
                </button>
                <input ref="uploadInput" type="file" multiple class="hidden-file-input" @change="handleUploadSelection" />
                <button class="btn btn-ghost btn-sm" :disabled="fileLoading" @click="loadFiles()"><Icon name="refresh-cw" :class="{ spin: fileLoading }" /></button>
              </div>
              <div v-if="fileError" class="error-text files-error">{{ fileError }}</div>
              <LoadingSkeleton v-else-if="fileLoading" :columns="2" />
              <div v-else class="files-scroll" @contextmenu.prevent="openFileMenu($event)">
                <div v-if="!fileEntries.length" class="file-preview-empty">{{ local('空文件夹 · 右键新建或上传', 'Empty folder · right-click to create or upload') }}</div>
                <div
                  v-for="entry in fileEntries"
                  :key="entry.path"
                  class="file-row"
                  :class="{ 'row-selected': fileSelection === entry.path }"
                  @click="fileSelection = entry.path"
                  @dblclick="openFile(entry)"
                  tabindex="0"
                  @contextmenu.prevent.stop="openFileMenu($event, entry)"
                  @keydown.enter="openFile(entry)"
                  @keydown.shift.f10.prevent="openFileMenu($event, entry)"
                >
                  <Icon v-if="entry.kind === 'directory'" name="folder" class="file-icon-folder" />
                  <Icon v-else name="file" class="file-icon-file" />
                  <span class="file-name truncate">{{ entry.name }}</span>
                  <span class="file-size">{{ entry.kind === 'directory' ? '' : formatBytes(entry.size) }}</span>
                  <button
                    v-if="entry.kind !== 'directory'"
                    class="docker-copy-button"
                    :title="t('download')"
                    @click.stop="downloadFile(entry)"
                  >
                    <Icon name="download" />
                  </button>
                </div>
              </div>
            </div>
            <div class="file-preview">
              <LoadingSkeleton v-if="filePreviewLoading" :rows="10" :columns="1" />
              <div v-else-if="filePreview?.dataBase64 && filePreview.mimeType" class="image-preview"><div class="preview-caption truncate">{{ filePreview.path }}</div><img :src="`data:${filePreview.mimeType};base64,${filePreview.dataBase64}`" :alt="filePreview.path" /><span class="hint-text">{{ filePreview.mimeType }}</span></div>
              <div v-else-if="filePreview?.binary" class="muted-text">{{ filePreview.truncated ? local('文件超出预览上限，请下载查看。', 'File exceeds the preview limit; download to view.') : t('binaryPreviewUnsupported') }}</div>
              <template v-else-if="filePreview"><div v-if="filePreview.truncated" class="preview-truncated">{{ local('仅预览前 2 MiB；可下载完整文件。', 'Showing the first 2 MiB; download for the complete file.') }}</div><CodeEditor :key="filePreview.path" :model-value="filePreview.content" :path="filePreview.path" read-only height="100%" /></template>
              <div v-else class="file-preview-empty">{{ t('selectFile') }}</div>
            </div>
          </div>
        </div>
      </template>

      <template v-else>
        <div class="toolbar">
          <div class="search-box">
            <Icon name="search" class="search-icon" />
            <input v-model="query" class="input search-input search-clearable" :placeholder="t('search')" @keydown.esc="query = ''" />
            <button v-if="query" class="icon-btn search-clear" :title="local('清除搜索', 'Clear search')" :aria-label="local('清除搜索', 'Clear search')" @click="query = ''"><Icon name="x" /></button>
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

        <div class="table-area" :aria-busy="loading">
          <LoadingSkeleton v-if="loading && !(resource === 'containers' ? containers : resource === 'images' ? images : resource === 'volumes' ? volumes : networks).length" :rows="10" :columns="columnWidths[resource].length" />
          <table v-else-if="resource === 'containers'" class="docker-table" :style="tableStyle('containers')">
            <colgroup>
              <col v-for="(width, index) in columnWidths.containers" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <thead>
              <tr>
                <ResizableHeader :width="columnWidths.containers[0]" @resize="resizeColumn('containers', 0, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[1]" @resize="resizeColumn('containers', 1, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[2]" @resize="resizeColumn('containers', 2, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('image')">{{ t('image') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[3]" @resize="resizeColumn('containers', 3, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('ports')">{{ t('ports') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[4]" @resize="resizeColumn('containers', 4, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('cpu')">CPU<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[5]" @resize="resizeColumn('containers', 5, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('memory')">{{ t('memory') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.containers[6]" @resize="resizeColumn('containers', 6, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('status')">{{ t('status') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader class="th-right" :width="columnWidths.containers[7]" @resize="resizeColumn('containers', 7, $event)" @commit="saveColumnWidths">{{ t('actions') }}</ResizableHeader>
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
                <tr v-for="container in expandedProjects.has(project) ? values : []" :key="container.id" class="compose-child" :class="{ 'row-selected': selectedRowKey === container.id }" @click="selectedRowKey = container.id">
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
                  <td><ContainerPorts :container="container" :connection-id="connectionId" /></td>
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
              <tr v-for="container in standaloneContainers" :key="container.id" :class="{ 'row-selected': selectedRowKey === container.id }" @click="selectedRowKey = container.id">
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
                <td><ContainerPorts :container="container" :connection-id="connectionId" /></td>
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
            <thead>
              <tr>
                <ResizableHeader :width="columnWidths.images[0]" @resize="resizeColumn('images', 0, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('name')">{{ t('repositoryTag') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.images[1]" @resize="resizeColumn('images', 1, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.images[2]" @resize="resizeColumn('images', 2, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('size')">{{ t('size') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.images[3]" @resize="resizeColumn('images', 3, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('created')">{{ t('created') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader class="th-right" :width="columnWidths.images[4]" @resize="resizeColumn('images', 4, $event)" @commit="saveColumnWidths">{{ t('actions') }}</ResizableHeader>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredImages" :key="item.id" :class="{ 'row-selected': selectedRowKey === item.id }" @click="selectedRowKey = item.id">
                <td :title="item.repoTags.join(', ')">
                  <div class="docker-copy-cell docker-truncated-cell">
                    <button class="resource-link truncate" @click.stop="openResourceDetails('images', item.id)">{{ item.repoTags.join(', ') || '<none>' }}</button
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
            <thead>
              <tr>
                <ResizableHeader :width="columnWidths.volumes[0]" @resize="resizeColumn('volumes', 0, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.volumes[1]" @resize="resizeColumn('volumes', 1, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('driver')">{{ t('driver') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.volumes[2]" @resize="resizeColumn('volumes', 2, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('scope')">{{ t('scope') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.volumes[3]" @resize="resizeColumn('volumes', 3, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('mountpoint')">{{ t('mountpoint') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredVolumes" :key="item.name" :class="{ 'row-selected': selectedRowKey === item.name }" @click="selectedRowKey = item.name">
                <td class="cell-strong" :title="item.name"><button class="resource-link docker-truncated-cell" @click.stop="openResourceDetails('volumes', item.name)">{{ item.name }}</button></td>
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
            <thead>
              <tr>
                <ResizableHeader :width="columnWidths.networks[0]" @resize="resizeColumn('networks', 0, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('name')">{{ t('name') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.networks[1]" @resize="resizeColumn('networks', 1, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('id')">ID<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.networks[2]" @resize="resizeColumn('networks', 2, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('driver')">{{ t('driver') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.networks[3]" @resize="resizeColumn('networks', 3, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('scope')">{{ t('scope') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.networks[4]" @resize="resizeColumn('networks', 4, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('internal')">{{ t('internal') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
                <ResizableHeader :width="columnWidths.networks[5]" @resize="resizeColumn('networks', 5, $event)" @commit="saveColumnWidths"><button class="docker-sort" @click="toggleSort('attachable')">{{ t('attachable') }}<Icon name="arrow-up-down" /></button></ResizableHeader>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredNetworks" :key="item.id" :class="{ 'row-selected': selectedRowKey === item.id }" @click="selectedRowKey = item.id">
                <td class="cell-strong"><button class="resource-link" @click.stop="openResourceDetails('networks', item.id)">{{ item.name }}</button></td>
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

    <Dialog :open="mutationDialog" content-class="dlg-wide automation-dialog" @update:open="settleMutation(false)">
      <div class="dlg-header"><div class="dlg-title">{{ local('操作预览', 'Operation preview') }} · {{ pendingOperation }}</div><div class="dlg-desc">{{ connectionName || connectionId }} · {{ isProduction ? local('生产环境', 'Production') : '' }}</div></div>
      <p class="hint-text">{{ local('仅执行预览中的范围；五分钟内有效，目标变化后需重新预览。', 'Execution is bound to this preview for five minutes; changed targets require a new preview.') }}</p>
      <div class="automation-scroll"><JsonTree :value="pendingPreview?.preview" :initial-expanded-depth="2" /></div>
      <div class="dlg-footer"><button class="btn btn-outline" @click="settleMutation(false)">{{ t('cancel') }}</button><button class="btn btn-danger" @click="settleMutation(true)">{{ local('确认执行', 'Confirm execution') }}</button></div>
    </Dialog>
    <ResourceDetails v-model:open="resourceDetailsOpen" :connection-id="connectionId" :kind="resourceDetailsKind" :resource-id="resourceDetailsId" :read-only="isReadOnly" @changed="loadResource(resource)" />
    <FileContextMenu :open="fileMenu.open" :x="fileMenu.x" :y="fileMenu.y" :entry="fileMenu.entry" :read-only="isReadOnly" @close="fileMenu.open = false" @action="fileMenuAction" />
    <Dialog :open="fileEdit.open" @update:open="!fileEdit.busy && (fileEdit.open = $event)">
      <div class="dlg-header"><div class="dlg-title">{{ fileEditTitle }}</div><div class="dlg-desc break-all">{{ fileEdit.path }}</div></div>
      <form @submit.prevent="saveFileEdit">
        <label class="docker-field"><span>{{ t('name') }}</span><input v-model="fileEdit.name" class="input" autofocus :disabled="fileEdit.busy" /></label>
        <p v-if="fileEdit.error" class="error-text" role="alert">{{ fileEdit.error }}</p>
        <div class="dlg-footer"><button type="button" class="btn btn-outline" :disabled="fileEdit.busy" @click="fileEdit.open = false">{{ t('cancel') }}</button><button class="btn btn-primary" :disabled="fileEdit.busy || !fileEdit.name || isReadOnly">{{ local('确定', 'Confirm') }}</button></div>
      </form>
    </Dialog>
    <Dialog :open="createContainerOpen" content-class="dlg-wide" @update:open="createContainerOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ composeEditingProject ? t('editCompose') : t('createContainer') }}</div>
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
        <LoadingSkeleton v-if="composeLoading" :rows="8" :columns="1" />
        <p v-if="composeError" class="error-text" role="alert">{{ composeError }}</p>
        <label class="docker-field"><span>{{ t('composeProject') }}</span><input v-model="composeDraft.projectName" class="input" :disabled="!!composeEditingProject" placeholder="my-project" /></label>
        <CodeEditor v-if="!composeLoading" v-model="composeDraft.content" path="compose.yaml" height="420px" />
        <p v-if="composeEditingProject" class="hint-text">{{ local('根据当前容器配置生成 YAML，原文件的注释与变量不可恢复。保存前请检查；不支持的配置会在预览时提示。', 'YAML is generated from current containers; original comments and variables are unavailable. Review before saving; unsupported settings are rejected during preview.') }}</p>
        <p class="hint-text">{{ t('composeSubsetHint') }}</p>
      </div>
      <div class="dlg-footer">
        <button class="btn btn-outline" @click="createContainerOpen = false">{{ t('cancel') }}</button>
        <button v-if="createMode === 'form'" class="btn btn-primary" :disabled="submitting || !createContainerDraft.name.trim() || !createContainerDraft.image.trim()" @click="createContainer">
          <Icon v-if="submitting" name="loader-circle" class="spin" />{{ t('create') }}
        </button>
        <button v-else class="btn btn-primary" :disabled="submitting || composeLoading || !!composeError || !composeDraft.projectName.trim() || !composeDraft.content.trim()" @click="applyCompose">
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

    <Dialog :open="engineJsonOpen" dismissible content-class="dlg-xwide" @update:open="engineJsonOpen = $event">
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
        <LoadingSkeleton v-if="engineDetailsLoading" :rows="8" :columns="2" />
        <pre v-else-if="engineJsonSearch" class="mono-xs wrap-all">{{ filteredEngineJson }}</pre>
        <JsonTree v-else :value="engineJson" :initial-expanded-depth="2" />
      </div>
    </Dialog>

    <Dialog :open="engineSummaryOpen" dismissible content-class="dlg-medium" @update:open="engineSummaryOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('engineInformation') }}</div>
      </div>
      <LoadingSkeleton v-if="engineDetailsLoading" :rows="8" :columns="2" />
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
      <LoadingSkeleton v-if="historyLoading" :columns="3" />
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

    <Dialog :open="diskUsageOpen" dismissible content-class="dlg-wide" @update:open="diskUsageOpen = $event">
      <div class="dlg-header">
        <div class="dlg-title">{{ t('diskUsage') }}</div>
        <div class="dlg-desc">{{ t('diskUsageDescription') }}</div>
      </div>

      <div class="disk-dialog">
        <div v-if="diskUsageError" class="error-text">{{ diskUsageError }}</div>
        <LoadingSkeleton v-else-if="diskUsageLoading && !diskUsage" :rows="4" :columns="4" />
        <table v-else-if="diskUsage" class="disk-table">
          <thead>
            <tr>
              <th>{{ t('resource') }}</th>
              <th>{{ t('count') }}</th>
              <th>{{ t('size') }}</th>
              <th>{{ t('reclaimable') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in diskUsageRows" :key="row.key">
              <td class="disk-name">{{ t(row.key) }}</td>
              <td class="disk-count">{{ row.count }}</td>
              <td class="disk-size">{{ formatBytes(row.size) }}</td>
              <td class="disk-reclaim">{{ formatBytes(row.reclaimable) }}</td>
            </tr>
          </tbody>
          <tfoot>
            <tr>
              <td colspan="2">{{ t('storageTotal') }}</td>
              <td colspan="2">{{ formatBytes(diskUsage.layersSize) }}</td>
            </tr>
          </tfoot>
        </table>

        <div class="danger-banner">
          <Icon name="circle-help" />
          <span>{{ t('pruneWarningBanner') }}</span>
        </div>

        <div class="prune-choices">
          <button
            v-for="choice in pruneChoices"
            :key="choice.value"
            class="prune-choice"
            :class="{ active: pruneChoice === choice.value, 'prune-choice-danger': choice.risky }"
            :disabled="isReadOnly"
            @click="selectPruneChoice(choice.value)"
          >
            <span class="prune-choice-label">{{ choice.label }}</span>
            <span class="prune-choice-hint">{{ choice.hint }}</span>
          </button>
        </div>

        <div class="prune-preview">
          <div class="prune-preview-head">
            <span>{{ t('prunePreviewTitle') }}</span>
            <button class="btn btn-ghost btn-sm" :disabled="prunePreviewLoading" @click="loadPrunePreview">
              <Icon name="refresh-cw" :class="{ spin: prunePreviewLoading }" />
            </button>
          </div>
          <div v-if="prunePreviewError" class="error-text">{{ prunePreviewError }}</div>
          <div v-else-if="prunePreviewLoading && !prunePreview" class="muted-text">{{ t('waitingForLogs') }}</div>
          <template v-else-if="prunePreview">
            <div class="prune-preview-summary">
              <strong>{{ t('prunePreviewCount', { count: prunePreview.count }) }}</strong>
              <span class="muted">{{ t('prunePreviewSize', { size: formatBytes(prunePreview.totalSize) }) }}</span>
            </div>
            <div v-if="!prunePreview.count" class="prune-preview-empty">{{ t('pruneNothing') }}</div>
            <ul v-else class="prune-preview-list">
              <li v-for="(item, index) in prunePreview.items" :key="`${item.id}-${index}`" class="prune-preview-item">
                <span class="prune-preview-name truncate" :title="item.name || item.id">{{ item.name || item.id }}</span>
                <span class="prune-preview-id mono-xs">{{ item.id }}</span>
                <span v-if="item.detail" class="prune-preview-detail truncate" :title="item.detail">{{ item.detail }}</span>
                <span class="prune-preview-size mono-xs">{{ item.size ? formatBytes(item.size) : '' }}</span>
              </li>
            </ul>
            <div v-if="prunePreview.truncated" class="muted-text xs-strong">{{ t('prunePreviewTruncated', { shown: prunePreview.items.length }) }}</div>
          </template>
        </div>

        <label class="docker-field">
          <span>{{ t('pruneConfirmLabel', { phrase: pruneConfirmPhrase }) }}</span>
          <input v-model="pruneConfirmText" class="input" :placeholder="pruneConfirmPhrase" autocomplete="off" spellcheck="false" />
        </label>
        <div v-if="pruneConfirmText && !prunePhraseMatches" class="error-text">{{ t('pruneConfirmMismatch', { phrase: pruneConfirmPhrase }) }}</div>
      </div>

      <div class="dlg-footer">
        <button class="btn btn-outline" @click="diskUsageOpen = false">{{ t('cancel') }}</button>
        <button class="btn btn-danger" :disabled="!canExecutePrune" @click="executePrune">
          <Icon v-if="pruneRunning" name="loader-circle" class="spin" />
          {{ t('pruneConfirmAction') }}
        </button>
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

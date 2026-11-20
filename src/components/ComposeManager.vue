<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { invoke } from '../bridge';
import { local, mutationExecute, type MutationPreview, type Task } from '../automation';
import { fileName, filterProjects, projectStatus, type ComposeAction, type ComposeProject as Project, type ComposeSource as Source } from '../composeProjects';
import type { DockerContainer } from '../types';
import { copyToClipboard, shortId } from '../format';
import Icon from './Icon.vue';
import CodeEditor from './CodeEditor.vue';
import Dialog from './Dialog.vue';
import Popover from './Popover.vue';
import LoadingSkeleton from './LoadingSkeleton.vue';

const props = defineProps<{ connectionId: string; readOnly: boolean; containers: DockerContainer[]; confirm: (operation: string, args: Record<string, unknown>) => Promise<MutationPreview | undefined>; discard: () => Promise<boolean> }>();
const emit = defineEmits<{ fallback: [project: string]; create: []; changed: []; container: [id: string] }>();
const projects = ref<Project[]>([]), selected = ref<Project>(), source = ref<Source>(), draft = ref(''), fileIndex = ref(0), query = ref('');
const tab = ref<'overview' | 'source'>('overview'), loading = ref(false), sourceLoading = ref(false), busy = ref(false);
const listError = ref(''), sourceError = ref(''), actionError = ref(''), helpOpen = ref(false), moreOpen = ref(false), task = ref<Task>();
const taskProject = ref('');
let listSequence = 0, sourceSequence = 0, closed = false, timer: ReturnType<typeof setTimeout> | undefined;
const dirty = computed(() => !!source.value?.editable && draft.value !== source.value.files[0]?.content);
const file = computed(() => source.value?.files[fileIndex.value]);
const filtered = computed(() => filterProjects(projects.value, query.value));
const members = computed(() => props.containers.filter(c => c.labels['com.docker.compose.project'] === selected.value?.name));
const status = computed(() => selected.value && projectStatus(selected.value));
const taskLabel = computed(() => task.value?.status === 'running' ? local('正在处理编排', 'Working on project') : task.value?.status === 'completed' ? local('操作已完成', 'Operation completed') : task.value?.status === 'cancelled' ? local('已取消，请核对项目状态', 'Cancelled; check project state') : local('操作未完成', 'Operation did not complete'));
const canManage = computed(() => !!source.value && !sourceLoading.value && !props.readOnly && !busy.value);
const sourceIssue = computed(() => selected.value?.conflict ? local('项目的文件路径记录不一致', 'Project source paths disagree') : !selected.value?.files.length ? local('没有找到原始编排文件', 'Original Compose file not found') : local('暂时无法读取原始文件', 'Unable to read original files'));

async function refresh() {
  const request = ++listSequence; loading.value = true; listError.value = '';
  try {
    const result = await invoke<Project[]>(props.connectionId, 'docker/composeProjects');
    if (closed || request !== listSequence) return;
    projects.value = result;
    if (selected.value) selected.value = result.find(p => p.name === selected.value?.name) || selected.value;
  } catch (e) { if (!closed && request === listSequence) listError.value = String(e); }
  finally { if (!closed && request === listSequence) loading.value = false; }
}
async function readSource(name: string) {
  const request = ++sourceSequence; sourceLoading.value = true; sourceError.value = ''; source.value = undefined; fileIndex.value = 0;
  try {
    const result = await invoke<Source>(props.connectionId, 'docker/composeSource', { project: name });
    if (closed || request !== sourceSequence || selected.value?.name !== name) return;
    source.value = result; selected.value = result.project; draft.value = result.files[0]?.content || '';
  } catch (e) { if (!closed && request === sourceSequence) sourceError.value = String(e); }
  finally { if (!closed && request === sourceSequence) sourceLoading.value = false; }
}
async function open(name: string, view: 'overview' | 'source' = 'source') {
  if (busy.value || (dirty.value && !(await props.discard()))) return;
  selected.value = projects.value.find(p => p.name === name) || { name, directory: '', files: [], containers: 0, running: 0, conflict: false };
  tab.value = view; moreOpen.value = false; task.value = undefined; actionError.value = ''; draft.value = '';
  await readSource(name);
}
async function back() {
  if (dirty.value && !(await props.discard())) return;
  ++sourceSequence; sourceLoading.value = false; selected.value = undefined; source.value = undefined; draft.value = ''; moreOpen.value = false;
  await refresh();
}
async function reloadSource() {
  if (busy.value || !selected.value || (dirty.value && !(await props.discard()))) return;
  await Promise.all([refresh(), readSource(selected.value.name)]);
}
async function discardChanges() { if (dirty.value && await props.discard()) draft.value = source.value?.files[0]?.content || ''; }
function chooseFile(index: number) { if (busy.value) return; fileIndex.value = index; if (!source.value?.editable) draft.value = source.value?.files[index]?.content || ''; }
async function poll(id: string, project: string) {
  if (closed) return;
  try {
    const result = await invoke<Task>(props.connectionId, 'docker/taskStatus', { taskId: id });
    if (closed) return;
    task.value = result;
    if (result.status === 'running') { timer = setTimeout(() => void poll(id, project), 1200); return; }
    busy.value = false;
    if (result.error) actionError.value = result.error;
    emit('changed'); await refresh();
    if (result.status === 'completed' && selected.value?.name === project) await readSource(project);
  } catch (e) { if (!closed) { actionError.value = String(e); busy.value = false; } }
}
async function run(action: ComposeAction) {
  if (!selected.value || !canManage.value || (dirty.value && action !== 'deploy')) return;
  busy.value = true; actionError.value = ''; moreOpen.value = false;
  const id = props.connectionId, name = selected.value.name;
  try {
    const args: Record<string, unknown> = { project: name, action };
    if (action === 'deploy' && dirty.value) { args.content = draft.value; args.sourceHash = source.value!.sourceHash; }
    const preview = await props.confirm('compose_project', args);
    if (!preview || id !== props.connectionId || closed) { busy.value = false; return; }
    const result = await mutationExecute<{taskId: string}>(id, 'compose_project', args, preview);
    taskProject.value = name;
    task.value = { id: result.taskId, tool: 'compose_project', status: 'running', createdAt: new Date().toISOString(), cancelRequested: false };
    // Keep Compose progress inline. The persistent task is also available in
    // the task center, without opening a modal over the editor.
    await poll(result.taskId, name);
  } catch (e) { if (!closed) { actionError.value = String(e); busy.value = false; } }
}
async function cancelTask() {
  if (!task.value || task.value.cancelRequested) return;
  try { await invoke(props.connectionId, 'docker/taskCancel', { taskId: task.value.id }); task.value.cancelRequested = true; }
  catch (e) { actionError.value = String(e); }
}
async function copyPath() { if (file.value) { try { await copyToClipboard(file.value.path); } catch (e) { actionError.value = String(e); } } }
onMounted(refresh);
onUnmounted(() => { closed = true; ++listSequence; ++sourceSequence; if (timer) clearTimeout(timer); });
defineExpose({ open, dirty, busy });
</script>

<template>
  <section class="compose-workbench">
    <template v-if="!selected">
      <div class="toolbar">
        <div class="search-box"><Icon name="search" class="search-icon" /><input v-model="query" class="input search-input search-clearable" :placeholder="local('搜索编排项目', 'Search projects')" @keydown.esc="query = ''" /><button v-if="query" class="icon-btn search-clear" :aria-label="local('清除搜索', 'Clear search')" @click="query = ''"><Icon name="x" /></button></div>
        <button class="btn btn-primary" :disabled="readOnly || busy" @click="emit('create')"><Icon name="plus" />{{ local('新建编排', 'New project') }}</button>
        <span class="compose-list-count">{{ local('共', 'Total') }} {{ projects.length }} {{ local('个项目', 'projects') }}</span>
        <button class="icon-btn ml-auto" :title="local('编排使用说明', 'Compose help')" @click="helpOpen = true"><Icon name="circle-help" /></button>
        <button class="btn btn-ghost" :disabled="loading" @click="refresh"><Icon name="refresh-cw" :class="{ spin: loading }" />{{ local('刷新', 'Refresh') }}</button>
      </div>
      <div v-if="busy && task" class="compose-task" role="status"><Icon name="loader-circle" class="spin" /><span>{{ local('正在处理编排', 'Working on project') }}</span><small>{{ taskProject }}</small><button class="btn btn-ghost btn-sm ml-auto" :disabled="task.cancelRequested" @click="cancelTask">{{ task.cancelRequested ? local('正在取消', 'Cancelling') : local('取消任务', 'Cancel task') }}</button></div>
      <div v-if="listError" class="error-banner" role="alert">{{ listError }}<button class="btn btn-ghost btn-sm" @click="refresh">{{ local('重试', 'Retry') }}</button></div>
      <div class="table-area">
        <LoadingSkeleton v-if="loading && !projects.length" :rows="8" :columns="6" />
        <table v-else-if="filtered.length" class="docker-table compose-table">
          <colgroup><col style="width:24%" /><col style="width:120px" /><col style="width:100px" /><col style="width:21%" /><col /><col style="width:140px" /></colgroup>
          <thead><tr><th>{{ local('项目名称', 'Project') }}</th><th>{{ local('状态', 'Status') }}</th><th>{{ local('容器', 'Containers') }}</th><th>{{ local('编排文件', 'Compose files') }}</th><th>{{ local('工作目录', 'Working directory') }}</th><th class="th-right">{{ local('操作', 'Actions') }}</th></tr></thead>
          <tbody><tr v-for="project in filtered" :key="project.name" @dblclick="open(project.name, 'overview')">
            <td><button class="compose-project-name resource-link" :disabled="busy" @click="open(project.name, 'overview')"><Icon name="layers" /><span class="truncate">{{ project.name }}</span></button></td>
            <td><span class="docker-status" :class="projectStatus(project).key">{{ local(projectStatus(project).zh, projectStatus(project).en) }}</span></td>
            <td class="compose-container-count"><strong>{{ project.running }}</strong><span> / {{ project.containers }}</span></td>
            <td><span v-if="project.conflict" class="compose-conflict"><Icon name="triangle-alert" />{{ local('路径冲突', 'Path conflict') }}</span><span v-else class="compose-file-cell truncate" :title="project.files.join('\n')">{{ project.files.length ? fileName(project.files[0]!) : local('未识别源文件', 'No source file') }}<small v-if="project.files.length > 1">+{{ project.files.length - 1 }}</small></span></td>
            <td class="mono-xs compose-path" :title="project.directory">{{ project.directory || '—' }}</td>
            <td><div class="row-actions"><button class="icon-btn icon-sm" :disabled="busy" :title="local('编辑编排', 'Edit Compose')" @click="open(project.name, 'source')"><Icon name="file-code" /></button><button class="btn btn-ghost btn-sm" :disabled="busy" @click="open(project.name, 'overview')">{{ local('详情', 'Details') }}</button></div></td>
          </tr></tbody>
        </table>
        <div v-else class="compose-empty"><span class="compose-empty-icon"><Icon name="layers" /></span><h3>{{ query ? local('没有匹配的项目', 'No matching projects') : local('暂无编排项目', 'No Compose projects') }}</h3><p>{{ query ? local('试试项目名称或文件路径。', 'Try a project name or file path.') : local('创建编排，或刷新已连接 Docker 中的项目。', 'Create a project or refresh projects from Docker.') }}</p><button v-if="query" class="btn btn-outline" @click="query = ''">{{ local('清空筛选', 'Clear search') }}</button><button v-else class="btn btn-primary" :disabled="readOnly" @click="emit('create')"><Icon name="plus" />{{ local('新建编排', 'New project') }}</button></div>
      </div>
    </template>

    <template v-else>
      <header class="detail-header compose-detail-header">
        <button class="btn btn-ghost btn-sm" @click="back"><Icon name="arrow-left" />{{ local('返回编排列表', 'Back to projects') }}</button><span class="detail-divider" />
        <div class="detail-title"><div class="detail-name">{{ selected.name }}</div></div><span v-if="status" class="docker-status" :class="status.key">{{ local(status.zh, status.en) }}</span>
        <div class="compose-header-actions">
          <button v-if="!selected.running" class="btn btn-outline btn-sm" :disabled="!canManage || dirty" @click="run('start')"><Icon name="play" />{{ local('启动', 'Start') }}</button>
          <button v-else class="btn btn-outline btn-sm" :disabled="!canManage || dirty" @click="run('stop')"><Icon name="square" />{{ local('停止', 'Stop') }}</button>
          <button class="icon-btn" :disabled="busy || sourceLoading" :title="local('刷新项目', 'Refresh project')" @click="reloadSource"><Icon name="refresh-cw" :class="{ spin: sourceLoading }" /></button>
          <Popover :open="moreOpen" @update:open="moreOpen = $event"><template #trigger><button class="icon-btn" :disabled="busy" :title="local('更多操作', 'More actions')"><Icon name="ellipsis" /></button></template><div class="compose-menu"><button :disabled="!canManage || dirty" @click="run('restart')"><Icon name="rotate-cw" />{{ local('重启项目', 'Restart project') }}</button><button @click="helpOpen = true; moreOpen = false"><Icon name="circle-help" />{{ local('使用说明', 'Help') }}</button><div class="compose-menu-divider" /><button class="compose-menu-danger" :disabled="!canManage || dirty" @click="run('down')"><Icon name="trash-2" />{{ local('移除项目', 'Remove project') }}</button></div></Popover>
        </div>
      </header>
      <nav class="detail-tabs"><button v-for="view in ['overview','source'] as const" :key="view" class="docker-detail-tab" :class="{ active: tab === view }" @click="tab = view">{{ view === 'overview' ? local('概览', 'Overview') : local('编排文件', 'Compose files') }}<span v-if="view === 'source' && dirty" class="compose-dirty-dot" /></button></nav>
      <div v-if="task" class="compose-task" :class="{ failed: ['failed','cancelled'].includes(task.status) }" role="status"><Icon :name="task.status === 'running' ? 'loader-circle' : task.status === 'completed' ? 'circle-check' : 'triangle-alert'" :class="{ spin: task.status === 'running' }" /><span>{{ taskLabel }}</span><small :title="task.id">{{ selected.name }}</small><button v-if="task.status === 'running'" class="btn btn-ghost btn-sm ml-auto" :disabled="task.cancelRequested" @click="cancelTask">{{ task.cancelRequested ? local('正在取消', 'Cancelling') : local('取消任务', 'Cancel task') }}</button></div>
      <div v-if="actionError" class="error-banner compose-action-error" role="alert"><strong>{{ local('操作未完成，请核对项目状态后重试', 'Operation did not complete; inspect project state before retrying') }}</strong><details><summary>{{ local('查看详细信息', 'Details') }}</summary>{{ actionError }}</details></div>

      <div v-if="tab === 'overview'" class="detail-body">
        <div class="container-overview compose-overview">
          <section class="overview-hero"><div class="overview-symbol"><Icon name="layers" /></div><div class="overview-identity"><h2>{{ selected.name }}</h2><div class="overview-image"><Icon name="file-code" /><span>{{ selected.files.map(fileName).join(' + ') || local('没有源文件记录', 'No source file recorded') }}</span></div><div class="overview-id"><span>{{ local('Compose 编排项目', 'Docker Compose project') }}</span></div></div><div class="overview-state"><span v-if="status" class="docker-status" :class="status.key">{{ local(status.zh, status.en) }}</span><span>{{ selected.running }} / {{ selected.containers }} {{ local('容器运行中', 'containers running') }}</span></div></section>
          <section class="overview-section"><h3><Icon name="folder" />{{ local('项目配置', 'Project configuration') }}</h3><dl class="overview-facts compose-project-facts"><div><dt>{{ local('工作目录', 'Working directory') }}</dt><dd class="mono-xs">{{ selected.directory || '—' }}</dd></div><div><dt>{{ local('配置来源', 'Configuration source') }}</dt><dd>{{ sourceLoading ? local('识别中', 'Detecting') : source ? local('原始 Compose 文件', 'Original Compose files') : local('源文件不可用', 'Source unavailable') }}</dd></div><div><dt>{{ local('编排文件', 'Compose files') }}</dt><dd>{{ selected.files.length }} {{ local('个文件', 'files') }}</dd></div><div><dt>{{ local('移除项目时', 'When removing') }}</dt><dd>{{ local('保留数据卷', 'Data volumes are retained') }}</dd></div></dl></section>
          <section class="overview-section compose-members"><h3><Icon name="box" />{{ local('项目容器', 'Project containers') }}<span class="overview-count">{{ members.length }}</span></h3><table v-if="members.length" class="docker-table"><thead><tr><th>{{ local('服务 / 容器', 'Service / container') }}</th><th>{{ local('镜像', 'Image') }}</th><th>{{ local('状态', 'Status') }}</th><th class="th-right">{{ local('操作', 'Actions') }}</th></tr></thead><tbody><tr v-for="member in members" :key="member.id"><td><strong>{{ member.labels['com.docker.compose.service'] || member.names[0]?.replace(/^\//, '') }}</strong><small class="compose-member-id">{{ shortId(member.id) }}</small></td><td class="compose-path" :title="member.image">{{ member.image }}</td><td><span class="docker-status" :class="member.state === 'running' ? 'running' : 'stopped'">{{ member.state === 'running' ? local('运行中', 'Running') : local('已停止', 'Stopped') }}</span></td><td class="th-right"><button class="btn btn-ghost btn-sm" @click="emit('container', member.id)">{{ local('查看容器', 'View container') }}</button></td></tr></tbody></table><div v-else class="overview-empty">{{ local('项目尚未部署，可在编排文件中预览部署。', 'No containers deployed. Open Compose files to preview deployment.') }}<button class="btn btn-ghost btn-sm" @click="tab = 'source'">{{ local('查看编排文件', 'Open Compose files') }}<Icon name="chevron-right" /></button></div></section>
        </div>
      </div>

      <div v-else class="compose-source-view">
        <LoadingSkeleton v-if="sourceLoading" :rows="12" :columns="1" />
        <template v-else-if="source">
          <div class="compose-file-tabs"><button v-for="(item, index) in source.files" :key="item.path" :class="{ active: fileIndex === index }" :title="item.path" @click="chooseFile(index)"><Icon name="file-code" />{{ fileName(item.path) }}<span v-if="source.editable && dirty" class="compose-dirty-dot" /></button><span class="compose-file-state">{{ readOnly || !source.editable ? local('只读', 'Read only') : dirty ? local('未保存', 'Unsaved changes') : local('已保存', 'Saved') }}</span></div>
          <div class="compose-source-path"><Icon name="folder" /><span class="truncate" :title="file?.path">{{ file?.path }}</span><button class="icon-btn icon-xs" :title="local('复制路径', 'Copy path')" @click="copyPath"><Icon name="copy" /></button><button class="icon-btn icon-xs" :title="local('编排使用说明', 'Compose help')" @click="helpOpen = true"><Icon name="circle-help" /></button></div>
          <div v-if="!source.editable" class="compose-inline-note"><Icon name="circle-help" />{{ local('多文件编排按此顺序合并部署。请在主机上编辑原文件。', 'Files deploy in this order. Edit the originals on the host.') }}</div>
          <div class="compose-editor-area"><CodeEditor :key="selected.name + fileIndex" v-model="draft" :path="fileName(file?.path || 'compose.yaml')" :read-only="readOnly || !source.editable || busy" height="100%" /></div>
          <footer class="compose-editor-footer"><span class="compose-save-status"><Icon :name="dirty ? 'pencil' : 'circle-check'" />{{ dirty ? local('部署前将保存当前修改', 'Changes will be saved before deployment') : local('原始编排文件', 'Original Compose file') }}</span><button class="btn btn-outline btn-sm" :disabled="!dirty || busy" @click="discardChanges">{{ local('还原更改', 'Discard changes') }}</button><button class="btn btn-primary btn-sm" :disabled="!canManage" @click="run('deploy')"><Icon :name="busy ? 'loader-circle' : 'play'" :class="{ spin: busy }" />{{ local('预览部署', 'Preview deployment') }}</button></footer>
        </template>
        <div v-else class="compose-empty compose-source-empty"><span class="compose-empty-icon"><Icon name="file-search" /></span><h3>{{ sourceIssue }}</h3><p>{{ local('确认主机文件仍存在，或导入 YAML 继续管理。', 'Check that host files still exist, or import YAML to continue.') }}</p><div class="compose-empty-actions"><button class="btn btn-primary" :disabled="readOnly || busy" @click="emit('fallback', selected.name)"><Icon name="file-down" />{{ local('导入容器快照', 'Import snapshot') }}</button><button class="btn btn-outline" :disabled="busy" @click="reloadSource"><Icon name="refresh-cw" />{{ local('重新读取', 'Retry') }}</button></div><p class="compose-empty-caption">{{ local('快照是当前配置的有限副本，无法还原原始注释和变量。', 'A snapshot is a limited copy of current settings; comments and variables cannot be recovered.') }}</p><details v-if="sourceError" class="compose-error-details"><summary>{{ local('查看读取原因', 'Why the source could not be read') }}</summary><p>{{ sourceError }}</p></details></div>
      </div>
    </template>

    <Dialog :open="helpOpen" dismissible content-class="dlg-medium compose-help" @update:open="helpOpen = $event"><div class="dlg-header"><div class="dlg-title">{{ local('编排使用说明', 'Working with Compose') }}</div><div class="dlg-desc">{{ local('沿用原始文件管理 Docker Compose 项目', 'Manage Docker Compose projects from their original files') }}</div></div><div class="compose-help-item"><Icon name="file-code" /><div><strong>{{ local('保留原始配置', 'Keep original configuration') }}</strong><p>{{ local('通过项目标签识别文件，保留注释与变量。单文件可在这里编辑，多文件在主机编辑后部署。', 'Project labels locate source files, preserving comments and variables. Edit single files here; edit multi-file projects on the host.') }}</p></div></div><div class="compose-help-item"><Icon name="network" /><div><strong>{{ local('连接与环境', 'Connection and environment') }}</strong><p>{{ local('读取原文件需要本地或 SSH Socket 连接及主机上的 Docker Compose。采用源目录 .env 或标签记录的环境文件；命令行临时变量和 profiles 请在主机确认。', 'Source access requires local or SSH socket access and host Docker Compose. It uses .env or recorded environment files; check shell-only variables and profiles on the host.') }}</p></div></div><div class="compose-help-item"><Icon name="shield-check" /><div><strong>{{ local('先预览，再执行', 'Review before executing') }}</strong><p>{{ local('写操作均需确认。部署前会保存修改；移除项目保留数据卷。失败或取消后请核对项目状态。', 'Writes require confirmation. Deployment saves edits first; removal retains data volumes. Check project state after failure or cancellation.') }}</p></div></div><div class="dlg-footer"><button class="btn btn-primary" @click="helpOpen = false">{{ local('知道了', 'Got it') }}</button></div></Dialog>
  </section>
</template>

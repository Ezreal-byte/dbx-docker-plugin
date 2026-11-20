<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import { invoke } from '../bridge';
import { local, mutationExecute, type MutationPreview, type Task } from '../automation';
import Icon from './Icon.vue';
import CodeEditor from './CodeEditor.vue';
import LoadingSkeleton from './LoadingSkeleton.vue';

type Project = { name: string; directory: string; files: string[]; containers: number; running: number; conflict: boolean };
type Source = { project: Project; files: {path: string; content: string}[]; editable: boolean; sourceHash: string };
const props = defineProps<{ connectionId: string; readOnly: boolean; confirm: (operation: string, args: Record<string, unknown>) => Promise<MutationPreview | undefined>; discard: () => Promise<boolean> }>();
const emit = defineEmits<{ fallback: [project: string]; create: []; changed: [] }>();
const projects = ref<Project[]>([]), selected = ref(''), source = ref<Source>(), draft = ref(''), fileIndex = ref(0);
const loading = ref(false), busy = ref(false), error = ref(''), task = ref<Task>();
let sequence = 0, closed = false, timer: ReturnType<typeof setTimeout> | undefined;
const dirty = computed(() => !!source.value?.editable && draft.value !== source.value.files[0]?.content);
const file = computed(() => source.value?.files[fileIndex.value]);
async function refresh() {
  const request = ++sequence; loading.value = true; error.value = '';
  try { const result = await invoke<Project[]>(props.connectionId, 'docker/composeProjects'); if (!closed && request === sequence) projects.value = result; }
  catch (e) { if (!closed && request === sequence) error.value = String(e); }
  finally { if (request === sequence) loading.value = false; }
}
async function open(name: string) {
  if (busy.value || (dirty.value && !(await props.discard()))) return;
  const request = ++sequence; selected.value = name; source.value = undefined; draft.value = ''; fileIndex.value = 0; loading.value = true; error.value = '';
  try { const result = await invoke<Source>(props.connectionId, 'docker/composeSource', { project: name }); if (!closed && request === sequence) { source.value = result; draft.value = result.files[0]?.content || ''; } }
  catch (e) { if (!closed && request === sequence) error.value = String(e); }
  finally { if (request === sequence) loading.value = false; }
}
async function poll() {
  if (!task.value || closed) return;
  try {
    const result = await invoke<Task>(props.connectionId, 'docker/taskStatus', { taskId: task.value.id });
    if (closed) return; task.value = result;
    if (result.status === 'running') { timer = setTimeout(poll, 1500); return; }
    busy.value = false;
    if (result.error) error.value = result.error;
    emit('changed'); await refresh();
    if (result.status === 'completed') {
      if (source.value?.editable) source.value.files[0]!.content = draft.value;
      await open(selected.value);
    }
  } catch (e) { if (!closed) { error.value = String(e); busy.value = false; } }
}
async function run(action: string) {
  if (!source.value || props.readOnly || busy.value) return;
  busy.value = true; error.value = '';
  const id = props.connectionId;
  try {
    const args: Record<string, unknown> = { project: selected.value, action };
    if (action === 'deploy' && dirty.value) { args.content = draft.value; args.sourceHash = source.value.sourceHash; }
    const preview = await props.confirm('compose_project', args);
    if (!preview || id !== props.connectionId || closed) { busy.value = false; return; }
    const result = await mutationExecute<{taskId: string}>(id, 'compose_project', args, preview);
    task.value = { id: result.taskId, tool: 'compose_project', status: 'running', createdAt: new Date().toISOString(), cancelRequested: false };
    window.dispatchEvent(new CustomEvent('docker-task-started')); await poll();
  } catch (e) { if (!closed) { error.value = String(e); busy.value = false; } }
}
watch(fileIndex, () => { if (!source.value?.editable) draft.value = file.value?.content || ''; });
onMounted(refresh);
onUnmounted(() => { closed = true; sequence++; if (timer) clearTimeout(timer); });
defineExpose({ open, dirty });
</script>

<template>
  <section class="compose-manager">
    <div class="compose-manager-toolbar"><h2>Compose</h2><span class="hint-text">{{ local('识别原始文件 · 主机原生 Compose v2', 'Original files · host Docker Compose v2') }}</span><div class="spacer" /><button class="btn btn-outline btn-sm" :disabled="loading || busy" @click="refresh"><Icon name="refresh-cw" />{{ local('刷新', 'Refresh') }}</button><button class="btn btn-primary btn-sm" :disabled="readOnly || busy" @click="emit('create')"><Icon name="plus" />{{ local('新建 / 导入 YAML', 'New / import YAML') }}</button></div>
    <p class="hint-text">{{ local('从项目标签识别文件位置。读取主机文件和完整编排管理需要本地或 SSH Socket 连接，以及 Docker Compose v2。TCP / 宿主隧道连接可查看项目并显式导入 YAML。移除项目保留数据卷。', 'Paths are discovered from project labels. Host file access and full management require local or SSH socket access and Docker Compose v2. TCP/tunnel connections can list projects and explicitly import YAML. Removing projects retains data volumes.') }}</p>
    <p v-if="error" class="error-text" role="alert">{{ error }}</p>
    <LoadingSkeleton v-if="loading" :rows="5" :columns="2" />
    <div v-else class="compose-manager-layout">
      <aside><button v-for="project in projects" :key="project.name" class="compose-project-item" :class="{ active: selected === project.name }" :disabled="busy" @click="open(project.name)"><strong><Icon name="layers" />{{ project.name }}</strong><small>{{ project.running }} / {{ project.containers }} {{ local('运行中', 'running') }}</small><small class="truncate">{{ project.directory || local('无源文件路径', 'No source path') }}</small><small v-if="project.conflict" class="error-text">{{ local('文件标签冲突', 'Conflicting labels') }}</small></button><p v-if="!projects.length" class="hint-text">{{ local('尚未发现 Compose 项目', 'No Compose projects found') }}</p></aside>
      <article>
        <template v-if="source"><h3>{{ selected }}</h3><p class="hint-text wrap-all">{{ source.project.directory }}</p><div class="compose-manager-toolbar"><button v-for="(item, index) in source.files" :key="item.path" class="btn btn-outline btn-sm" :class="{ active: fileIndex === index }" :disabled="busy" @click="fileIndex = index">{{ item.path }}</button></div>
          <p v-if="!source.editable" class="hint-text">{{ local('多文件项目按原有顺序合并部署；请在主机编辑各文件，此处只读。', 'Multiple files deploy in their original order. Edit them on the host; this view is read-only.') }}</p>
          <CodeEditor :key="selected + fileIndex" v-model="draft" :path="file?.path || 'compose.yaml'" :read-only="readOnly || !source.editable || busy" height="420px" />
          <p class="hint-text">{{ local('保留原文、注释和变量；部署由主机 Compose 解析 .env、依赖和网络。修改后部署会先原子保存文件。失败或取消可能留下部分容器变更，请核对后重试。', 'Original text, comments and variables are preserved. Host Compose resolves .env, dependencies and networks. Deploying edits atomically saves the file first. Failure or cancellation may leave partial changes; inspect before retrying.') }}</p>
          <div class="compose-manager-toolbar"><button v-for="action in ['deploy','start','stop','restart','down']" :key="action" class="btn btn-outline btn-sm" :class="{ 'btn-danger': action === 'down' }" :disabled="readOnly || busy || (dirty && action !== 'deploy')" @click="run(action)">{{ ({deploy:local('保存并部署','Save / deploy'),start:local('启动项目','Start'),stop:local('停止项目','Stop'),restart:local('重启项目','Restart'),down:local('移除项目（保留卷）','Remove (retain volumes)')} as Record<string,string>)[action] }}</button><span v-if="dirty" class="warn-text">{{ local('有未保存修改', 'Unsaved changes') }}</span></div>
        </template>
        <template v-else-if="selected"><p class="hint-text">{{ local('原始文件无法读取。可显式导入容器快照生成的 YAML；快照无法还原注释、变量或完整编排配置。', 'Original files could not be read. You can explicitly import a container snapshot as YAML; comments, variables and full orchestration cannot be recovered.') }}</p><button class="btn btn-outline" :disabled="readOnly || busy" @click="emit('fallback', selected)">{{ local('导入容器快照（有限子集）', 'Import container snapshot (subset)') }}</button><button class="btn btn-outline" :disabled="busy" @click="open(selected)">{{ local('重试读取原文件', 'Retry source') }}</button></template>
        <p v-else class="hint-text">{{ local('选择项目查看源文件和管理操作', 'Select a project to view its source and actions') }}</p>
        <p v-if="task" class="hint-text">{{ local('任务', 'Task') }} {{ task.id }} · {{ task.status }} <button v-if="busy" class="btn btn-ghost btn-sm" @click="invoke(connectionId, 'docker/taskCancel', {taskId: task.id}).catch(e => error = String(e))">{{ local('请求取消', 'Request cancellation') }}</button></p>
      </article>
    </div>
  </section>
</template>

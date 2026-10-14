<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { invoke } from '../bridge';
import { getSettings, setSettings, local, type Settings, type Task } from '../automation';
import Dialog from './Dialog.vue';
import JsonTree from './JsonTree.vue';

const props = defineProps<{ connectionId: string; name: string; readOnly: boolean; production: boolean }>();
const open = ref(false), tab = ref('permissions'), busy = ref(false), error = ref('');
const settings = ref<Settings>();
const tasks = ref<Task[]>([]), audit = ref<unknown[]>([]);
const events = ref<unknown>();
const enabled = ref(false), allowed = ref<string[]>([]);
const groups = computed(() => [
  { id: 'query', label: local('查询', 'Queries') }, { id: 'manage', label: local('常规修改', 'Regular changes') },
  { id: 'create', label: local('创建与部署', 'Create & deploy') }, { id: 'delete', label: local('删除与清理', 'Delete & prune') },
]);
const status = computed(() => !settings.value?.policy.enabled ? local('关闭', 'Off') : settings.value.policy.allowedTools.some(name => settings.value?.tools.some(t => t.name === name && t.group !== 'query')) ? local('允许修改', 'Changes allowed') : local('仅查询', 'Queries only'));
function accept(value: Settings) { settings.value = value; enabled.value = value.policy.enabled; allowed.value = [...(value.policy.allowedTools || [])]; }
async function reload() {
  if (!props.connectionId) return;
  error.value = '';
  try { accept(await getSettings(props.connectionId)); } catch (e) { error.value = String(e); }
}
async function save() {
  if (!settings.value) return;
  busy.value = true; error.value = '';
  try { accept(await setSettings(props.connectionId, { enabled: enabled.value, allowedTools: allowed.value, revision: settings.value.policy.revision })); }
  catch (e) { error.value = String(e); } finally { busy.value = false; }
}
function toggleEnabled() {
  if (enabled.value && !settings.value?.policy.enabled) allowed.value = settings.value?.tools.filter(t => t.group === 'query').map(t => t.name) || [];
}
function toggleGroup(group: string, checked: boolean) {
  const names = settings.value?.tools.filter(t => t.group === group).map(t => t.name) || [];
  allowed.value = checked ? [...new Set([...allowed.value, ...names])] : allowed.value.filter(n => !names.includes(n));
}
function checked(group: string) { return settings.value?.tools.filter(t => t.group === group).every(t => allowed.value.includes(t.name)); }
async function loadTasks() {
  try { tasks.value = await invoke<Task[]>(props.connectionId, 'docker/taskList'); audit.value = await invoke<unknown[]>(props.connectionId, 'docker/auditList'); }
  catch (e) { error.value = String(e); }
}
async function cancel(id: string) { try { await invoke(props.connectionId, 'docker/taskCancel', { taskId: id }); await loadTasks(); } catch (e) { error.value = String(e); } }
async function loadEvents() { try { events.value = await invoke(props.connectionId, 'docker/events', { limit: 100 }); } catch (e) { error.value = String(e); } }
let timer: ReturnType<typeof setInterval> | undefined;
watch(() => props.connectionId, () => { settings.value = undefined; open.value = false; void reload(); }, { immediate: true });
watch(open, value => { clearInterval(timer); if (value) { void reload(); void loadTasks(); timer = setInterval(() => void loadTasks(), 2000); } });
onUnmounted(() => clearInterval(timer));
function showTasks() { tab.value = 'tasks'; open.value = true; void loadTasks(); }
onMounted(() => window.addEventListener('docker-task-started', showTasks));
onUnmounted(() => window.removeEventListener('docker-task-started', showTasks));
</script>

<template>
  <button class="btn btn-outline btn-sm automation-entry" :disabled="!connectionId" @click="open = true">AI/MCP {{ local('权限', 'permissions') }} · {{ status }}</button>
  <Dialog :open="open" content-class="dlg-wide automation-dialog" @update:open="open = $event">
    <div class="dlg-header"><div class="dlg-title">{{ name || connectionId }} · AI/MCP</div><div class="dlg-desc">{{ production ? local('生产环境', 'Production') : local('当前连接', 'Current connection') }} · {{ readOnly ? local('只读', 'Read-only') : status }}</div></div>
    <div class="filter-group"><button class="filter-btn" :class="{ active: tab === 'permissions' }" @click="tab = 'permissions'">{{ local('权限', 'Permissions') }}</button><button class="filter-btn" :class="{ active: tab === 'tasks' }" @click="tab = 'tasks'">{{ local('任务与调用记录', 'Tasks & calls') }}</button></div>
    <p v-if="error || settings?.warning" role="alert" class="error-banner">{{ error || settings?.warning }}</p>
    <div v-if="tab === 'permissions' && settings" class="automation-scroll">
      <p class="hint-text">{{ local('权限仅适用于当前连接，同时约束 DBX 内置 AI 和外部 MCP。DBX 自身的授权、白名单和只读限制仍然生效。', 'Permissions apply to this connection in both DBX AI and external MCP. DBX approval, allowlists and read-only restrictions still apply.') }}</p>
      <label class="check-label"><input v-model="enabled" type="checkbox" :disabled="busy || !!settings.warning" @change="toggleEnabled" />{{ local('启用 AI/MCP（首次开启仅查询）', 'Enable AI/MCP (queries only on first enable)') }}</label>
      <fieldset v-for="group in groups" :key="group.id" class="automation-group" :disabled="!enabled || busy || !!settings.warning || (readOnly && group.id !== 'query') || (!settings.policy.enabled && group.id !== 'query')">
        <legend><label class="check-label"><input type="checkbox" :checked="checked(group.id)" @change="toggleGroup(group.id, ($event.target as HTMLInputElement).checked)" />{{ group.label }}</label></legend>
        <details><summary>{{ local('展开查看具体工具', 'Show individual tools') }} ({{ settings.tools.filter(t => t.group === group.id).length }})</summary>
          <label v-for="tool in settings.tools.filter(t => t.group === group.id)" :key="tool.name" class="automation-tool"><input v-model="allowed" type="checkbox" :value="tool.name" /><span><strong>{{ tool.name }}</strong><small>{{ tool.description }}</small></span></label>
        </details>
      </fieldset>
      <p class="hint-text">{{ local('需要在 DBX 插件中心启用内置 AI 工具；外部 MCP 还需在 DBX 中授权连接。查询日志可能返回应用程序自行写入的敏感内容。', 'Enable built-in AI tools in DBX Plugin Center. External MCP also requires DBX connection authorization. Requested logs may contain sensitive application output.') }}</p>
    </div>
    <div v-else-if="tab === 'tasks'" class="automation-scroll">
      <p v-if="!tasks.length" class="hint-text">{{ local('暂无后台任务', 'No background tasks') }}</p>
      <article v-for="task in tasks" :key="task.id" class="automation-group">
        <div class="automation-task-title"><strong>{{ task.tool }}</strong><span>{{ task.status }}</span><button v-if="task.status === 'running'" class="btn btn-outline btn-sm" :disabled="task.cancelRequested" @click="cancel(task.id)">{{ task.cancelRequested ? local('正在取消', 'Cancelling') : local('取消任务', 'Cancel task') }}</button></div>
        <small>{{ task.id }} · {{ new Date(task.createdAt).toLocaleString() }}</small><p v-if="task.error" role="alert">{{ task.error }}</p>
        <details v-if="task.result"><summary>{{ local('结果', 'Result') }}</summary><JsonTree :value="task.result" :initial-expanded-depth="2" /></details>
      </article>
      <details><summary>{{ local('最近调用记录（不保存参数和凭据）', 'Recent calls (no arguments or credentials stored)') }}</summary><JsonTree :value="audit" :initial-expanded-depth="2" /></details>
      <details @toggle="($event.target as HTMLDetailsElement).open && loadEvents()"><summary>{{ local('Docker 事件（最近十分钟，最多 100 条）', 'Docker events (last ten minutes, up to 100)') }}</summary><JsonTree v-if="events" :value="events" :initial-expanded-depth="2" /></details>
    </div>
    <div class="dlg-footer"><button class="btn btn-outline" @click="open = false">{{ local('关闭', 'Close') }}</button><button v-if="tab === 'permissions'" class="btn btn-outline" :disabled="busy" @click="reload">{{ local('重新读取', 'Reload') }}</button><button v-if="tab === 'permissions'" class="btn btn-primary" :disabled="busy || !settings || !!settings.warning" @click="save">{{ local('保存权限', 'Save permissions') }}</button></div>
  </Dialog>
</template>

<style>
.automation-dialog{max-height:90vh;display:flex;flex-direction:column;gap:12px}.automation-scroll{overflow:auto;min-height:0;max-height:60vh}.automation-group{border:1px solid var(--border);border-radius:8px;padding:12px;margin:12px 0}.automation-group:disabled{opacity:.55}.automation-group summary{cursor:pointer;padding:6px 0}.automation-tool{display:flex;align-items:flex-start;gap:10px;margin:12px 0}.automation-tool span{display:grid;gap:4px}.automation-tool small{opacity:.75;line-height:1.5}.automation-task-title{display:flex;align-items:center;gap:12px}.automation-entry{white-space:nowrap}.automation-dialog small{overflow-wrap:anywhere}
</style>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { local } from '../automation';
import { fileName, type ComposeReview } from '../composeProjects';
import Icon from './Icon.vue';
const props = defineProps<{ review: ComposeReview }>();
const emit = defineEmits<{ validity: [valid: boolean] }>();
const confirmation = ref('');
const removing = computed(() => props.review.action === 'down');
const explanation = computed(() => ({
  deploy: local('将按编排文件创建或更新服务。已有容器可能被重新创建。', 'Services will be created or updated from the Compose files. Existing containers may be recreated.'),
  start: local('启动此项目中已创建的容器，不修改编排文件。', 'Start existing project containers without editing Compose files.'),
  stop: local('停止此项目的容器。服务会暂时不可用，数据卷保留。', 'Stop project containers. Services will be unavailable; data volumes are retained.'),
  restart: local('重启此项目的容器。服务会短暂中断。', 'Restart project containers. Services will be briefly interrupted.'),
  down: local('移除此项目的容器和 Compose 管理的网络，数据卷保留。', 'Remove project containers and Compose-managed networks. Data volumes are retained.'),
}[props.review.action]));
const rows = computed(() => props.review.action === 'deploy' ? (props.review.services || []).map(s => ({ name: s.name, image: s.image, label: s.exists ? local('检查更新', 'Check for updates') : local('创建', 'Create') })) : (props.review.containers || []).map(c => ({ name: c.service || c.name, image: c.image, label: c.state === 'running' ? local('运行中', 'Running') : local('已停止', 'Stopped') })));
watch(() => [props.review.project, props.review.action], () => { confirmation.value = ''; }, { immediate: true });
watch([confirmation, () => props.review], () => emit('validity', !removing.value || confirmation.value === props.review.project), { immediate: true });
</script>

<template>
  <div class="compose-review">
    <div class="compose-review-project"><span class="overview-symbol"><Icon name="layers" /></span><div><strong>{{ review.project }}</strong><span>{{ local('Docker Compose 项目', 'Docker Compose project') }}</span></div></div>
    <p class="compose-review-description">{{ explanation }}</p>
    <div v-if="review.sourceWillBeSaved" class="compose-review-notice"><Icon name="file-code" /><span>{{ local('当前编辑内容会先保存到原文件，再部署。', 'Current edits will be saved to the original file before deployment.') }}</span></div>
    <section class="compose-review-scope"><div class="compose-review-section-title"><strong>{{ review.action === 'deploy' ? local('编排服务', 'Compose services') : local('影响范围', 'Affected containers') }}</strong><span>{{ rows.length }} {{ local('项', 'items') }}</span></div><table v-if="rows.length" class="docker-table"><thead><tr><th>{{ local('服务', 'Service') }}</th><th>{{ local('镜像', 'Image') }}</th><th>{{ review.action === 'deploy' ? local('处理方式', 'Action') : local('当前状态', 'Current state') }}</th></tr></thead><tbody><tr v-for="(row, index) in rows" :key="row.name + index"><td>{{ row.name }}</td><td class="compose-path" :title="row.image">{{ row.image }}</td><td>{{ row.label }}</td></tr></tbody></table><p v-else class="hint-text">{{ local('当前没有已创建的容器。', 'No existing containers.') }}</p></section>
    <div class="compose-review-files" v-if="review.files?.length"><span>{{ local('配置文件', 'Configuration') }}</span><code v-for="path in review.files" :key="path" :title="path">{{ fileName(path) }}</code></div>
    <div class="compose-review-retention"><Icon name="shield-check" /><span>{{ local('数据卷保留；操作不会自动重试。', 'Data volumes are retained. Operations are not automatically retried.') }}</span></div>
    <label v-if="removing" class="docker-field compose-confirm-name"><span>{{ local('输入项目名称以确认移除', 'Type the project name to confirm removal') }} <strong>{{ review.project }}</strong></span><input v-model="confirmation" class="input" :placeholder="review.project" autocomplete="off" spellcheck="false" /></label>
  </div>
</template>

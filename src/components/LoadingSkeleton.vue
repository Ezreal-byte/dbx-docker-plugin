<script setup lang="ts">
import { local } from '../automation';
withDefaults(defineProps<{ rows?: number; columns?: number }>(), { rows: 6, columns: 3 });
</script>
<template>
  <div class="loading-skeleton" role="status" :aria-label="local('正在加载', 'Loading')" aria-live="polite">
    <div v-for="row in rows" :key="row" class="skeleton-row" :style="{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }" aria-hidden="true">
      <span v-for="col in columns" :key="col" class="skeleton-line" :style="{ width: `${45 + (row * 17 + col * 13) % 50}%` }" />
    </div>
  </div>
</template>

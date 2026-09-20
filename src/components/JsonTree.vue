// 简版 JsonTree：可展开/收起的树形 JSON 视图。
<template>
  <div class="json-tree">
    <JsonNode :value="value" :depth="0" :max-depth="initialExpandedDepth" path="$" :label="undefined" />
  </div>
</template>

<script setup lang="ts">
import JsonNode from './JsonNode.vue';

defineProps<{
  value: unknown;
  initialExpandedDepth?: number;
}>();
</script>

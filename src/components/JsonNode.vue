<template>
  <div class="json-node" :style="{ paddingLeft: depth > 0 ? '14px' : '0' }">
    <template v-if="isContainer">
      <button class="json-toggle" @click="expanded = !expanded">
        <span class="json-chevron" :class="{ open: expanded }">▸</span>
        <span v-if="label !== undefined" class="json-key">{{ label }}:</span>
        <span class="json-summary">{{ summary }}</span>
      </button>
      <template v-if="expanded">
        <JsonNode
          v-for="entry in entries"
          :key="entry.key"
          :value="entry.value"
          :label="entry.key"
          :depth="depth + 1"
          :max-depth="maxDepth"
          :path="`${path}.${entry.key}`"
        />
      </template>
    </template>
    <template v-else>
      <div class="json-leaf">
        <span v-if="label !== undefined" class="json-key">{{ label }}: </span>
        <span :class="scalarClass">{{ scalarText }}</span>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';

const props = defineProps<{
  value: unknown;
  label?: string;
  depth: number;
  maxDepth?: number;
  path: string;
}>();

const expanded = ref(props.maxDepth === undefined || props.depth < props.maxDepth);

const isContainer = computed(() => props.value !== null && typeof props.value === 'object');

const entries = computed(() => {
  if (!isContainer.value) return [];
  if (Array.isArray(props.value)) {
    return (props.value as unknown[]).map((value, i) => ({ key: String(i), value }));
  }
  return Object.entries(props.value as Record<string, unknown>).map(([key, value]) => ({ key, value }));
});

const summary = computed(() => {
  if (Array.isArray(props.value)) return `[${(props.value as unknown[]).length}]`;
  return `{${Object.keys(props.value as Record<string, unknown>).length}}`;
});

const scalarClass = computed(() => {
  const v = props.value;
  if (v === null) return 'json-null';
  if (typeof v === 'number') return 'json-number';
  if (typeof v === 'boolean') return 'json-boolean';
  return 'json-string';
});

const scalarText = computed(() => {
  const v = props.value;
  if (v === null) return 'null';
  if (typeof v === 'string') return JSON.stringify(v);
  return String(v);
});
</script>

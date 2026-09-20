// SVG sparkline 折线图，复刻 MetricLineChart 的可视效果（标题 + 折线 + y 轴百分比）。
<template>
  <div class="chart-card">
    <div class="chart-title">{{ title }}</div>
    <svg :viewBox="`0 0 ${width} ${height}`" class="chart-svg" preserveAspectRatio="none">
      <line v-for="i in 4" :key="i" :x1="pad" :x2="width - 4" :y1="gridY(i)" :y2="gridY(i)" class="chart-grid" />
      <text v-for="i in 4" :key="'t' + i" :x="2" :y="gridY(i) + 3" class="chart-label">{{ labelFor(i) }}</text>
      <polyline :points="points" fill="none" :stroke="color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
    </svg>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';

const props = defineProps<{
  title: string;
  data: number[];
  color: string;
  valueFormatter?: (value: number) => string;
}>();

const width = 300;
const height = 220;
const pad = 34;

const maxValue = computed(() => {
  const m = Math.max(...props.data, 1);
  return m * 1.1;
});

function gridY(i: number): number {
  return 8 + ((height - 28) * (4 - i)) / 4;
}

function labelFor(i: number): string {
  const value = (maxValue.value * (i - 1)) / 4;
  return props.valueFormatter ? props.valueFormatter(value) : value.toFixed(0);
}

const points = computed(() => {
  const n = props.data.length;
  if (!n) return '';
  const usableWidth = width - pad - 6;
  const usableHeight = height - 28;
  return props.data
    .map((v, i) => {
      const x = pad + (n === 1 ? usableWidth / 2 : (i / (n - 1)) * usableWidth);
      const y = 8 + usableHeight - (v / maxValue.value) * usableHeight;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
});
</script>

<script setup lang="ts">
// ECharts 折线图：替换原来的手写 SVG sparkline。
// 原实现用 preserveAspectRatio="none" 把 300x220 的 viewBox 拉伸铺满卡片，
// 于是 y 轴刻度文字（和网格线）一起被非等比缩放，表现为「文字被横向拉伸」。
// ECharts 走 canvas 渲染，不参与 CSS 缩放，字体与线条始终保持 1:1。
import { onMounted, onUnmounted, ref, watch } from 'vue';
import * as echarts from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer]);

export interface MetricSeries {
  name: string;
  color: string;
  data: number[];
}

const props = defineProps<{
  title: string;
  labels: string[];
  series: MetricSeries[];
  /** y 轴数值格式化，用于刻度与 tooltip。 */
  valueFormatter?: (value: number) => string;
  /** 固定 y 轴上限（如 CPU 百分比固定 100）。 */
  max?: number;
  /** 是否用小面积渐变填充（单序列时更易读）。 */
  area?: boolean;
  /** y 轴刻度取整方式，百分比场景传 1 避免出现 33.33 这类刻度。 */
  axisInterval?: number;
}>();

const host = ref<HTMLDivElement>();
let chart: echarts.ECharts | undefined;
let observer: ResizeObserver | undefined;
let themeObserver: MutationObserver | undefined;
let themeTimer: number | undefined;

function readToken(name: string, fallback: string): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return raw && !raw.includes('var(') ? raw : fallback;
}

function format(value: number): string {
  return props.valueFormatter ? props.valueFormatter(value) : String(value);
}

function buildOption(): echarts.EChartsCoreOption {
  const dark = document.documentElement.dataset.dbxTheme === 'dark';
  const textColor = readToken('--muted-foreground', dark ? '#a1a1aa' : '#71717a');
  const splitColor = readToken('--border', dark ? '#3f3f46' : '#e4e4e7');
  const totalPoints = props.labels.length;

  return {
    animation: false,
    grid: { left: 8, right: 12, top: props.series.length > 1 ? 26 : 10, bottom: 4, containLabel: true },
    legend:
      props.series.length > 1
        ? { top: 0, right: 0, itemWidth: 10, itemHeight: 6, textStyle: { color: textColor, fontSize: 10 } }
        : undefined,
    tooltip: {
      trigger: 'axis',
      confine: true,
      backgroundColor: readToken('--background', dark ? '#18181b' : '#ffffff'),
      borderColor: splitColor,
      textStyle: { color: readToken('--foreground', dark ? '#e4e4e7' : '#18181b'), fontSize: 11 },
      valueFormatter: (value: unknown) => format(Number(value)),
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: props.labels,
      axisLine: { lineStyle: { color: splitColor } },
      axisTick: { show: false },
      axisLabel: { color: textColor, fontSize: 10, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      max: props.max,
      min: 0,
      minInterval: props.axisInterval,
      splitNumber: 4,
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: splitColor, type: 'dashed' } },
      axisLabel: { color: textColor, fontSize: 10, formatter: (value: number) => format(value) },
    },
    series: props.series.map((item) => ({
      name: item.name,
      type: 'line',
      showSymbol: totalPoints <= 2,
      symbolSize: 4,
      smooth: false,
      lineStyle: { width: 1.8, color: item.color },
      itemStyle: { color: item.color },
      areaStyle:
        props.area || props.series.length === 1
          ? { opacity: 0.12, color: item.color }
          : undefined,
      data: item.data,
    })),
  };
}

function render() {
  if (!chart) return;
  chart.setOption(buildOption(), true);
}

onMounted(() => {
  if (!host.value) return;
  chart = echarts.init(host.value);
  render();
  observer = new ResizeObserver(() => chart?.resize());
  observer.observe(host.value);
  // 宿主切换明暗或配色时同步刷新图表配色。
  themeObserver = new MutationObserver(() => {
    if (themeTimer) window.clearTimeout(themeTimer);
    themeTimer = window.setTimeout(render, 60);
  });
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-dbx-theme', 'style', 'class'] });
});

onUnmounted(() => {
  if (themeTimer) window.clearTimeout(themeTimer);
  observer?.disconnect();
  themeObserver?.disconnect();
  chart?.dispose();
  chart = undefined;
});

watch(() => [props.labels, props.series], render, { deep: true });
</script>

<template>
  <div class="chart-card">
    <div class="chart-title">{{ title }}</div>
    <div ref="host" class="echart-host" />
  </div>
</template>

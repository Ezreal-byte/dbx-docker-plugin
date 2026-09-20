// 监控指标换算：把 Docker 的累计计数器（网络字节、块设备字节）折算成每秒速率。

export interface CounterSample {
  readAt: string;
  value: number;
}

/**
 * 把累计计数器序列折算成每秒速率（与 `docker stats` 的口径一致）。
 * - 首点没有前值，补 0。
 * - 时间差非正、或计数器回退（容器重启）时，该点记为 0，避免出现负速率或尖峰。
 */
export function computeRate(samples: CounterSample[]): number[] {
  const rates: number[] = [];
  for (let index = 0; index < samples.length; index += 1) {
    if (index === 0) {
      rates.push(0);
      continue;
    }
    const previous = samples[index - 1];
    const current = samples[index];
    const elapsedSeconds = (Date.parse(current.readAt) - Date.parse(previous.readAt)) / 1000;
    const delta = current.value - previous.value;
    if (!Number.isFinite(elapsedSeconds) || elapsedSeconds <= 0 || delta < 0) {
      rates.push(0);
      continue;
    }
    rates.push(delta / elapsedSeconds);
  }
  return rates;
}

/** 把字符串时间戳序列转成图表 x 轴标签。 */
export function timeLabels(readAt: string[]): string[] {
  return readAt.map((value) => {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? '' : parsed.toLocaleTimeString();
  });
}

/** 固定小数位并加上单位，用于 tooltip / 轴标签。 */
export function formatRate(bytesPerSecond: number): string {
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let value = Math.max(0, bytesPerSecond);
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value >= 10 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

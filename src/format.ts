export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, index);
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

export function formatDate(timestamp: number): string {
  if (!timestamp) return '';
  return new Date(timestamp * 1000).toLocaleString();
}

export function shortId(id: string): string {
  const trimmed = id.replace(/^sha256:/, '');
  return trimmed.slice(0, 12);
}

export async function copyToClipboard(text: string): Promise<void> {
  const plugin = window.dbxPlugin;
  if (plugin?.copy) {
    await plugin.copy(text);
    return;
  }
  await navigator.clipboard.writeText(text);
}

/**
 * 取保存路径所在目录；兼容 Windows 反斜杠与 POSIX 斜杠。
 * 输入已是目录（带尾部斜杠）时返回目录本身；只有文件名（Web 宿主）时原样返回。
 */
export function directoryOf(path: string): string {
  const trimmed = path.replace(/([\\/])+$/, '');
  if (trimmed && trimmed !== path) return trimmed;
  const index = Math.max(trimmed.lastIndexOf('\\'), trimmed.lastIndexOf('/'));
  return index > 0 ? trimmed.slice(0, index) : trimmed;
}

/** 桌面宿主回传绝对路径，Web 宿主只回传文件名。 */
export function savedPathKind(path: string, fileName: string): 'desktop' | 'web' {
  return path === fileName || !/[\\/]/.test(path) ? 'web' : 'desktop';
}

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

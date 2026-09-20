// 宿主桥接封装：window.dbxPlugin 由 dbx 沙箱注入。
export interface PluginContext {
  connectionId?: string;
  providerId?: string;
  connectionType?: string;
  workbenchId?: string;
  connection?: { id: string; name: string; host: string; port: number; username: string; readOnly: boolean };
  [key: string]: unknown;
}

export interface PluginTheme {
  appearance?: 'light' | 'dark';
  tokens?: Record<string, string>;
}

/** dbx-plugin-init / dbx-plugin-env 事件 detail 里与本插件相关的字段。 */
export interface PluginEnvPayload {
  locale?: string;
  theme?: PluginTheme;
}

interface DbxPlugin {
  ready: Promise<unknown>;
  context: PluginContext;
  locale?: string;
  theme?: PluginTheme;
  invoke<T>(method: string, params: Record<string, unknown>, options?: { timeoutMs: number }): Promise<T>;
  onContext(fn: (context: PluginContext) => void): () => void;
  onBinary(fn: (payload: { channel: string; data: Uint8Array }) => void): () => void;
  sendBinary?(channel: string, data: string | Uint8Array | ArrayBuffer): Promise<unknown>;
  /**
   * 宿主签名是 saveFile(options, data)：data 必须作为第二个参数传入，
   * 否则宿主会直接抛 "requires transferred binary data or dataBase64"。
   * 桌面宿主弹原生保存对话框并回传绝对路径；Web 宿主回传文件名；取消时回传 null。
   */
  saveFile?(
    options?: { fileName?: string; contentType?: string },
    data?: Uint8Array | ArrayBuffer | string,
  ): Promise<{ path?: string } | null>;
  copy?(text: string): Promise<unknown>;
}

declare global {
  interface Window {
    dbxPlugin?: DbxPlugin;
  }
}

export function getPlugin(): DbxPlugin {
  if (!window.dbxPlugin) throw new Error('此页面需要在 DBX 插件沙箱中运行 / This page must run inside the DBX plugin sandbox');
  return window.dbxPlugin;
}

export async function invoke<T>(connectionId: string, method: string, params: Record<string, unknown> = {}): Promise<T> {
  return getPlugin().invoke<T>(method, { ...params, connectionId }, { timeoutMs: 120000 });
}

export function onBinary(fn: (payload: { channel: string; data: Uint8Array }) => void): () => void {
  return getPlugin().onBinary(fn);
}

export function currentLocale(): string {
  return getPlugin().locale || 'en';
}

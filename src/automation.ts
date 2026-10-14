import { invoke } from './bridge';
import { locale } from './i18n';

export const local = (zh: string, en: string) => locale.value === 'zh-CN' ? zh : en;
export interface ToolInfo { name: string; description: string; group: 'query' | 'manage' | 'create' | 'delete' }
export interface Policy { enabled: boolean; allowedTools: string[]; revision: number }
export interface Settings { connectionId: string; policy: Policy; tools: ToolInfo[]; warning?: string }
export interface Task { id: string; tool: string; status: string; createdAt: string; cancelRequested: boolean; result?: unknown; error?: string }
export interface MutationPreview { previewToken: string; expiresAt: string; preview: unknown; isProduction: boolean }
export const getSettings = (id: string) => invoke<Settings>(id, 'mcp/settings/get');
export const setSettings = (id: string, policy: Policy) => invoke<Settings>(id, 'mcp/settings/set', { ...policy });
export const mutationPreview = (id: string, operation: string, args: Record<string, unknown>) => invoke<MutationPreview>(id, 'docker/mutation', { operation, arguments: args });
export const mutationExecute = <T>(id: string, operation: string, args: Record<string, unknown>, preview: MutationPreview) => invoke<T>(id, 'docker/mutation', { operation, arguments: { ...args, previewToken: preview.previewToken } });

// Only identifiers chosen by the user enter the host AI context.
export function recommendations(kind: string, id: string, zh: boolean) {
  const resource = { kind, id };
  const subject = id ? `${kind}/${id}` : kind;
  const prompts: Record<string, [string, string]> = {
    containers: ['分析容器健康状态和退出原因，按需读取有限日志。', 'Analyze container health and exit causes using bounded logs when needed.'],
    images: ['查看镜像平台、摘要和关联容器。', 'Inspect image platform, digests and referencing containers.'],
    networks: ['查看网络子网、端点和关联容器。', 'Inspect network subnets, endpoints and connected containers.'],
    volumes: ['查看这个卷被哪些容器使用，包括已停止的容器。', 'Find containers referencing this volume, including stopped containers.'],
  };
  const label = (prompts[kind] || prompts.containers)[zh ? 0 : 1];
  return { context: { resource }, items: [{ id: 'docker-inspect', label, prompt: `${label}\n${JSON.stringify({ resource: subject })}`, order: 10 }] };
}

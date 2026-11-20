export type ComposeAction = 'deploy' | 'start' | 'stop' | 'restart' | 'down';
export type ComposeProject = { name: string; directory: string; files: string[]; envFiles?: string[]; containers: number; running: number; conflict: boolean };
export type ComposeSource = { project: ComposeProject; files: { path: string; content: string }[]; editable: boolean; sourceHash: string };
export type ComposeReview = { project: string; action: ComposeAction; directory?: string; files?: string[]; sourceWillBeSaved: boolean; dataVolumesRetained: boolean; services?: { name: string; image: string; exists: boolean }[]; containers?: { name: string; service: string; state: string; image: string }[] };

export function fileName(path: string) { return path.split(/[\\/]/).filter(Boolean).pop() || path; }
export function projectStatus(project: ComposeProject): { key: 'running' | 'paused' | 'stopped'; zh: string; en: string } {
  if (!project.containers) return { key: 'stopped', zh: '未部署', en: 'Not deployed' };
  if (!project.running) return { key: 'stopped', zh: '已停止', en: 'Stopped' };
  if (project.running < project.containers) return { key: 'paused', zh: '部分运行', en: 'Partly running' };
  return { key: 'running', zh: '运行中', en: 'Running' };
}
export function actionLabel(action: ComposeAction, zh: boolean) {
  return ({ deploy: ['部署编排', 'Deploy project'], start: ['启动项目', 'Start project'], stop: ['停止项目', 'Stop project'], restart: ['重启项目', 'Restart project'], down: ['移除项目', 'Remove project'] } as const)[action][zh ? 0 : 1];
}
export function filterProjects(projects: ComposeProject[], query: string) {
  const search = query.trim().toLocaleLowerCase();
  return projects.filter(p => [p.name, p.directory, ...p.files].some(value => value.toLocaleLowerCase().includes(search)));
}

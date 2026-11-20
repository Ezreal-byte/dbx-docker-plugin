import { describe, it, expect } from 'vitest';
import { filterProjects, fileName, projectStatus, actionLabel, type ComposeProject } from './composeProjects';
const base: ComposeProject = { name: 'Demo-Shop', directory: '/srv/apps/shop', files: ['/srv/apps/shop/compose.yaml'], containers: 2, running: 2, conflict: false };
describe('Compose project presentation', () => {
  it('distinguishes undeployed, stopped, partial and running projects', () => {
    expect(projectStatus(base).zh).toBe('运行中');
    expect(projectStatus({ ...base, running: 1 }).zh).toBe('部分运行');
    expect(projectStatus({ ...base, running: 0 }).zh).toBe('已停止');
    expect(projectStatus({ ...base, containers: 0, running: 0 }).zh).toBe('未部署');
  });
  it('searches names and paths without case or whitespace surprises', () => {
    expect(filterProjects([base], ' DEMO ')).toEqual([base]);
    expect(filterProjects([base], 'COMPOSE.YAML')).toEqual([base]);
    expect(filterProjects([base], '/srv/apps')).toEqual([base]);
    expect(filterProjects([base], 'missing')).toEqual([]);
    expect(filterProjects([base], '')).toEqual([base]);
  });
  it('shows readable file names for local and SSH sources', () => {
    expect(fileName('E:\\projects\\shop\\compose.yaml')).toBe('compose.yaml');
    expect(fileName('/srv/shop/compose.override.yml')).toBe('compose.override.yml');
    expect(actionLabel('down', true)).toBe('移除项目');
    expect(actionLabel('deploy', false)).toBe('Deploy project');
  });
});

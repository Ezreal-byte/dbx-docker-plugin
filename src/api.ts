// API 层：把分支 api.docker* 调用映射为插件 sidecar 的 docker/* 方法。
import { invoke } from './bridge';
import type {
  DockerComposeApplyRequest,
  DockerComposeApplyResult,
  DockerConnectionInfo,
  DockerContainer,
  DockerContainerStats,
  DockerCreateContainerRequest,
  DockerCreateContainerResult,
  DockerCreateNetworkRequest,
  DockerCreateNetworkResult,
  DockerCreateVolumeRequest,
  DockerEngineDetails,
  DockerFileEntry,
  DockerFilePreview,
  DockerImage,
  DockerLogOptions,
  DockerNetwork,
  DockerRegistryAuth,
  DockerVolume,
} from './types';

export function listContainers(connectionId: string, all: boolean) {
  return invoke<DockerContainer[]>(connectionId, 'docker/listContainers', { all });
}

export function listImages(connectionId: string) {
  return invoke<DockerImage[]>(connectionId, 'docker/listImages');
}

export function listVolumes(connectionId: string) {
  return invoke<DockerVolume[]>(connectionId, 'docker/listVolumes');
}

export function listNetworks(connectionId: string) {
  return invoke<DockerNetwork[]>(connectionId, 'docker/listNetworks');
}

export function containerAction(connectionId: string, containerId: string, action: string) {
  return invoke<{ success: boolean }>(connectionId, 'docker/containerAction', { containerId, action });
}

export function inspectContainer(connectionId: string, containerId: string) {
  return invoke<Record<string, unknown>>(connectionId, 'docker/inspectContainer', { containerId });
}

export function containerStats(connectionId: string, containerIds: string[]) {
  return invoke<DockerContainerStats[]>(connectionId, 'docker/containerStats', { containerIds });
}

export function createContainer(connectionId: string, request: DockerCreateContainerRequest) {
  return invoke<DockerCreateContainerResult>(connectionId, 'docker/createContainer', { request });
}

export function applyCompose(connectionId: string, request: DockerComposeApplyRequest) {
  return invoke<DockerComposeApplyResult>(connectionId, 'docker/applyCompose', { request });
}

export function removeContainer(connectionId: string, containerId: string) {
  return invoke<{ success: boolean }>(connectionId, 'docker/removeContainer', { containerId });
}

export function pullImage(connectionId: string, image: string, auth: DockerRegistryAuth | undefined, sessionId: string) {
  return invoke<{ sessionId: string }>(connectionId, 'docker/pullImage', { image, auth, sessionId });
}

export function pushImage(connectionId: string, sourceImageId: string, targetReference: string, auth: DockerRegistryAuth | undefined, sessionId: string) {
  return invoke<{ sessionId: string }>(connectionId, 'docker/pushImage', { sourceImageId, targetReference, auth, sessionId });
}

export function removeImage(connectionId: string, imageId: string) {
  return invoke<{ success: boolean }>(connectionId, 'docker/removeImage', { imageId });
}

export function exportImage(connectionId: string, imageId: string, sessionId: string) {
  return invoke<{ sessionId: string }>(connectionId, 'docker/exportImage', { imageId, sessionId });
}

export function createVolume(connectionId: string, request: DockerCreateVolumeRequest) {
  return invoke<DockerVolume>(connectionId, 'docker/createVolume', { request });
}

export function createNetwork(connectionId: string, request: DockerCreateNetworkRequest) {
  return invoke<DockerCreateNetworkResult>(connectionId, 'docker/createNetwork', { request });
}

export function startLogs(connectionId: string, containerId: string, options: DockerLogOptions, sessionId: string) {
  return invoke<{ sessionId: string }>(connectionId, 'docker/startLogs', { containerId, options, sessionId });
}

export function stopStream(connectionId: string, sessionId: string) {
  return invoke<{ success: boolean }>(connectionId, 'docker/stopStream', { sessionId });
}

export function startExec(connectionId: string, containerId: string, sessionId: string, command: string[], cols: number, rows: number) {
  return invoke<{ sessionId: string }>(connectionId, 'docker/startExec', { containerId, sessionId, command, cols, rows });
}

export function execResize(connectionId: string, sessionId: string, cols: number, rows: number) {
  return invoke<{ success: boolean }>(connectionId, 'docker/execResize', { sessionId, cols, rows });
}

export function stopExec(connectionId: string, sessionId: string) {
  return invoke<{ success: boolean }>(connectionId, 'docker/stopExec', { sessionId });
}

export function listContainerFiles(connectionId: string, containerId: string, path: string) {
  return invoke<DockerFileEntry[]>(connectionId, 'docker/listContainerFiles', { containerId, path });
}

export function previewContainerFile(connectionId: string, containerId: string, path: string) {
  return invoke<DockerFilePreview>(connectionId, 'docker/previewContainerFile', { containerId, path });
}

export function getEngineDetails(connectionId: string) {
  return invoke<DockerEngineDetails>(connectionId, 'docker/getEngineDetails');
}

export interface ConnectionInfoResult {
  success: boolean;
  info: DockerConnectionInfo;
  readOnly: boolean;
  isProduction: boolean;
  color: string;
  name: string;
}

export function getConnectionInfo(connectionId: string) {
  return invoke<ConnectionInfoResult>(connectionId, 'docker/getConnectionInfo');
}

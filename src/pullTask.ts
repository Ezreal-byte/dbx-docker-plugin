// 逐字移植分支 apps/desktop/src/components/docker/dockerPullTask.ts。
export interface PendingDockerPullTask {
  sessionId: string;
  progress: {
    sessionId: string;
    kind: "pull";
    direction: "download";
    image: string;
    status: "running";
    bytesCompleted: number;
    layersCompleted: number;
    layersTotal: number;
  };
}

export function newSessionId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `docker-pull-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function createPendingDockerPullTask(image: string): PendingDockerPullTask {
  const sessionId = newSessionId();
  return {
    sessionId,
    progress: {
      sessionId,
      kind: "pull",
      direction: "download",
      image,
      status: "running",
      bytesCompleted: 0,
      layersCompleted: 0,
      layersTotal: 0,
    },
  };
}

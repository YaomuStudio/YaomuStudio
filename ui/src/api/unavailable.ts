// 无桥/桥损坏时的安全不可用 API。
//
// 不维护缓存或示例数据，不把未知错误伪装成成功：所有异步方法统一拒绝
// CORE_NOT_READY；subscribeEvents 保持同步返回取消函数的既有形状，
// 在微任务里通知安全错误，取消后不再回调。
import { coreNotReadyError, type YaoMuApi, type EventStreamError } from "./types";

const NOT_READY_STREAM_ERROR: EventStreamError = {
  code: "CORE_NOT_READY",
  message: "CORE_NOT_READY",
  retryable: true,
};

function rejectNotReady(): Promise<never> {
  return Promise.reject(coreNotReadyError());
}

/** 显式列出全部方法，`satisfies` 保证接口新增方法时必须同步更新。 */
const unavailableMethods = {
  // 系统 / 能力 / 槽位配置
  getSystem: rejectNotReady,
  getCapabilities: rejectNotReady,
  getDevices: rejectNotReady,
  getEnvironment: rejectNotReady,
  getStorageUsage: rejectNotReady,
  cleanupStorage: rejectNotReady,
  getLocalApiStats: rejectNotReady,
  getLocalApiSettings: rejectNotReady,
  updateLocalApiSettings: rejectNotReady,
  issueLocalApiToken: rejectNotReady,
  revokeLocalApiToken: rejectNotReady,
  getEngineConfig: rejectNotReady,
  setEngineConfig: rejectNotReady,

  // 项目
  listProjects: rejectNotReady,
  getProject: rejectNotReady,
  createProject: rejectNotReady,
  updateProject: rejectNotReady,
  deleteProject: rejectNotReady,
  importAssets: rejectNotReady,
  relocateAsset: rejectNotReady,

  // 任务 / 工作流
  listTasks: rejectNotReady,
  getTask: rejectNotReady,
  cancelTask: rejectNotReady,
  retryTask: rejectNotReady,
  getRun: rejectNotReady,
  pauseRun: rejectNotReady,
  resumeRun: rejectNotReady,
  preflight: rejectNotReady,
  createWorkflowRun: rejectNotReady,
  getSnapshot: rejectNotReady,
  getExecutionLog: rejectNotReady,

  // 字幕 / 候选 / 审核
  saveSubtitle: rejectNotReady,
  generateCandidates: rejectNotReady,
  listCandidates: rejectNotReady,
  selectCandidate: rejectNotReady,
  getSelection: rejectNotReady,
  listReviews: rejectNotReady,
  getReview: rejectNotReady,
  approveReview: rejectNotReady,

  // 导出 / 安装
  export: rejectNotReady,
  listExports: rejectNotReady,
  getInstallations: rejectNotReady,
  planInstallation: rejectNotReady,
  install: rejectNotReady,
  cancelInstallation: rejectNotReady,
  rollbackInstallation: rejectNotReady,
  deleteInstallation: rejectNotReady,

  // 文件 / 字幕轨
  saveAsset: rejectNotReady,
  importSubtitleTrack: rejectNotReady,

  // 专属链路模板
  listCustomPipelineTemplates: rejectNotReady,
  saveCustomPipelineTemplates: rejectNotReady,
} satisfies Omit<YaoMuApi, "subscribeEvents">;

export function createUnavailableApi(): YaoMuApi {
  return {
    ...unavailableMethods,
    subscribeEvents(_cb, onError) {
      let active = true;
      queueMicrotask(() => {
        if (!active) return;
        onError?.(NOT_READY_STREAM_ERROR);
      });
      return () => {
        active = false;
      };
    },
  };
}

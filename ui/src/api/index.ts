// API 门面：页面统一 `import { api } from "@/api"`。
//
// 数据源模式（生产/测试边界）：
// - bridge：存在且完整的 Electron preload 桥，始终优先；桥调用失败按真实错误展示，不降级夹具。
// - fixtures：仅 Playwright 测试构建（Vite dev serve + VITE_E2E_FIXTURES=1）加载测试夹具；
//   产品没有演示模式入口或横幅，所有 build 一律禁用夹具模块。
// - unavailable：无桥且未启用测试夹具（或桥对象损坏），统一返回安全 CORE_NOT_READY，不展示示例数据。
import { createBridgeApi } from "./bridge";
import { fixtureApi } from "yaomu-e2e-source";
import { createUnavailableApi } from "./unavailable";
import type { YaoMuApi } from "./types";

export type ApiMode = "bridge" | "fixtures" | "unavailable";

/** 完整方法名清单：TypeScript 校验必须覆盖 YaoMuApi 的全部成员，避免损坏桥被当作可用。 */
const BRIDGE_METHODS = {
  getSystem: true,
  getCapabilities: true,
  getDevices: true,
  getEnvironment: true,
  getStorageUsage: true,
  cleanupStorage: true,
  getLocalApiStats: true,
  getLocalApiSettings: true,
  updateLocalApiSettings: true,
  issueLocalApiToken: true,
  revokeLocalApiToken: true,
  getEngineConfig: true,
  setEngineConfig: true,
  listProjects: true,
  getProject: true,
  createProject: true,
  updateProject: true,
  deleteProject: true,
  importAssets: true,
  relocateAsset: true,
  listTasks: true,
  getTask: true,
  cancelTask: true,
  retryTask: true,
  getRun: true,
  pauseRun: true,
  resumeRun: true,
  preflight: true,
  createWorkflowRun: true,
  getSnapshot: true,
  getExecutionLog: true,
  saveSubtitle: true,
  generateCandidates: true,
  listCandidates: true,
  selectCandidate: true,
  getSelection: true,
  listReviews: true,
  getReview: true,
  approveReview: true,
  export: true,
  listExports: true,
  getInstallations: true,
  planInstallation: true,
  install: true,
  cancelInstallation: true,
  rollbackInstallation: true,
  deleteInstallation: true,
  saveAsset: true,
  importSubtitleTrack: true,
  listCustomPipelineTemplates: true,
  saveCustomPipelineTemplates: true,
  subscribeEvents: true,
} satisfies Record<keyof YaoMuApi, true>;

const BRIDGE_METHOD_NAMES = Object.keys(BRIDGE_METHODS) as (keyof YaoMuApi)[];

const bridge = typeof window !== "undefined" ? window.yaomu?.api : undefined;

/** preload 桥必须完整：部分/损坏对象不能因为 truthy 就展示假成功。 */
function isBridgeUsable(candidate: unknown): candidate is YaomuBridgeApi {
  if (typeof candidate !== "object" || candidate === null) return false;
  try {
    const record = candidate as Record<string, unknown>;
    return BRIDGE_METHOD_NAMES.every((name) => typeof record[name] === "function");
  } catch {
    // 属性访问抛错的损坏桥按不可用处理，不在模块导入时把页面带崩。
    return false;
  }
}

const bridgeUsable = isBridgeUsable(bridge);

export const apiMode: ApiMode = bridgeUsable
  ? "bridge"
  : bridge === undefined && fixtureApi !== null
    ? "fixtures"
    : "unavailable";

export const api: YaoMuApi = bridgeUsable
  ? createBridgeApi(bridge)
  : apiMode === "fixtures" && fixtureApi !== null
    ? fixtureApi
    : createUnavailableApi();

export { ApiError, coreNotReadyError } from "./types";
export type { YaoMuApi } from "./types";

import { contextBridge, ipcRenderer, webUtils } from "electron";
import type { IpcRendererEvent } from "electron";

// 白名单桥接：按能力逐个暴露，不一次放任意 IPC 通道（最小权限）。
// 每个 API 方法返回可序列化信封 {ok,data} / {ok,error}；事件订阅返回取消函数，避免 Renderer 泄漏监听。

interface BridgeError {
  code: string;
  messageKey: string;
  retryable: boolean;
  actionIds: string[];
  requestId?: string;
  status?: number;
}

type BridgeResult<T> = { ok: true; data: T } | { ok: false; error: BridgeError };

interface BridgeEventStreamError {
  code: string;
  message: string;
  retryable: boolean;
}

function invoke<T>(method: string, ...args: unknown[]): Promise<BridgeResult<T>> {
  return ipcRenderer.invoke("yaomu:api:invoke", method, args) as Promise<BridgeResult<T>>;
}

function subscribe<T>(channel: string, callback: (payload: T) => void): () => void {
  const listener = (_event: IpcRendererEvent, payload: T) => callback(payload);
  ipcRenderer.on(channel, listener);
  return () => ipcRenderer.removeListener(channel, listener);
}

// 连接通知（contracts/desktop-events.md）：只接受精确的 {schemaVersion:1}。
// 它是传输信号而非 DomainEvent，不得被当成业务事件或游标推进依据。
function isEventStreamConnected(payload: unknown): boolean {
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return false;
  const record = payload as Record<string, unknown>;
  return Object.keys(record).length === 1 && record.schemaVersion === 1;
}

// 拖入窗口的 File 在 sandbox Renderer 里没有 .path；只能由 preload 的 webUtils 解析。
// 只对用户实际选择/拖入的 File 生效，合成 File 返回空串；模块不可用时安全降级。
function filePath(file: unknown): string {
  try {
    const getPath = webUtils?.getPathForFile as ((file: unknown) => string) | undefined;
    const resolved = getPath ? (getPath(file) ?? "") : "";
    if (!resolved) return "";
    const authorized: unknown = ipcRenderer.sendSync("yaomu:files:register-drop", resolved);
    return typeof authorized === "string" ? authorized : "";
  } catch {
    return "";
  }
}

// 播放地址：只暴露 yaomu-asset 协议，不暴露 Core URL/实例令牌。
// sandbox preload 不能 require 本地模块，规则与 desktop/src/ipc/asset-stream.ts 保持一致。
const ASSET_SCHEME = "yaomu-asset";
const MAX_ASSET_ID = 200;
function assetUrl(assetId: unknown): string {
  if (
    typeof assetId !== "string" ||
    assetId.length === 0 ||
    assetId.length > MAX_ASSET_ID ||
    assetId === "." ||
    assetId === ".." ||
    /[/\\\u0000]/.test(assetId)
  ) {
    return "";
  }
  return `${ASSET_SCHEME}://asset/${encodeURIComponent(assetId)}`;
}

const update = {
  check: () => ipcRenderer.invoke("update:check"),
  download: () => ipcRenderer.invoke("update:download"),
  // 首个订阅者挂载后请求当前有限更新状态，避免错过启动检查事件。
  ready: () => ipcRenderer.invoke("update:ready"),
  // 显式同意/加入当前安装尝试；返回有限状态信封，绝不直接触发未受控安装。
  install: () => ipcRenderer.invoke("update:install"),
  getInstallState: () => ipcRenderer.invoke("update:get-install-state"),
  cancelInstall: () => ipcRenderer.invoke("update:cancel-install"),
  onInstallState: (callback: (payload: unknown) => void): (() => void) => {
    // 先登记状态监听，再发送 attach，避免订阅与首帧之间丢通知。
    const unsubscribe = subscribe("update:install-state", callback);
    ipcRenderer.send("update:install-state:attach");
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      unsubscribe();
      ipcRenderer.send("update:install-state:detach");
    };
  },
  onStatus: (cb: (payload: string) => void) => subscribe("update:status", cb),
  onAvailable: (cb: (info: unknown) => void) => subscribe("update:available", cb),
  onNotAvailable: (cb: (info: unknown) => void) => subscribe("update:not-available", cb),
  onProgress: (cb: (p: unknown) => void) => subscribe("update:progress", cb),
  onDownloaded: (cb: (info: unknown) => void) => subscribe("update:downloaded", cb),
  onError: (cb: (message: string) => void) => subscribe("update:error", cb),
};

// 每个能力单独、显式暴露；通道名不整段透传。
const api = {
  // 系统 / 能力
  getSystem: () => invoke<unknown>("getSystem"),
  getCapabilities: () => invoke<unknown>("getCapabilities"),
  getDevices: () => invoke<unknown>("getDevices"),
  getEnvironment: (refresh?: boolean) => invoke<unknown>("getEnvironment", refresh),
  getStorageUsage: () => invoke<unknown>("getStorageUsage"),
  cleanupStorage: (input: unknown) => invoke<unknown>("cleanupStorage", input),
  getLocalApiStats: () => invoke<unknown>("getLocalApiStats"),
  getLocalApiSettings: () => invoke<unknown>("getLocalApiSettings"),
  updateLocalApiSettings: (input: unknown) => invoke<unknown>("updateLocalApiSettings", input),
  issueLocalApiToken: () => invoke<unknown>("issueLocalApiToken"),
  revokeLocalApiToken: () => invoke<unknown>("revokeLocalApiToken"),
  getEngineConfig: (slot: string) => invoke<unknown>("getEngineConfig", slot),
  setEngineConfig: (slot: string, input: unknown) => invoke<unknown>("setEngineConfig", slot, input),

  // 项目
  listProjects: () => invoke<unknown>("listProjects"),
  getProject: (id: string) => invoke<unknown>("getProject", id),
  createProject: (input: unknown) => invoke<unknown>("createProject", input),
  updateProject: (id: string, input: unknown) => invoke<unknown>("updateProject", id, input),
  deleteProject: (id: string) => invoke<unknown>("deleteProject", id),
  importAssets: (input: unknown) => invoke<unknown>("importAssets", input),
  relocateAsset: (assetId: string, input: unknown) =>
    invoke<unknown>("relocateAsset", assetId, input),

  // 任务 / 工作流
  listTasks: (query?: unknown) => invoke<unknown>("listTasks", query),
  getTask: (id: string) => invoke<unknown>("getTask", id),
  getExecutionLog: (id: string, options?: unknown) =>
    invoke<unknown>("getExecutionLog", id, options),
  cancelTask: (id: string) => invoke<unknown>("cancelTask", id),
  retryTask: (id: string) => invoke<unknown>("retryTask", id),
  getRun: (runId: string) => invoke<unknown>("getRun", runId),
  pauseRun: (id: string) => invoke<unknown>("pauseRun", id),
  resumeRun: (id: string) => invoke<unknown>("resumeRun", id),
  preflight: (input: unknown) => invoke<unknown>("preflight", input),
  createWorkflowRun: (input: unknown) => invoke<unknown>("createWorkflowRun", input),
  getSnapshot: (projectId: string) => invoke<unknown>("getSnapshot", projectId),

  // 字幕 / 候选 / 审核
  saveSubtitle: (trackId: string, input: unknown) => invoke<unknown>("saveSubtitle", trackId, input),
  importSubtitleTrack: (input: unknown) => invoke<unknown>("importSubtitleTrack", input),
  listCustomPipelineTemplates: () => invoke<unknown>("listCustomPipelineTemplates"),
  saveCustomPipelineTemplates: (input: unknown) =>
    invoke<unknown>("saveCustomPipelineTemplates", input),
  generateCandidates: (segmentId: string, input: unknown) =>
    invoke<unknown>("generateCandidates", segmentId, input),
  listCandidates: (segmentId: string, language: string) =>
    invoke<unknown>("listCandidates", segmentId, language),
  selectCandidate: (segmentId: string, input: unknown) =>
    invoke<unknown>("selectCandidate", segmentId, input),
  getSelection: (segmentId: string, language: string) =>
    invoke<unknown>("getSelection", segmentId, language),
  listReviews: (projectId: string, runId?: string) =>
    invoke<unknown>("listReviews", projectId, runId),
  getReview: (reviewId: string) => invoke<unknown>("getReview", reviewId),
  approveReview: (reviewId: string, input: unknown) =>
    invoke<unknown>("approveReview", reviewId, input),

  // 导出 / 安装
  export: (input: unknown) => invoke<unknown>("export", input),
  listExports: (projectId: string) => invoke<unknown>("listExports", projectId),
  getInstallations: () => invoke<unknown>("getInstallations"),
  planInstallation: (slot: string) => invoke<unknown>("planInstallation", slot),
  install: (input: unknown) => invoke<unknown>("install", input),
  cancelInstallation: (id: string) => invoke<unknown>("cancelInstallation", id),
  rollbackInstallation: (slot: string) => invoke<unknown>("rollbackInstallation", slot),
  deleteInstallation: (slot: string) => invoke<unknown>("deleteInstallation", slot),

  // 事件：attach/detach 由 Main 按引用计数管理 SSE，取消订阅后不再回调。
  // onError 只在事件流不可恢复（游标过期/鉴权失败）时触发，用于提示重新同步。
  // options.afterSeq 是快照高水位 H：首次订阅从 H 续订，避免回放与实时监听之间丢事件。
  // options.onConnected 是可选的传输连接通知：Main 每次 HTTP SSE 成功后触发一次，
  // 客户端据此合并一次只读刷新；回调只留在 preload，不跨 IPC 序列化。
  subscribeEvents: (
    cb: (event: unknown) => void,
    onError?: (error: BridgeEventStreamError) => void,
    options?: { afterSeq?: number; onConnected?: () => void },
  ): (() => void) => {
    // 先登记全部监听，再发送 attach，避免订阅与首个通知之间丢帧。
    const unsubscribe = subscribe("yaomu:api:event", cb);
    const unsubscribeError = onError ? subscribe("yaomu:api:event-error", onError) : () => undefined;
    const unsubscribeConnected = options?.onConnected
      ? subscribe("yaomu:api:events-connected", (payload: unknown) => {
          if (!isEventStreamConnected(payload)) return;
          try {
            options.onConnected?.();
          } catch {
            // Renderer 回调异常不得影响流生命周期，也不能诱发 Main 重新连接。
          }
        })
      : () => undefined;
    const afterSeq =
      typeof options?.afterSeq === "number" && Number.isFinite(options.afterSeq)
        ? Math.max(0, options.afterSeq)
        : undefined;
    ipcRenderer.send("yaomu:events:attach", afterSeq);
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      unsubscribe();
      unsubscribeError();
      unsubscribeConnected();
      ipcRenderer.send("yaomu:events:detach");
    };
  },

  // 文件与系统能力（用户经系统对话框授权，不接受任意路径读写）
  selectFiles: (options?: unknown) => ipcRenderer.invoke("yaomu:files:select-files", options),
  selectDirectory: (options?: unknown) =>
    ipcRenderer.invoke("yaomu:files:select-directory", options),
  // 任务产出固定目录：主进程创建并授予本窗口写权限；后续可配置时仍由主进程返回。
  getDefaultOutputDir: () =>
    ipcRenderer.invoke("yaomu:files:default-output-dir") as Promise<string>,
  // 系统目录选择器授权后由主进程递归扫描受支持媒体文件；取消时 directory 为空。
  selectDirectoryFiles: (options?: unknown) =>
    ipcRenderer.invoke("yaomu:files:select-directory-files", options),
  getPathForFile: (file: unknown) => filePath(file),
  // 媒体播放入口：非法资产 ID 返回空串；不暴露令牌或 Core 地址。
  assetUrl: (assetId: unknown) => assetUrl(assetId),
  openPath: (target: string) => ipcRenderer.invoke("yaomu:files:open-path", target),
  saveTextFile: (input: unknown) => ipcRenderer.invoke("yaomu:files:save-text", input),
  saveAsset: (input: unknown) => ipcRenderer.invoke("yaomu:assets:save", input),
  removeWslRuntime: () => ipcRenderer.invoke("yaomu:runtime:clean-wsl"),
  notify: (title: string, body: string, category?: string) =>
    ipcRenderer.invoke("yaomu:notify", { title, body, category }),
  // 关闭行为同步给主进程（唯一权威）；主进程负责持久化与托盘生命周期。
  setCloseBehavior: (value: "quit" | "minimize") =>
    ipcRenderer.invoke("yaomu:settings:set-close-behavior", value),
  // 自动检查更新开关同步给主进程（唯一权威）；只影响自动检查，手动检查不受限。
  setAutoCheckUpdates: (value: boolean) =>
    ipcRenderer.invoke("yaomu:settings:set-auto-check-updates", value),
  // 只读水合：localStorage 没有持久值时读取主进程权威值，不把界面默认值写回。
  getAutoCheckUpdates: () => ipcRenderer.invoke("yaomu:settings:get-auto-check-updates") as Promise<boolean>,
  // 自动下载开关同步给主进程；安装始终需要显式确认。
  setAutoDownloadUpdates: (value: boolean) =>
    ipcRenderer.invoke("yaomu:settings:set-auto-download-updates", value),
  getAutoDownloadUpdates: () =>
    ipcRenderer.invoke("yaomu:settings:get-auto-download-updates") as Promise<boolean>,
  // 引导完成状态以主进程文件为权威；null 表示未设置（Renderer 按未完成处理）。
  getOnboardingCompleted: () =>
    ipcRenderer.invoke("yaomu:settings:get-onboarding-completed") as Promise<boolean | null>,
  // 完成引导后写入主进程权威状态；失败由 Renderer 记录，不回滚界面。
  setOnboardingCompleted: () =>
    ipcRenderer.invoke("yaomu:settings:set-onboarding-completed") as Promise<void>,
  // 界面缩放：Main 使用 Chromium 页面缩放应用到当前窗口，Renderer 不直接改页面 zoom。
  setUiScale: (factor: number) => ipcRenderer.invoke("yaomu:settings:set-ui-scale", factor),
};

// 宿主初始化状态（契约 contracts/desktop-runtime.md）：只暴露有限快照/订阅/重试。
// sandbox preload 不 require 本地模块；通道名与信封形状内联在这里。
const runtime = {
  getSnapshot: () => ipcRenderer.invoke("yaomu:runtime:get"),
  retry: () => ipcRenderer.invoke("yaomu:runtime:retry"),
  onChanged: (callback: (snapshot: unknown) => void): (() => void) => {
    // 先登记 changed 监听，再发送 attach，避免订阅与首帧之间丢通知。
    const unsubscribe = subscribe("yaomu:runtime:changed", callback);
    ipcRenderer.send("yaomu:runtime:attach");
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      unsubscribe();
      ipcRenderer.send("yaomu:runtime:detach");
    };
  },
};

contextBridge.exposeInMainWorld("yaomu", {
  platform: process.platform,
  update,
  api,
  runtime,
  // Linux 自绘标题栏控件（TopBar）；mac/win 用系统按钮不调用。
  window: {
    minimize(): Promise<void> {
      return ipcRenderer.invoke("window:minimize") as Promise<void>;
    },
    toggleMaximize(): Promise<boolean> {
      return ipcRenderer.invoke("window:maximize-toggle") as Promise<boolean>;
    },
    close(): Promise<void> {
      return ipcRenderer.invoke("window:close") as Promise<void>;
    },
    isMaximized(): Promise<boolean> {
      return ipcRenderer.invoke("window:is-maximized") as Promise<boolean>;
    },
  },
});

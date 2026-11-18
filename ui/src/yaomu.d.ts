// window.yaomu 桌面桥类型声明（与 desktop/src/preload.ts 对齐）。
// 本文件无 import/export，作为全局声明脚本生效。

interface YaomuUpdateInfo {
  version: string;
  releaseName?: string | null;
  releaseNotes?: string | null;
  releaseDate?: string;
}

interface YaomuUpdateProgress {
  percent: number;
  transferred: number;
  total: number;
  bytesPerSecond: number;
}

/** 主进程返回的可序列化错误（HTTP 错误体已映射为驼峰字段）。 */
interface YaomuBridgeError {
  code: string;
  messageKey: string;
  retryable: boolean;
  actionIds: string[];
  requestId?: string;
  status?: number;
}

/** 宿主更新安装状态（唯一语义见 contracts/app-update-installation.md 与 schema v1）。 */
type YaomuUpdateInstallPhase =
  | "idle"
  | "waiting"
  | "preparing"
  | "stopping"
  | "installing"
  | "blocked";

type YaomuUpdateInstallReason =
  | "active_work"
  | "request_in_flight"
  | "executor_starting"
  | "runtime_unavailable"
  | "runtime_changed"
  | "executor_unconfirmed"
  | "prepare_failed"
  | "stop_unconfirmed"
  | "restart_required"
  | "update_unavailable"
  | "install_failed";

interface YaomuUpdateInstallSnapshot {
  schemaVersion: 1;
  /** 宿主生命周期内单调递增；旧 revision 不得覆盖新状态。 */
  revision: number;
  phase: YaomuUpdateInstallPhase;
  reason: YaomuUpdateInstallReason | null;
  version: string | null;
  canCancel: boolean;
}

/** 固定安全错误：只按 code 映射已注册文案，不展示任意异常文本。 */
interface YaomuUpdateInstallError {
  code: "UPDATE_INSTALL_UNAVAILABLE" | "UPDATE_INSTALL_ACCESS_DENIED";
  message: string;
}

type YaomuUpdateInstallResult =
  | { ok: true; data: YaomuUpdateInstallSnapshot }
  | { ok: false; error: YaomuUpdateInstallError };

type YaomuBridgeResult<T> = { ok: true; data: T } | { ok: false; error: YaomuBridgeError };

/** 事件流不可恢复错误（游标过期、鉴权失败），UI 据此提示重新同步。 */
interface YaomuEventStreamError {
  code: string;
  message: string;
  retryable: boolean;
}

/** 事件订阅选项：快照高水位握手时从 afterSeq 续订。 */
interface YaomuEventSubscribeOptions {
  afterSeq?: number;
  /** 传输层成功连接通知（初连/自动重连）；只触发只读刷新，不是业务事件。 */
  onConnected?: () => void;
}

/** 文件夹导入：主进程递归扫描后的受支持媒体文件（绝对路径）。 */
interface YaomuDirectoryScanResult {
  /** 用户选择的目录；取消选择时为空串。 */
  directory: string;
  paths: string[];
  /** 目录内匹配文件总数（不受 limit 截断）。 */
  total: number;
  truncated: boolean;
  /** 不可读子目录数量，已跳过并继续扫描。 */
  errors: number;
}

/**
 * 桌面运行时就绪快照（唯一语义见 contracts/desktop-runtime.md 与 schema v1）。
 * Main 拥有 revision/generation；preload 只转发，Renderer 只读。
 */
type YaomuRuntimePhase =
  | "preparing"
  | "starting"
  | "ready"
  | "restarting"
  | "failed"
  | "stopping"
  | "stopped";

type YaomuRuntimeReason =
  | "startup_failed"
  | "core_exited"
  | "cleanup_unconfirmed"
  | "handshake_failed"
  | "protocol_mismatch"
  | "workspace_mismatch";

interface YaomuRuntimeSnapshot {
  schemaVersion: 1;
  /** Main 生命周期内单调递增；同版本幂等，旧版本不得覆盖。 */
  revision: number;
  /** 已确认 Core 连接代次；ready 时至少为 1。不是 Core.server_epoch。 */
  generation: number;
  phase: YaomuRuntimePhase;
  reason: YaomuRuntimeReason | null;
  canRetry: boolean;
}

/** 受控拒绝信封：只含安全错误码与已注册文案键，不含原始异常。 */
interface YaomuRuntimeError {
  code: "LOCAL_ACCESS_DENIED" | "CORE_NOT_READY";
  messageKey: "api.accessDenied" | "api.coreNotReady";
  retryable: boolean;
  actionIds: [];
}

type YaomuRuntimeResult =
  | { ok: true; data: YaomuRuntimeSnapshot }
  | { ok: false; error: YaomuRuntimeError };

/** 宿主初始化桥：与业务 Core HTTP/SSE 分离，不承载业务队列或持久游标。 */
interface YaomuRuntimeBridge {
  getSnapshot(): Promise<YaomuRuntimeResult>;
  onChanged(callback: (snapshot: YaomuRuntimeSnapshot) => void): () => void;
  retry(): Promise<YaomuRuntimeResult>;
}

/** 与 ui/src/api/types.ts 的 YaoMuApi 一一对应；请求/响应使用契约的 snake_case 线上形状。 */
interface YaomuBridgeApi {
  // 系统 / 能力
  getSystem(): Promise<YaomuBridgeResult<unknown>>;
  getCapabilities(): Promise<YaomuBridgeResult<unknown>>;
  getDevices(): Promise<YaomuBridgeResult<unknown>>;
  getEnvironment(refresh?: boolean): Promise<YaomuBridgeResult<unknown>>;
  getStorageUsage(): Promise<YaomuBridgeResult<unknown>>;
  getLocalApiStats(): Promise<YaomuBridgeResult<unknown>>;
  getLocalApiSettings(): Promise<YaomuBridgeResult<unknown>>;
  updateLocalApiSettings(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  issueLocalApiToken(): Promise<YaomuBridgeResult<unknown>>;
  revokeLocalApiToken(): Promise<YaomuBridgeResult<unknown>>;
  getEngineConfig(slot: string): Promise<YaomuBridgeResult<unknown>>;
  setEngineConfig(slot: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;

  // 项目
  listProjects(): Promise<YaomuBridgeResult<unknown>>;
  getProject(id: string): Promise<YaomuBridgeResult<unknown>>;
  createProject(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  updateProject(id: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;
  deleteProject(id: string): Promise<YaomuBridgeResult<unknown>>;
  importAssets(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  relocateAsset(assetId: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;

  // 任务 / 工作流
  listTasks(query?: unknown): Promise<YaomuBridgeResult<unknown>>;
  getTask(id: string): Promise<YaomuBridgeResult<unknown>>;
  getExecutionLog(id: string, options?: unknown): Promise<YaomuBridgeResult<unknown>>;
  cancelTask(id: string): Promise<YaomuBridgeResult<unknown>>;
  retryTask(id: string): Promise<YaomuBridgeResult<unknown>>;
  getRun(runId: string): Promise<YaomuBridgeResult<unknown>>;
  pauseRun(id: string): Promise<YaomuBridgeResult<unknown>>;
  resumeRun(id: string): Promise<YaomuBridgeResult<unknown>>;
  preflight(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  createWorkflowRun(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  getSnapshot(projectId: string): Promise<YaomuBridgeResult<unknown>>;

  // 字幕 / 候选 / 审核
  saveSubtitle(trackId: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;
  importSubtitleTrack(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  listCustomPipelineTemplates(): Promise<YaomuBridgeResult<unknown>>;
  saveCustomPipelineTemplates(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  generateCandidates(segmentId: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;
  listCandidates(segmentId: string, language: string): Promise<YaomuBridgeResult<unknown>>;
  selectCandidate(segmentId: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;
  getSelection(segmentId: string, language: string): Promise<YaomuBridgeResult<unknown>>;
  listReviews(projectId: string, runId?: string): Promise<YaomuBridgeResult<unknown>>;
  getReview(reviewId: string): Promise<YaomuBridgeResult<unknown>>;
  approveReview(reviewId: string, input: unknown): Promise<YaomuBridgeResult<unknown>>;

  // 导出 / 安装
  export(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  listExports(projectId: string): Promise<YaomuBridgeResult<unknown>>;
  getInstallations(): Promise<YaomuBridgeResult<unknown>>;
  planInstallation(slot: string): Promise<YaomuBridgeResult<unknown>>;
  install(input: unknown): Promise<YaomuBridgeResult<unknown>>;
  cancelInstallation(id: string): Promise<YaomuBridgeResult<unknown>>;
  rollbackInstallation(slot: string): Promise<YaomuBridgeResult<unknown>>;
  deleteInstallation(slot: string): Promise<YaomuBridgeResult<unknown>>;

  /** 订阅 Core 事件；返回取消函数，取消后不再回调。 */
  subscribeEvents(
    cb: (event: unknown) => void,
    onError?: (error: YaomuEventStreamError) => void,
    options?: YaomuEventSubscribeOptions,
  ): () => void;

  // 文件与系统能力（用户经系统对话框授权）
  selectFiles(options?: {
    title?: string;
    filters?: { name: string; extensions: string[] }[];
  }): Promise<string[]>;
  selectDirectory(options?: { title?: string }): Promise<string | null>;
  /** 主进程创建并授权的任务产出默认目录（<程序目录>/outputs）；Renderer 只读。 */
  getDefaultOutputDir(): Promise<string>;
  /** 系统目录选择器 + 主进程递归扫描；取消时 directory 为空串。 */
  selectDirectoryFiles(options?: {
    title?: string;
    extensions?: string[];
    limit?: number;
  }): Promise<YaomuDirectoryScanResult>;
  /** 解析用户真实选择/拖入的 File 的绝对路径；合成 File 返回空串。 */
  getPathForFile(file: File): string;
  /**
   * 媒体播放地址：`yaomu-asset://asset/<assetId>`，由主进程带实例令牌读取 Core。
   * 资产 ID 非法或非桌面环境返回空串；不暴露令牌、Core URL 或本机路径。
   */
  assetUrl(assetId: string): string;
  openPath(target: string): Promise<void>;
  /** 用户经系统保存对话框选择目标路径；取消返回 null。 */
  saveAsset(input: {
    assetId: string;
    suggestedName?: string;
    title?: string;
  }): Promise<{ path: string } | null>;
  /** 字幕等文本由系统保存对话框落盘；Renderer 不传任意目标路径。 */
  saveTextFile(input: {
    suggestedName?: string;
    text: string;
    title?: string;
    filters?: { name: string; extensions: string[] }[];
  }): Promise<{ path: string } | null>;
  notify(title: string, body: string, category?: string): Promise<void>;
  /** 将关闭行为同步给主进程（主进程据此决定拦截 close 与托盘生命周期）。 */
  setCloseBehavior(value: "quit" | "minimize"): Promise<void>;
  /**
   * 将“自动检查更新”开关同步给主进程（主进程持有权威值并持久化）。
   * 只影响打包态启动/切换时的自动检查；手动检查不受此开关限制。
   */
  setAutoCheckUpdates(value: boolean): Promise<void>;
  /**
   * 读取主进程持有的“自动检查更新”权威值。
   * 仅在 localStorage 没有持久布尔值时用于首次水合，避免界面默认 true 覆盖已保存的 false。
   */
  getAutoCheckUpdates(): Promise<boolean>;
  /**
   * 将“自动下载更新”开关同步给主进程（主进程持有权威值并持久化）。
   * 开启后背景下载，安装仍需用户确认。
   */
  setAutoDownloadUpdates(value: boolean): Promise<void>;
  /** 读取主进程持有的“自动下载更新”权威值；用于 localStorage 无水合值时初始化。 */
  getAutoDownloadUpdates(): Promise<boolean>;
  /** 桌面端界面缩放：由主进程对当前窗口应用 Chromium 页面缩放（0.8–1.5）。 */
  setUiScale(factor: number): Promise<void>;
}

interface Window {
  yaomu?: {
    platform: string;
    /** Linux 自绘标题栏控件（TopBar）；mac/win 用系统按钮不调用。 */
    window?: {
      minimize(): Promise<void>;
      toggleMaximize(): Promise<boolean>;
      close(): Promise<void>;
      isMaximized(): Promise<boolean>;
    };
    update: {
      check(): Promise<unknown>;
      download(): Promise<unknown>;
      /** 首个订阅者挂载后请求当前有限更新状态；旧 preload 可能没有。 */
      ready?(): Promise<unknown>;
      /** 显式同意/加入当前安装尝试；返回有限宿主快照。 */
      install(): Promise<YaomuUpdateInstallResult>;
      getInstallState(): Promise<YaomuUpdateInstallResult>;
      cancelInstall(): Promise<YaomuUpdateInstallResult>;
      /** 先登记推送再 attach；返回幂等取消函数。旧 preload 没有这些方法。 */
      onInstallState(cb: (snapshot: YaomuUpdateInstallSnapshot) => void): () => void;
      onStatus(cb: (status: string) => void): () => void;
      onAvailable(cb: (info: YaomuUpdateInfo) => void): () => void;
      onNotAvailable(cb: (info: YaomuUpdateInfo) => void): () => void;
      onProgress(cb: (progress: YaomuUpdateProgress) => void): () => void;
      onDownloaded(cb: (info: YaomuUpdateInfo) => void): () => void;
      onError(cb: (message: string) => void): () => void;
    };
    api: YaomuBridgeApi;
    /** 宿主初始化/就绪桥；旧 preload 没有该对象时按原加载路径，不等待、不落 mock。 */
    runtime?: YaomuRuntimeBridge;
  };
}

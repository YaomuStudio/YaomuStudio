// API 门面类型与错误。页面只 import { api } from "@/api" 与类型 from "@/domain/types"。
import type {
  Asset,
  AssetImportResult,
  AudioCandidate,
  CandidateSelection,
  Capability,
  CustomPipelineFeature,
  CustomPipelineTemplate,
  CustomPipelineTemplateList,
  DeviceInfo,
  DomainEvent,
  EngineConfig,
  EngineConfigUpdate,
  EnvironmentInfo,
  ErrorInfo,
  EraseBlock,
  ExecutionLog,
  ExportFormat,
  ExportKind,
  ExportRecord,
  ExtractOptions,
  ID,
  Installation,
  InstallationPlan,
  LocalApiSettings,
  LocalApiSettingsUpdate,
  LocalApiStats,
  LocalApiToken,
  ModelSlotKey,
  PreflightResult,
  Project,
  ProjectSnapshot,
  ProjectSummary,
  ReviewCurrentBinding,
  ReviewGate,
  ReviewType,
  Segment,
  SegmentCandidateJob,
  StorageCleanupCategory,
  StorageCleanupResult,
  StorageUsage,
  SubtitleRevision,
  SubtitleStyle,
  SubtitleTrack,
  SystemInfo,
  Task,
  TaskList,
  TaskStatus,
  WorkflowRun,
} from "@/domain/types";

export class ApiError extends Error {
  readonly code: string;
  readonly messageKey: string;
  readonly retryable: boolean;
  readonly actionIds: string[];
  readonly requestId?: string;
  readonly status?: number;

  constructor(info: ErrorInfo & { requestId?: string; status?: number }) {
    super(info.messageKey);
    this.name = "ApiError";
    this.code = info.code;
    this.messageKey = info.messageKey;
    this.retryable = info.retryable;
    this.actionIds = info.actionIds;
    this.requestId = info.requestId;
    this.status = info.status;
  }
}

/** 桌面桥/Core 不可用的统一安全错误：只暴露已注册文案键，不携带原始异常文本。 */
export function coreNotReadyError(): ApiError {
  return new ApiError({
    code: "CORE_NOT_READY",
    messageKey: "api.coreNotReady",
    retryable: true,
    actionIds: [],
  });
}

// ---- 输入 DTO（UI camelCase；桥接层负责转成契约的 snake_case） ----

export interface CreateProjectInput {
  name: string;
}

export interface UpdateProjectInput {
  name: string;
  /** 当前编辑版本；传入时 Core 做乐观并发检查。 */
  editVersion?: number;
}

export interface ImportAssetsInput {
  projectId: ID;
  /** 必须来自 selectFiles 的 file grant；不接受宿主任意路径。 */
  paths: string[];
  /** 默认引用；true 复制入库（FR-IMP-04）。 */
  copyMode: boolean;
}

export interface SubtitleOutlineStyleInput {
  color?: string;
  width?: number;
}

export interface SubtitleShadowStyleInput {
  color?: string;
  blur?: number;
  x?: number;
  y?: number;
}

export interface SubtitleStyleInput {
  x?: number;
  y?: number;
  font?: string;
  fontSize?: number;
  lineSpacing?: number;
  bold?: boolean;
  align?: "left" | "center" | "right";
  outline?: SubtitleOutlineStyleInput;
  shadow?: SubtitleShadowStyleInput;
  background?: string;
  maxTranslationLines?: number;
}

export interface CreateWorkflowRunInput {
  projectId: ID;
  name: string;
  template?: string;
  /** 仅 custom_pipeline；Core 生成固定节点闭包。 */
  features?: CustomPipelineFeature[];
  /** 省略表示自动识别；Core 归一为 und。 */
  sourceLanguage?: string;
  targetLanguages: string[];
  /** 源媒体 Asset.id；Core 冻结为 Run.Config.InputRevisionIDs，空输入不会推进 01。 */
  inputRevisionIds?: ID[];
  extract?: ExtractOptions;
  /** 复用分离音轨：Asset.id 且 Asset.type=audio_track（不是 SubtitleTrack.id）。 */
  reuseAudioTrackId?: ID;
  /** 复用的字幕轨：SubtitleTrack.id（不是 Asset.id）。 */
  reuseSubtitleTrackId?: ID;
  eraseMode?: "dialog_only" | "dialog_and_art";
  validateTimestamps?: boolean;
  adjustDuration?: boolean;
  bilingual?: boolean;
  subtitleStyle?: SubtitleStyleInput;
}

export interface SaveSubtitleInput {
  trackId: ID;
  baseRevisionId: ID;
  /** contentRevision / contentDigest 由 Core 计算，桥接层不提交。 */
  segments: Segment[];
  /**
   * 整轨替换：保存时必须回传当前 revision 已有的样式/擦除块，
   * 否则 Core 会按省略语义清空它们。
   */
  style?: SubtitleStyle;
  eraseBlocks?: EraseBlock[];
}

export interface GenerateCandidatesInput {
  segmentId: ID;
  language: string;
}

export interface ApproveReviewInput {
  reviewId: ID;
  revisionDigest: string;
  type: ReviewType;
  /**
   * 当前绑定 tuple：仅在 UI 已确认该 gate 的当前绑定时提交，Core 在批准事务内
   * 复核，不匹配即 409。旧 Core 请求解码器会拒绝未知字段（400），因此未知投影
   * （旧服务缺省 current_bindings）不发送该字段并 fail closed。
   */
  currentBinding?: ReviewCurrentBinding;
}

export interface ExportRequest {
  projectId: ID;
  runId?: ID;
  /** 导出语种；缺省使用 Run 的第一个目标语种。 */
  language?: string;
  kind: ExportKind;
  format: ExportFormat;
  /** Shell 目录选择器签发的授权令牌（当前 UI 传显示路径，见 open items）。 */
  outputDir: string;
  namingTemplate: string;
  inputRevisionId?: ID;
  /** 字幕 SRT 导出时生成原文+译文双行。 */
  bilingual?: boolean;
}

export interface ListTasksQuery {
  projectId?: ID;
  runId?: ID;
  status?: TaskStatus;
  cursor?: string;
  limit?: number;
}

/** 资产另存：目标路径只能由主进程的系统保存对话框取得。 */
export interface SaveAssetInput {
  assetId: ID;
  suggestedName?: string;
  title?: string;
}

export interface SavedAsset {
  path: string;
}

/** 从已导入 SRT Asset 创建原文轨；目标路径/文件内容由 Core 解析。 */
export interface ImportSubtitleTrackInput {
  projectId: ID;
  assetId: ID;
  language: string;
  /** transcription（默认）或 translation；克隆/翻译工具按用途导入。 */
  type?: "transcription" | "translation";
}

export interface InstallPlanRef {
  planId: ID;
  planHash: string;
}

/** 事件流不可恢复错误（游标过期、鉴权失败）；恢复需重新读取快照。 */
export interface EventStreamError {
  code: string;
  message: string;
  retryable: boolean;
}

/** 事件订阅选项：快照高水位握手时从 afterSeq 续订，避免回放与实时监听之间丢事件。 */
export interface EventSubscriptionOptions {
  afterSeq?: number;
  /**
   * 传输层每次成功建立 SSE（含自动重连）后的轻量通知；不是业务事件。
   * 消费者只做合并只读刷新，不得据此重新订阅或推进持久游标。
   */
  onConnected?: () => void;
}

export interface EventSubscription {
  (
    cb: (event: DomainEvent) => void,
    onError?: (error: EventStreamError) => void,
    options?: EventSubscriptionOptions,
  ): () => void;
}

/** UI 统一调用的 API 面。完整 Electron 桥优先；无桥时由显式开发演示或安全不可用实现承担。 */
export interface YaoMuApi {
  // 系统 / 能力 / 槽位配置
  getSystem(): Promise<SystemInfo>;
  getCapabilities(): Promise<Capability[]>;
  getDevices(): Promise<DeviceInfo[]>;
  getEnvironment(options?: { refresh?: boolean }): Promise<EnvironmentInfo>;
  getStorageUsage(): Promise<StorageUsage>;
  cleanupStorage(category: StorageCleanupCategory): Promise<StorageCleanupResult>;
  getLocalApiStats(): Promise<LocalApiStats>;
  getLocalApiSettings(): Promise<LocalApiSettings>;
  updateLocalApiSettings(input: LocalApiSettingsUpdate): Promise<LocalApiSettings>;
  issueLocalApiToken(): Promise<LocalApiToken>;
  revokeLocalApiToken(): Promise<void>;
  getEngineConfig(slot: ModelSlotKey): Promise<EngineConfig>;
  setEngineConfig(slot: ModelSlotKey, config: EngineConfigUpdate): Promise<EngineConfig>;

  // 项目
  listProjects(): Promise<ProjectSummary[]>;
  getProject(id: ID): Promise<Project>;
  createProject(input: CreateProjectInput): Promise<Project>;
  updateProject(id: ID, input: UpdateProjectInput): Promise<Project>;
  deleteProject(id: ID): Promise<void>;
  importAssets(input: ImportAssetsInput): Promise<AssetImportResult>;
  /**
   * 重新定位缺失的外部引用：path 必须来自 selectFiles。
   * 内容摘要须与导入时一致，否则 Core 返回 400。
   */
  relocateAsset(assetId: ID, path: string): Promise<Asset>;

  // 任务 / 工作流
  listTasks(query?: ListTasksQuery): Promise<TaskList>;
  getTask(id: ID): Promise<Task>;
  cancelTask(id: ID): Promise<void>;
  retryTask(id: ID): Promise<Task>;
  getRun(runId: ID): Promise<WorkflowRun>;
  pauseRun(id: ID): Promise<WorkflowRun>;
  resumeRun(id: ID): Promise<WorkflowRun>;
  preflight(input: CreateWorkflowRunInput): Promise<PreflightResult>;
  createWorkflowRun(input: CreateWorkflowRunInput): Promise<WorkflowRun>;
  /** 恢复项目视图：同一读事务内返回项目/运行/任务元数据与事件高水位。 */
  getSnapshot(projectId: ID): Promise<ProjectSnapshot>;

  /** 执行过程日志尾部；文件缺失或已清理时返回空 lines。 */
  getExecutionLog(
    taskId: ID,
    options?: { attempt?: number; tail?: number },
  ): Promise<ExecutionLog>;

  // 字幕 / 候选 / 审核
  saveSubtitle(input: SaveSubtitleInput): Promise<SubtitleRevision>;
  /** 202：只表示已受理，候选通过 listCandidates 读回。 */
  generateCandidates(input: GenerateCandidatesInput): Promise<SegmentCandidateJob>;
  listCandidates(segmentId: ID, language: string): Promise<AudioCandidate[]>;
  selectCandidate(segmentId: ID, language: string, candidateId: ID): Promise<CandidateSelection>;
  /** 尚无选择时返回 null（Core 返回 404 NOT_FOUND）。 */
  getSelection(segmentId: ID, language: string): Promise<CandidateSelection | null>;
  listReviews(projectId: ID, runId?: ID): Promise<ReviewGate[]>;
  getReview(reviewId: ID): Promise<ReviewGate>;
  approveReview(input: ApproveReviewInput): Promise<ReviewGate>;

  // 导出 / 安装
  export(input: ExportRequest): Promise<ExportRecord>;
  listExports(projectId: ID): Promise<ExportRecord[]>;
  getInstallations(): Promise<Installation[]>;
  planInstallation(slot: string): Promise<InstallationPlan>;
  /** 执行用户已同意的计划：必须携带 plan_id + plan_hash。 */
  install(input: InstallPlanRef): Promise<Installation>;
  cancelInstallation(id: ID): Promise<Installation>;
  rollbackInstallation(slot: string): Promise<Installation>;
  deleteInstallation(slot: string): Promise<void>;

  // 文件 / 字幕轨
  /** 用户经系统保存对话框选择目标路径；取消返回 null。 */
  saveAsset(input: SaveAssetInput): Promise<SavedAsset | null>;
  /** 从已导入 SRT Asset 创建字幕轨（原文或译文）。 */
  importSubtitleTrack(input: ImportSubtitleTrackInput): Promise<SubtitleTrack>;

  // 专属链路模板（本机持久化）
  listCustomPipelineTemplates(): Promise<CustomPipelineTemplateList>;
  saveCustomPipelineTemplates(items: CustomPipelineTemplate[]): Promise<CustomPipelineTemplateList>;

  // 事件
  subscribeEvents: EventSubscription;
}

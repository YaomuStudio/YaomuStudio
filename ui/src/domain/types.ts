// 领域类型：对齐 docs/data-and-storage.md、docs/local-api-contract.md、docs/workflow-and-review.md。
// 仅作 UI 呈现模型，不新增契约未定义字段。ID 一律不透明字符串，媒体时间整数毫秒，大小字节。

export type ID = string;
export type IsoDate = string; // UTC ISO 8601
export type Millis = number; // 媒体时间，整数毫秒
export type Bytes = number;

/** 语言（code 稳定，name 用于展示）。 */
export interface Language {
  code: string;
  name: string;
  nativeName?: string;
}

// ---------------------------------------------------------------------------
// 错误（对齐 docs/local-api-contract.md 错误格式）
// ---------------------------------------------------------------------------

export interface ErrorInfo {
  code: string;
  messageKey: string;
  retryable: boolean;
  actionIds: string[];
}

// ---------------------------------------------------------------------------
// 资产
// ---------------------------------------------------------------------------

export type AssetType =
  | "video"
  | "audio"
  | "subtitle"
  | "audio_track"
  | "image"
  | "archive"
  | "other";
export type AssetStatus = "importing" | "ready" | "missing" | "corrupt";

export interface MediaMeta {
  container: string;
  codec?: string;
  width?: number;
  height?: number;
  durationMs: Millis;
  audioTracks: number;
  sampleRate?: number;
}

export type AssetRole =
  | "original"
  | "voice"
  | "ambience"
  | "clean_video"
  | "source_subtitle"
  | "translated_subtitle"
  | "dub_audio"
  | "full_dub_audio"
  | "translated_video"
  | "final_video";

export interface Asset {
  id: ID;
  projectId: ID;
  type: AssetType;
  /** 产物语义角色；旧 Core 省略时按 type/顺序降级。 */
  role?: AssetRole;
  language?: string;
  /** 生成该产物的 Run；用户导入的原件省略。 */
  runId?: ID;
  name: string;
  path?: string;
  sizeBytes: Bytes;
  sha256?: string;
  meta?: MediaMeta;
  status: AssetStatus;
  importedAt: IsoDate;
}

/** 导入受理结果：长导入返回任务 ID，进度经任务快照或事件查询。 */
export interface AssetImportResult {
  assetIds: ID[];
  taskId?: ID;
}

// ---------------------------------------------------------------------------
// 字幕
// ---------------------------------------------------------------------------

export type TrackType = "transcription" | "translation" | "correction";

export interface Segment {
  id: ID;
  startMs: Millis;
  endMs: Millis;
  text: string;
  speaker?: string;
  contentRevision: number;
  contentDigest: string;
  /** 拆分/合并来源片段 ID。 */
  lineage?: ID[];
}

export interface SubtitleOutlineStyle {
  color?: string;
  width?: number;
}

export interface SubtitleShadowStyle {
  color?: string;
  blur?: number;
  x?: number;
  y?: number;
}

/** revision 级字幕样式；省略时回退 RunConfig.SubtitleStyle。 */
export interface SubtitleStyle {
  x?: number;
  y?: number;
  font?: string;
  fontSize?: number;
  lineSpacing?: number;
  bold?: boolean;
  align?: "left" | "center" | "right";
  outline?: SubtitleOutlineStyle;
  shadow?: SubtitleShadowStyle;
  background?: string;
  maxTranslationLines?: number;
}

/** revision 级擦除块占位（provisional）；当前只读，不派发擦除任务。 */
export interface EraseBlock {
  id?: ID;
  segmentId?: ID;
  startMs: Millis;
  endMs: Millis;
  bbox: [number, number, number, number];
  source?: "clean" | "source";
}

export interface SubtitleRevision {
  id: ID;
  trackId: ID;
  parentRevisionId?: ID;
  authorType: "system" | "user";
  segments: Segment[];
  inputRevisionId?: ID;
  style?: SubtitleStyle;
  eraseBlocks?: EraseBlock[];
  /** Core 派生的只读绑定摘要；样式/擦除块变化会改变该值。 */
  revisionDigest?: string;
  createdAt: IsoDate;
}

export interface SubtitleTrack {
  id: ID;
  projectId: ID;
  language: string;
  type: TrackType;
  currentRevisionId: ID;
  revisions: SubtitleRevision[];
}

// ---------------------------------------------------------------------------
// 工作流 / 管线（八节点）
// ---------------------------------------------------------------------------

export type RunStatus =
  | "DRAFT"
  | "QUEUED"
  | "RUNNING"
  | "PAUSE_REQUESTED"
  | "PAUSED"
  | "WAITING_REVIEW"
  | "SUCCEEDED"
  | "PARTIAL"
  | "FAILED"
  | "CANCELED";

export type PipelineNodeKey =
  | "01_import"
  | "02_analyze"
  | "03_separate"
  | "04_extract"
  | "05_erase"
  | "06_translate"
  | "07_clone"
  | "08_mix";

export type NodeStatus =
  | "pending"
  | "running"
  | "waiting_review"
  | "done"
  | "skipped"
  | "reused"
  | "failed";

export interface PipelineNodeState {
  key: PipelineNodeKey;
  status: NodeStatus;
  /** 0-100 */
  progress: number;
  detail?: string;
}

/** Run 创建参数的只读回显（旧 Run 可能省略）。 */
export interface RunParams {
  eraseMode?: "dialog_only" | "dialog_and_art";
  validateTimestamps?: boolean;
  adjustDuration?: boolean;
  bilingual?: boolean;
  extractMode?: "asr" | "ocr" | "asr_ocr";
  ocrLanguage?: "chinese" | "chinese_traditional" | "english" | "japanese";
  ocrQuality?: "standard" | "fine";
  asrLanguage?: string;
  /** 创建时冻结的运行级字幕样式；revision 未设置样式时作为回退。 */
  subtitleStyle?: SubtitleStyle;
}

export interface WorkflowRun {
  id: ID;
  projectId: ID;
  name: string;
  template: "full_pipeline" | "custom_pipeline" | string;
  /** 能力组合类型；旧 Core 省略时按节点推导。 */
  modeType?: string;
  status: RunStatus;
  sourceLanguage: string;
  languages: string[];
  /** 创建时冻结的输入资产 ID（Core 派生只读字段，顺序与创建请求一致）。 */
  inputAssetIds: ID[];
  nodes: PipelineNodeState[];
  params?: RunParams;
  createdAt: IsoDate;
  updatedAt: IsoDate;
}

/** 专属链路功能（contracts/openapi.yaml CustomPipelineFeature）。 */
export type CustomPipelineFeature = "separation" | "erase" | "translate" | "clone" | "mix";

/** 专属链路本机模板；只保存配置，不保存文件。 */
export interface CustomPipelineTemplate {
  id: ID;
  name: string;
  features: CustomPipelineFeature[];
  languages: string[];
  sourceLanguage: string;
  eraseMode?: "dialog_only" | "dialog_and_art";
  ocrLanguage?: "chinese" | "chinese_traditional" | "english" | "japanese";
  validateTimestamps: boolean;
  adjustDuration: boolean;
  createdAt: IsoDate;
  updatedAt: IsoDate;
}

export interface CustomPipelineTemplateList {
  items: CustomPipelineTemplate[];
}

/** 字幕提取方式三选一（FR-TOOL-04）。 */
export interface ExtractOptions {
  mode: "asr" | "ocr" | "asr_ocr";
  ocrLanguage?: "chinese" | "chinese_traditional" | "english" | "japanese";
  ocrQuality?: "standard" | "fine";
}

// ---------------------------------------------------------------------------
// 任务 / 尝试 / 产物
// ---------------------------------------------------------------------------

export type TaskStatus =
  | "QUEUED"
  | "WAITING_RESOURCE"
  | "RUNNING"
  | "PAUSE_REQUESTED"
  | "PAUSED"
  | "SUCCEEDED"
  | "FAILED"
  | "CANCEL_REQUESTED"
  | "CANCELED"
  | "INTERRUPTED";

/** install 不是 TaskKind：模型安装走 Installation 流程。 */
export type TaskKind = "node" | "segment" | "export";

export interface TaskStep {
  stepId: ID;
  status: "PENDING" | "RUNNING" | "COMMITTING" | "SUCCEEDED" | "FAILED" | "CANCELED" | "INTERRUPTED";
  attemptNo?: number;
  epoch?: number;
}

export interface Task {
  id: ID;
  projectId: ID;
  runId?: ID;
  kind: TaskKind;
  nodeKey?: PipelineNodeKey;
  /** 句段任务的稳定 Segment ID。 */
  segmentId?: ID;
  /** 节点/句段任务的目标语言；02/03/05 为空。08 预览按 run + language 定位。 */
  language?: string;
  label: string;
  status: TaskStatus;
  /**
   * 0-100；缺失/非法快照值保持 undefined（未知），不伪造 0。
   * 100 不代表成功，终态只由关键事件/快照决定。
   */
  progress?: number;
  steps?: TaskStep[];
  artifactIds: ID[];
  error?: ErrorInfo;
  createdAt: IsoDate;
  updatedAt: IsoDate;
}

/** 任务分页（GET /tasks 返回 {items,next_cursor}）。 */
export interface TaskList {
  items: Task[];
  nextCursor?: string;
}

/** 执行过程行来自 cache，只用于界面回放，不作为任务成功条件。 */
export type ExecutionLogStream = "stderr" | "stdout" | "runner";

export interface ExecutionLogLine {
  seq: number;
  stream: ExecutionLogStream;
  line: string;
  at?: IsoDate;
}

export interface ExecutionLog {
  taskId: ID;
  /** 实际读取的 attempt；0 表示没有可读日志。 */
  attempt: number;
  lines: ExecutionLogLine[];
}

export interface Attempt {
  id: ID;
  taskId: ID;
  attemptNo: number;
  status: TaskStatus;
}

export interface ArtifactCommit {
  id: ID;
  attemptId: ID;
  assetIds: ID[];
  committedAt: IsoDate;
}

// ---------------------------------------------------------------------------
// 人工审核（ReviewGate）
// ---------------------------------------------------------------------------

export type ReviewType = "translation" | "delivery";
export type ReviewStatus = "pending" | "approved";

/**
 * Core 在同一读事务派生的当前绑定来源。UI 只按 tuple 匹配，不计算摘要：
 * - translation_revision：06 当前译文 revision
 * - delivery_revision：交付字幕的当前译文 revision
 * - delivery_mix：该语种最新成功 08 任务
 */
export type ReviewBindingSourceKind =
  | "translation_revision"
  | "delivery_revision"
  | "delivery_mix";

export interface ReviewCurrentBinding {
  language: string;
  sourceKind: ReviewBindingSourceKind;
  sourceId: ID;
}

export interface ReviewGate {
  id: ID;
  runId: ID;
  type: ReviewType;
  language?: string;
  revisionDigest: string;
  status: ReviewStatus;
  confirmedAt?: IsoDate;
  /**
   * 只读当前绑定投影。缺省表示旧 Core 未提供（未知，UI 必须 fail closed）；
   * 空数组表示该历史记录不再对应当前内容。不得据此在本地持久化或推导摘要。
   */
  currentBindings?: ReviewCurrentBinding[];
}

// ---------------------------------------------------------------------------
// 配音候选 / 选择
// ---------------------------------------------------------------------------

export interface AudioCandidate {
  id: ID;
  segmentId: ID;
  language: string;
  revision: number;
  modelVersion: string;
  audioAssetId: ID;
  isRecommended: boolean;
  createdAt: IsoDate;
}

export interface CandidateSelection {
  segmentId: ID;
  language: string;
  candidateId: ID;
  revision: number;
  selectedAt: IsoDate;
}

/** 句级重抽受理（202）：只表示已创建新 attempt，候选通过列表读回。 */
export interface SegmentCandidateJob {
  taskId: ID;
  segmentId: ID;
  language: string;
  attemptNo: number;
}

// ---------------------------------------------------------------------------
// 能力目录
// ---------------------------------------------------------------------------

export type CapabilitySlotKey =
  | "asr"
  | "translate"
  | "tts"
  | "voice-clone"
  | "subtitle-erase"
  | "separation"
  | "ocr"
  | "ffmpeg";

export type ModelSlotKey =
  | "asr"
  | "translate"
  | "tts"
  | "voice-clone"
  | "subtitle-erase"
  | "separation";

export type SlotState =
  | "UNKNOWN"
  | "UNSUPPORTED"
  | "UNVERIFIED"
  | "NEEDS_SETUP"
  | "INSTALLING"
  | "VERIFYING"
  | "READY"
  | "NEEDS_ACTION"
  | "RESTART_REQUIRED"
  | "FAILED"
  | "DEGRADED";

export interface ParamField {
  key: string;
  labelKey: string;
  type: "string" | "number" | "boolean" | "enum";
  options?: { value: string; labelKey: string }[];
  min?: number;
  max?: number;
  default?: unknown;
}

export interface EngineOption {
  id: string;
  name: string;
  version?: string;
  state: SlotState;
  installed: boolean;
  selected: boolean;
  device?: string;
  source?: string;
  sizeBytes?: number;
}

export interface Capability {
  slot: CapabilitySlotKey;
  /** UI 派生的槽位文案键（Core 只返回 slot）。 */
  nameKey: string;
  state: SlotState;
  engineName?: string;
  version?: string;
  languages?: string[];
  params?: ParamField[];
  /** 可选的本地引擎目录；旧 Core 省略时只展示当前引擎。 */
  engines?: EngineOption[];
  reasonCode?: string;
  actionIds?: string[];
  /** 安装进度 0-100 */
  progress?: number;
  /** 槽位执行占用状态；running 表示有任务正在执行，不表示权重常驻。 */
  runtimeState?: "idle" | "running";
  /** 该槽位当前运行中的任务数。 */
  runningTasks?: number;
  /** FR-MODEL-04：是否有可用更新。 */
  updateAvailable: boolean;
  updateVersion?: string;
  updateNotes?: string;
}

// ---------------------------------------------------------------------------
// 预检
// ---------------------------------------------------------------------------

export type PreflightSeverity = "pass" | "warning" | "blocker";

export interface PreflightIssue {
  severity: PreflightSeverity;
  code: string;
  messageKey: string;
  actionIds: string[];
}

export interface PreflightResult {
  planId?: string;
  planHash?: string;
  issues: PreflightIssue[];
}

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

export type ExportKind = "video" | "audio" | "subtitle";

/** 实际编码/容器格式（FR-EXP-02）；与 kind 的一致性由 Core 校验。 */
export type ExportFormat = "mp4_h264" | "srt" | "ass" | "wav" | "mp3";

export interface ExportRecord {
  id: ID;
  runId?: ID;
  projectId: ID;
  kind: ExportKind;
  format: ExportFormat;
  status: TaskStatus;
  outputPath?: string;
  createdAt: IsoDate;
}

// ---------------------------------------------------------------------------
// 项目 / 安装 / 系统
// ---------------------------------------------------------------------------

export interface Project {
  id: ID;
  workspaceId: ID;
  /** 集 ID；旧项目省略时卡片回退项目 ID。 */
  episodeId?: string;
  name: string;
  /** 应用管理的项目目录绝对路径（Core 派生字段）；读取资产仍走受控内容接口。 */
  directory: string;
  /** 项目数据 schema 版本（迁移与降级检查）。 */
  schemaVersion: number;
  /** 编辑版本（乐观并发检查）。 */
  editVersion: number;
  createdAt: IsoDate;
  updatedAt: IsoDate;
  lastOpenedAt?: IsoDate;
  assets: Asset[];
  tracks: SubtitleTrack[];
  runs: WorkflowRun[];
}

/** 项目列表用的轻量摘要。 */
export interface ProjectSummary {
  /** 本地项目序号（创建顺序，单调递增、不复用）；旧 Core 缺省时回退短 ID。 */
  seq?: number;
  id: ID;
  name: string;
  editVersion: number;
  episodeId?: string;
  modeType?: string;
  createdAt: IsoDate;
  updatedAt: IsoDate;
  lastOpenedAt?: IsoDate;
  assetCount: number;
  runCount: number;
  latestRunStatus?: RunStatus;
  totalDurationMs?: Millis;
  /** 最近一次 Run 的当前节点任务在等待执行器/资源；UI 用“等待资源”替代“运行中”。 */
  waitingResource?: boolean;
}

/** 第三方本机 API 统计（FR-SET-32，不含素材内容）。 */
export interface LocalApiStats {
  enabled: boolean;
  totalCalls: number;
  byEndpoint: { endpoint: string; calls: number; lastError?: string }[];
}

/** 第三方本机 API 设置（FR-SET-30/31/33）；不包含明文令牌。 */
export interface LocalApiSettings {
  enabled: boolean;
  concurrencyLimit: number;
  tokenPresent: boolean;
  tokenMasked?: string;
}

export interface LocalApiSettingsUpdate {
  enabled?: boolean;
  concurrencyLimit?: number;
}

/** 新签发的第三方本机 API 令牌；明文只在签发响应出现一次。 */
export interface LocalApiToken {
  token: string;
  issuedAt: IsoDate;
}

export type InstallationPhase =
  | "planning"
  | "downloading"
  | "verifying"
  | "installing"
  | "done"
  | "failed"
  | "canceled";

export interface ReleaseRef {
  releaseId: ID;
  version: string;
  digest: string;
  channel?: string;
}

export interface LicenseSummary {
  licenseId: string;
  licenseName: string;
  noticeKeys?: string[];
}

/** 安装计划：绑定用户同意的下载/权限范围与计划哈希。 */
export interface InstallationPlan {
  planId: ID;
  planHash: string;
  /** 发起请求的入口槽位；不是同意范围的全部。 */
  slot: CapabilitySlotKey;
  /**
   * 已准入发布声明的完整覆盖范围（排序、唯一、含 slot）。
   * 新 Core 必返；字段缺失仅表示旧 Core，此时按原单槽语义展示。
   * 显式空数组或未包含 slot 的范围不合法，不能据此确认安装。
   */
  coveredSlots?: CapabilitySlotKey[];
  /** 同一物理安装的复用/加入目标；存在时不启动第二份下载。 */
  reuseInstallationId?: ID;
  release: ReleaseRef;
  downloadBytes: Bytes;
  cacheReuseBytes: Bytes;
  installBytes: Bytes;
  peakStagingBytes?: Bytes;
  permissions: string[];
  license: LicenseSummary;
  requiresRestart?: boolean;
}

export interface Installation {
  id: ID;
  /** 发起槽位；共享安装完成后仍保留该历史身份。 */
  slot: CapabilitySlotKey;
  /**
   * 原始冻结同意范围；旧记录只有原 slot。解绑不改写此历史事实。
   * 字段缺失=旧 Core（按单槽显示）；显式空数组非法。
   */
  coveredSlots?: CapabilitySlotKey[];
  /**
   * 仍引用该物理记录的槽位（含保留历史）。
   * 字段缺失=旧 Core（按单槽显示）；显式空数组不得回退发起 slot。
   */
  boundSlots?: CapabilitySlotKey[];
  /**
   * 当前选中此 Done 实体的槽位；未完成记录为空。
   * 字段缺失=旧 Core；显式空数组不得回退发起 slot。
   */
  currentSlots?: CapabilitySlotKey[];
  phase: InstallationPhase;
  /** 0-100 */
  progress: number;
  verifiedFiles?: number;
  error?: ErrorInfo;
  retryable?: boolean;
  planHash?: string;
}

export interface DeviceInfo {
  id: string;
  name: string;
  kind: "cpu" | "cuda" | "mps";
  recommended: boolean;
  /** 是否为当前生效设备。 */
  selected: boolean;
  memoryTotalBytes?: Bytes;
  memoryUsedBytes?: Bytes;
  inUse: boolean;
  /** GPU 驱动版本；CPU/MPS 或未知时省略。 */
  driverVersion?: string;
}

export interface WSLStatus {
  available: boolean;
  version?: 1 | 2;
  cudaAvailable?: boolean;
}

export interface EnvironmentCheck {
  code: string;
  severity: PreflightSeverity;
  messageKey: string;
  actionIds: string[];
}

export interface EnvironmentInfo {
  platform: "windows" | "darwin" | "linux";
  wsl?: WSLStatus;
  devices: DeviceInfo[];
  checks: EnvironmentCheck[];
}

/** 槽位受控配置（GET /engines/{slot}/config）；设备切换与参数只对后续任务生效。 */
export interface EngineConfig {
  slot: ModelSlotKey;
  /** 当前选中的引擎；旧 Core 省略时按 engineName/version 回显。 */
  engineId?: string;
  /** `auto` 或 DeviceInfo.id；缺省为 auto。 */
  deviceId?: string;
  /** 受控参数值，键与范围来自能力目录的 ParamField。 */
  values?: Record<string, unknown>;
  updatedAt?: IsoDate;
}

export interface EngineConfigUpdate {
  engineId?: string;
  deviceId?: string;
  values?: Record<string, unknown>;
}

/** 恢复项目视图的单事务快照（GET /snapshot）；eventSeq 是事件高水位 H。 */
export interface ProjectSnapshot {
  projectId: ID;
  project: ProjectSummary;
  runs: WorkflowRun[];
  tasks: Task[];
  /** 随后从 /events?after_seq=H 订阅；大内容按 revision 另取。 */
  eventSeq: number;
}

export interface StorageUsage {
  models: Bytes;
  media: Bytes;
  cache: Bytes;
  exports: Bytes;
}

export type StorageCleanupCategory = "models" | "cache";

export interface StorageCleanupResult {
  category: StorageCleanupCategory;
  freedBytes: Bytes;
}

/** 实例握手与启动阶段。 */
export interface SystemInfo {
  appId: string;
  appVersion?: string;
  protocolVersion: string;
  instanceId: ID;
  workspaceId: ID;
  serverEpoch: number;
  coreReady: boolean;
}

// ---------------------------------------------------------------------------
// 事件（对齐 docs/local-api-contract.md 事件结构）
// ---------------------------------------------------------------------------

export interface DomainEvent {
  schemaVersion: number;
  seq: number;
  type: string;
  projectId?: ID;
  taskId?: ID;
  stateVersion?: number;
  data?: unknown;
}

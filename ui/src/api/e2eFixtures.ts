// e2e 测试夹具后端：仅 Playwright 测试构建（VITE_E2E_FIXTURES=1）使用。
// 产品演示模式已移除；普通开发/生产构建由 vite.config.ts 别名到 e2eSource.disabled.ts，
// 不会加载本模块。数据与领域模型对齐 domain/types.ts；方法签名对齐 api/types.ts 的 YaoMuApi。
import type {
  Asset,
  AssetImportResult,
  AudioCandidate,
  CandidateSelection,
  Capability,
  CapabilitySlotKey,
  CustomPipelineFeature,
  CustomPipelineTemplate,
  CustomPipelineTemplateList,
  DeviceInfo,
  DomainEvent,
  EngineConfig,
  EnvironmentInfo,
  EraseBlock,
  ExecutionLog,
  ExecutionLogLine,
  ExecutionLogStream,
  ExportRecord,
  ID,
  Installation,
  InstallationPlan,
  LocalApiSettings,
  LocalApiStats,
  LocalApiToken,
  ModelSlotKey,
  PreflightIssue,
  Project,
  ProjectSnapshot,
  ProjectSummary,
  ReleaseRef,
  ReviewCurrentBinding,
  ReviewGate,
  RunParams,
  RunStatus,
  Segment,
  SegmentCandidateJob,
  SlotState,
  StorageUsage,
  SubtitleRevision,
  SubtitleStyle,
  SubtitleTrack,
  SystemInfo,
  Task,
  TaskList,
  WorkflowRun,
} from "@/domain/types";
import { CAPABILITY_SLOTS, PIPELINE_NODE_KEYS } from "@/domain/capabilities";
import {
  installationBoundSlots,
  installationCoveredSlots,
  installationCurrentSlots,
  isInstallationActive,
} from "@/domain/installations";
import {
  ApiError,
  coreNotReadyError,
  type ApproveReviewInput,
  type CreateWorkflowRunInput,
  type ExportRequest,
  type ImportSubtitleTrackInput,
  type SaveAssetInput,
  type YaoMuApi,
} from "./types";

/**
 * E2E 故障注入（仅开发/测试）：
 * 只有 `import.meta.env.DEV` 为 true 时才读取 `window.__yaomuMock.fail`。
 * Vite 在生产构建把该条件替换为 false，开关不会生效；Electron 有 preload 桥时也不使用 mock。
 * 被测页面通过 `window.__yaomuMock = { fail: "listProjects" }` 注入，清空后重试可恢复。
 */
type MockFault = "listProjects" | "getProject";

interface MockControl {
  fail?: MockFault | MockFault[];
  /** 仅供 e2e 覆盖环境检查；生产构建不会读取。 */
  environment?: EnvironmentInfo;
  /** 仅供 e2e 替换项目列表（如强制空态验证无项目引导）；生产构建不会读取。 */
  projectListOverride?: ProjectSummary[];
  /** 仅供 e2e 断言创建 Run 的入参；生产构建不会读取。 */
  lastRunInput?: CreateWorkflowRunInput;
  /** 仅供 e2e 断言导出请求；生产构建不会读取。 */
  lastExportInput?: ExportRequest;
  /** 仅供 e2e 断言批量导出的逐项请求。 */
  exportInputs?: ExportRequest[];
  /** 仅供 e2e 模拟导出失败。 */
  exportFailure?: boolean;
  /** 仅供 e2e 断言资产另存入参。 */
  lastSaveAssetInput?: SaveAssetInput;
  /** 仅供 e2e 断言资产另存请求次数（locale/重渲染不得重发保存）。 */
  saveAssetCalls?: number;
  /** 仅供 e2e 模拟资产另存失败。 */
  saveAssetFailure?: boolean;
  /** 仅供 e2e 断言导出请求次数（locale/重渲染不得重发导出）。 */
  exportCalls?: number;
  /** 仅供 e2e 断言计算设备配置请求次数（locale/重渲染不得重发切换）。 */
  engineConfigCalls?: number;
  /** 仅供 e2e 断言任务重试/取消请求次数（locale/重渲染不得重发动作）。 */
  retryTaskCalls?: number;
  cancelTaskCalls?: number;
  /** 仅供 e2e 让任务重试/取消以未知错误失败（验证安全回退且不暴露正文）。 */
  taskActionFailure?: boolean;
  /** 仅供 e2e 覆盖项目摘要的 latestRunStatus（验证任务中心计数文案）。 */
  projectRunStatusFixtures?: Partial<Record<ID, RunStatus>>;
  projectWaitingResourceFixtures?: Partial<Record<ID, boolean>>;
  /** 仅供 e2e 断言环境检查请求次数。 */
  environmentCalls?: number;
  /** 仅供 e2e 模拟环境检查以未知错误失败（验证不暴露原始 Error.message）。 */
  environmentFailure?: boolean;
  /** 仅供 e2e 断言 SRT 导入轨入参。 */
  lastImportTrackInput?: ImportSubtitleTrackInput;
  /** 仅供 e2e 断言多文件导入（克隆）。 */
  lastImportTrackInputs?: ImportSubtitleTrackInput[];
  /** 仅供 e2e 模拟克隆无候选。 */
  voiceCloneNoCandidates?: boolean;
  /** 仅供 e2e 模拟工具 Run 失败。 */
  toolRunFailure?: boolean;
  /** 仅供 e2e 让接下来 N 次 Run 读取返回可重试的 Core 暂时不可用。 */
  getRunFailures?: number;
  /** 仅供 e2e 模拟 PARTIAL：只完成第一个目标语言。 */
  toolRunPartial?: boolean;
  /** 仅供 e2e 模拟 PARTIAL 且一个目标语言都没产出（翻译执行失败）。 */
  toolRunPartialNoTracks?: boolean;
  /** 仅供 e2e 延长工具 Run 的完成延迟（毫秒），验证运行中状态展示。 */
  toolRunDelayMs?: number;
  /** 仅供 e2e 模拟专属链路无候选。 */
  customNoCandidates?: boolean;
  /** 仅供 e2e 跳过专属链路审核门，直接完成。 */
  customSkipReview?: boolean;
  /** 仅供 e2e 断言模板保存入参。 */
  lastTemplateSave?: CustomPipelineTemplate[];
  /** 仅供 e2e 断言最近一次保存字幕产生的新 revision（核心分配 ID/lineage）。 */
  lastSavedRevision?: SubtitleRevision;
  /** 仅供 e2e 断言逐轨保存结果（源轨/译文轨各自 revision）。 */
  lastSavedTracks?: { trackId: string; text: string; startMs: number; endMs: number }[];
  /** 仅供 e2e 断言字幕保存尝试次数（快捷键/按钮不得重复提交）。 */
  saveSubtitleCalls?: number;
  /** 仅供 e2e 断言保存后该项目的最新审核门槛（旧门槛失效、新 pending 生成）。 */
  lastSavedReviews?: ReviewGate[];
  /** 仅供 e2e 记录审核批准请求，验证 dirty/saving 时不会发出批准。 */
  approveReviewInputs?: ApproveReviewInput[];
  /**
   * 仅供 e2e：译文审核放行后模拟 07/08 完成并停在交付审核。
   * 默认关闭；交付审核仍需用例/用户显式放行，不模拟自动放行 delivery。
   */
  remixOnApprove?: boolean;
  /** 仅供 e2e 用显式 fixture 替换审核列表（含 currentBindings 原样返回，不重新投影）。 */
  reviewFixtures?: ReviewGate[];
  /** 仅供 e2e 覆盖 Run 目标语种（多语种交付审核选择）。 */
  reviewRunLanguages?: string[];
  /** 仅供 e2e 覆盖 Run 终态/暂停（验证批准入口与导出例外）。 */
  runStatusOverride?: WorkflowRun["status"];
  /** 仅供 e2e 挂起审核列表读取；释放后返回调用时捕获的旧列表。 */
  reviewListGate?: Promise<void>;
  releaseReviewList?: () => void;
  /** 仅供 e2e 挂起批准回包；用于验证切换上下文后旧回包不写回。 */
  approveReviewGate?: Promise<void>;
  releaseApproveReview?: () => void;
  /** 仅供 e2e 让批准后的权威重读失败，验证 UI 不假装已同步。 */
  reviewRefreshFailure?: boolean;
  /** 仅供 e2e 让所有审核列表读取持续失败，验证 UI 不把读取失败当作“无门槛”。 */
  reviewReadFailure?: boolean;
  /** 内部：批准后下一次审核列表读取失败（由 reviewRefreshFailure 触发）。 */
  failNextReviewList?: boolean;
  /** 仅供 e2e 让下一次保存挂起，直到用例显式释放；不引入固定延时。 */
  saveSubtitleGate?: Promise<void>;
  /** 仅供 e2e 模拟保存失败（409 冲突），验证草稿保留与审核继续阻断。 */
  saveSubtitleFailure?: "conflict";
  /** 仅供 e2e 模拟“外部源文件已缺失”：导入时按文件名把这些资产标为 missing。 */
  missingImportNames?: string[];
  /** 仅供 e2e 断言最近一次重定位入参。 */
  lastRelocateInput?: { assetId: ID; path: string };
  /** 仅供 e2e 覆盖槽位准入状态；缺省使用默认 READY。生产构建不会读取。 */
  capabilityStates?: Partial<Record<CapabilitySlotKey, SlotState>>;
  /** 仅供 e2e 覆盖槽位引擎/版本/语言等展示字段；生产构建不会读取。 */
  capabilityFixtures?: Partial<Record<CapabilitySlotKey, Partial<Capability>>>;
  /** 仅供 e2e 断言恢复入口只导航、未请求安装计划。 */
  lastPlanSlot?: CapabilitySlotKey;
  /** 仅供 e2e 覆盖计划的覆盖范围；null 表示省略字段（旧 Core 单槽形状）。 */
  planCoverage?: Partial<Record<CapabilitySlotKey, CapabilitySlotKey[] | null>>;
  /** 仅供 e2e 强制计划的复用目标 ID（可为不存在的 ID，验证 stale 失败）。 */
  planReuse?: Partial<Record<CapabilitySlotKey, ID>>;
  /** 仅供 e2e 预置安装记录（含旧 Core 缺范围字段的单槽形状）。 */
  installationFixtures?: Installation[];
  /** 仅供 e2e 断言物理安装启动次数（一次共享安装只应 +1）。 */
  installationStarts?: number;
  /** 仅供 e2e 断言 install 请求次数（locale/重渲染不得重发安装）。 */
  installRequests?: number;
  /** 仅供 e2e 断言计划请求次数。 */
  planRequests?: number;
  /** 仅供 e2e 断言取消命中的物理安装 ID。 */
  cancelInstallationIds?: ID[];
  /** 仅供 e2e 模拟一次物理安装完成（全部覆盖槽同一事务激活）。 */
  completeInstallation?: (installationId: ID) => void;
  /** 仅供 e2e 模拟一次物理安装失败（不激活新版本）。 */
  failInstallation?: (installationId: ID) => void;
  /** 仅供 e2e 推送共享安装进度（同一 ID 影响全部引用槽的显示）。 */
  setInstallationProgress?: (installationId: ID, progress: number) => void;
  /** 仅供 e2e 读取 mock 侧安装快照（验证一次物理实体/引用投影）。 */
  readInstallations?: () => Installation[];
  /** 仅供 e2e 覆盖预检问题列表（验证去重与安全回退）。生产构建不会读取。 */
  preflightIssues?: PreflightIssue[];
  /** 仅供 e2e 让预检抛出非 ApiError（验证不展示原始 Error.message）。 */
  preflightError?: string;
  /** 仅供 e2e 替换指定项目的资产（卡片下载/预览命名组合）；生产构建不会读取。 */
  assetFixtures?: Record<ID, Asset[]>;
  /** 仅供 e2e 覆盖 Run.params 冻结值；null 表示该字段未记录。生产构建不会读取。 */
  runParamsFixtures?: Record<ID, Record<string, boolean | null>>;
  /** 仅供 e2e 预置旧 custom 模板（含 adjustDuration=true）。生产构建不会读取。 */
  customTemplateFixtures?: CustomPipelineTemplate[];
  /** 仅供 e2e 模拟 07 时间窗预检问题；仅 validate_timestamps=true 时返回。 */
  cloneTimelineIssue?: "out_of_range" | "unknown_duration";
  /** 仅供 e2e 记录测试夹具调用，验证宿主 ready 前不读取业务资源。 */
  businessCalls?: string[];
  /** 仅供 e2e 挂起下一次 getProject；晚回包用于验证旧 generation 读取被丢弃。 */
  getProjectGate?: Promise<void>;
  releaseGetProject?: () => void;
  /** 仅供 e2e 观察 getProject 是否已在飞；用于可释放读取屏障。 */
  getProjectCalls?: number;
  /** 仅供 e2e 覆盖项目名，制造旧读回包与新连接结果不同的场景。 */
  projectNameFixtures?: Record<ID, string>;
  /** 仅供 e2e 模拟旧 revision 缺少 input_revision_id。 */
  clearTranslationInput?: boolean;

  // ---- 任务进度/连接通知/日志的可释放测试控制（仅测试夹具） ----
  /** 持久事件广播（会进入演示回放日志）。 */
  emitDomainEvent?: (type: string, payload?: Partial<DomainEvent>) => void;
  /** 原始事件注入：允许构造非法 schema/seq/data，消费者必须自行 fail-closed。 */
  emitRawEvent?: (event: unknown) => void;
  /** 构造合法 task.progress（seq=0，不进入持久回放）。 */
  emitTaskProgress?: (input: {
    taskId: ID;
    projectId?: ID;
    stateVersion: number;
    stepId: string;
    attempt: number;
    epoch: number | string;
    progress: number;
  }) => void;
  /** 修改演示任务（状态/进度/steps 等），用于终态、重试和未知进度场景。 */
  setTask?: (taskId: ID, patch: Partial<Task>) => void;
  /** 追加一条演示任务（模拟快照窗口内新建任务）。 */
  addTask?: (task: Task) => void;
  /** 模拟 Main 侧成功 SSE 连接通知（初连/重连），不携带业务事件。 */
  emitEventStreamConnected?: () => void;
  /** 模拟已退订旧流的迟到事件（UI 必须按订阅代次丢弃）。 */
  emitStaleSubscriptionEvent?: (event: unknown) => void;
  /** 模拟已退订旧流的迟到连接通知。 */
  emitStaleSubscriptionConnected?: () => void;
  eventSubscriptionCount?: number;
  connectedNotificationCount?: number;
  /** 旧订阅连接回调实际被调用次数（验证 UI 代次守卫确实被测到）。 */
  staleConnectedCalls?: number;

  // 可释放读取屏障：捕获调用时的快照，释放后仍返回旧数据。
  listTasksGate?: Promise<void>;
  releaseListTasks?: () => void;
  getSnapshotGate?: Promise<void>;
  releaseGetSnapshot?: () => void;
  getTaskGate?: Promise<void>;
  releaseGetTask?: () => void;
  executionLogGate?: Promise<void>;
  releaseExecutionLog?: () => void;
  listTasksCalls?: number;
  getSnapshotCalls?: number;
  getTaskCalls?: number;
  executionLogCalls?: number;

  /** 追加一条执行过程日志事件（新 attempt 会替换旧缓存）。 */
  appendExecutionLog?: (
    taskId: ID,
    attempt: number,
    stream: ExecutionLogStream,
    line: string,
  ) => void;
  lastExecutionLogTaskId?: ID;
  lastExecutionLogAttempt?: number;
}

function mockControl(): MockControl | undefined {
  if (!import.meta.env.DEV) return undefined;
  return (window as Window & { __yaomuMock?: MockControl }).__yaomuMock;
}

function throwMockFault(fault: MockFault): void {
  const configured = mockControl()?.fail;
  const hit =
    configured === fault || (Array.isArray(configured) && configured.includes(fault));
  if (!hit) return;
  throw coreNotReadyError();
}

let seq = 0;
interface MockSubscription {
  cb: (event: DomainEvent) => void;
  onConnected?: () => void;
  active: boolean;
}
const listeners = new Set<MockSubscription>();
/** 已退订的旧订阅：仅用于模拟迟到回调，验证 UI 代次守卫。 */
const staleSubscriptions: MockSubscription[] = [];
/** 有界事件日志：mock 下模拟可回放游标（快照高水位握手可验证）。 */
const eventLog: DomainEvent[] = [];
const EVENT_LOG_LIMIT = 500;

const now = (): string => new Date().toISOString();
const id = (p: string): string => `${p}-${Math.random().toString(36).slice(2, 8)}`;

function broadcast(event: DomainEvent): void {
  for (const subscription of listeners) {
    if (subscription.active) subscription.cb(event);
  }
}

function emit(type: string, data: Partial<DomainEvent> = {}): void {
  seq += 1;
  const event: DomainEvent = { schemaVersion: 1, seq, type, ...data };
  eventLog.push(event);
  if (eventLog.length > EVENT_LOG_LIMIT) eventLog.shift();
  broadcast(event);
}

/** 可丢弃实时事件（task.progress / task.execution_log）：seq=0，不进入持久回放。 */
function emitTransient(type: string, data: Partial<DomainEvent> = {}): void {
  broadcast({ schemaVersion: 1, seq: 0, type, ...data });
}

function notifyConnected(subscription: MockSubscription): void {
  if (!subscription.active) return;
  try {
    subscription.onConnected?.();
  } catch {
    // 消费者回调异常不得改变传输生命周期。
  }
}

/** mock 执行过程日志：按 task 保留当前 attempt 的 cache 尾部。 */
interface MockExecutionLog {
  attempt: number;
  lines: ExecutionLogLine[];
}
const executionLogs = new Map<string, MockExecutionLog>();

function appendExecutionLog(
  taskId: string,
  attempt: number,
  stream: ExecutionLogStream,
  line: string,
): void {
  const current = executionLogs.get(taskId);
  const entry = current && current.attempt === attempt
    ? current
    : { attempt, lines: [] };
  const next: ExecutionLogLine = {
    seq: (entry.lines.at(-1)?.seq ?? 0) + 1,
    stream,
    line,
    at: now(),
  };
  entry.lines.push(next);
  executionLogs.set(taskId, entry);
  const control = mockControl();
  if (control) {
    control.lastExecutionLogTaskId = taskId;
    control.lastExecutionLogAttempt = attempt;
  }
  emitTransient("task.execution_log", {
    taskId,
    data: {
      task_id: taskId,
      attempt,
      stream,
      line,
      seq: next.seq,
    },
  });
}

function seg(index: number, startMs: number, endMs: number, text: string): Segment {
  return {
    id: `seg-${index}`,
    startMs,
    endMs,
    text,
    contentRevision: 1,
    contentDigest: `${index}:${text.length}`,
  };
}

// ---- 样例字幕（转录 + 译文） ----
const zhSegments: Segment[] = [
  seg(1, 1000, 3500, "欢迎来到YaomuStudio。"),
  seg(2, 3500, 7200, "这是一段本地译制的演示。"),
  seg(3, 7200, 11800, "我们先把人声和背景音分离。"),
  seg(4, 11800, 15600, "再通过语音识别提取字幕。"),
  seg(5, 15600, 20500, "然后翻译成目标语言。"),
  seg(6, 20500, 24800, "用克隆音色生成配音。"),
  seg(7, 24800, 29200, "最后合成并烧录字幕。"),
  seg(8, 29200, 34000, "全程离线，数据留在本机。"),
];

const enSegments: Segment[] = [
  seg(101, 1000, 3500, "Welcome to YaoMu."),
  seg(102, 3500, 7200, "This is a local dubbing demo."),
  seg(103, 7200, 11800, "First we separate vocals from background."),
  seg(104, 11800, 15600, "Then transcribe via speech recognition."),
  seg(105, 15600, 20500, "Next translate into the target language."),
  seg(106, 20500, 24800, "Generate dubbing with a cloned voice."),
  seg(107, 24800, 29200, "Finally merge and burn subtitles."),
  seg(108, 29200, 34000, "Fully offline, data stays on this machine."),
];

function track(
  projectId: ID,
  language: string,
  type: SubtitleTrack["type"],
  segments: Segment[],
  ids: {
    trackId?: ID;
    revisionId?: ID;
    inputRevisionId?: ID;
    style?: SubtitleStyle;
    eraseBlocks?: EraseBlock[];
  } = {},
): SubtitleTrack {
  const trackId = ids.trackId ?? id("track");
  const rev: SubtitleRevision = {
    id: ids.revisionId ?? id("rev"),
    trackId,
    authorType: "system",
    segments,
    inputRevisionId: ids.inputRevisionId,
    style: ids.style,
    eraseBlocks: ids.eraseBlocks,
    createdAt: now(),
  };
  return {
    id: trackId,
    projectId,
    language,
    type,
    currentRevisionId: rev.id,
    revisions: [rev],
  };
}

// demo 轨使用固定 ID：审核 fixture 需要稳定的 revision ID（真实 Core 的 ID 由服务端生成）。
const demoTranscriptionTrack = track("proj-demo", "zh", "transcription", zhSegments, {
  trackId: "track-demo-zh",
  revisionId: "rev-demo-zh-1",
});
const demoTranslationTrack = track("proj-demo", "en", "translation", enSegments, {
  trackId: "track-demo-en",
  revisionId: "rev-demo-en-1",
  inputRevisionId: "rev-demo-zh-1",
  // 演示 revision 自带样式/擦除块，供保存往返测试验证不会因编辑字幕而丢失。
  style: { x: 0.5, y: 0.85, fontSize: 44, align: "center" },
  eraseBlocks: [{
    id: "eblock-demo-1",
    startMs: 0,
    endMs: 900,
    bbox: [10, 20, 110, 120],
    source: "clean",
  }],
});

/** 演示用的无 Run 项目轨道：卡片详情按项目类型展示语言/选项回退。 */
const cloneTranscriptionTrack = track("proj-clone", "zh", "transcription", zhSegments);
const cloneTranslationTrack = track("proj-clone", "en", "translation", enSegments);
const extractTranscriptionTrack = track("proj-trans", "zh", "transcription", zhSegments);

/**
 * mock 审核摘要（Core 使用 domain.RevisionBindingDigest）。
 * 浏览器演示只需要稳定、可区分的字符串；投影口径与 Core 相同：
 * raw revision / DeliveryBind(revision) / 08 成功 Task。
 */
function translationRevisionDigest(revisionId: ID): string {
  return revisionId;
}
function deliveryRevisionDigest(revisionId: ID): string {
  return `delivery:${revisionId}`;
}
function deliveryMixDigest(runId: ID, language: string): string {
  return `mix:${runId}:${language}`;
}
function deliveryMixSourceId(runId: ID, language: string): ID {
  return `task-mix:${runId}:${language}`;
}

const capabilities: Capability[] = [
  {
    slot: "asr",
    nameKey: "slot.asr",
    state: "READY",
    engineName: "flash-asr",
    version: "1.2.0",
    languages: ["zh", "en", "ja", "ko"],
    runtimeState: "running",
    runningTasks: 1,
    updateAvailable: false,
    engines: [
      { id: "flash-asr", name: "flash-asr", version: "1.2.0", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 1.8 * 1024 ** 3 },
      { id: "faster-whisper", name: "Faster-Whisper large-v3", version: "1.0.0", state: "READY", installed: true, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 3.2 * 1024 ** 3 },
    ],
  },
  {
    slot: "translate",
    nameKey: "slot.translate",
    state: "READY",
    engineName: "flash-mt",
    version: "2.0.1",
    languages: ["zh", "en", "ja", "fr", "de", "es"],
    updateAvailable: true,
    updateVersion: "2.1.0",
    engines: [
      { id: "flash-mt", name: "flash-mt", version: "2.0.1", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 2.4 * 1024 ** 3 },
      { id: "nllb-200", name: "NLLB-200 distilled", version: "1.0.0", state: "READY", installed: true, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 2.5 * 1024 ** 3 },
    ],
  },
  {
    slot: "tts",
    nameKey: "slot.tts",
    state: "READY",
    engineName: "flash-tts",
    version: "1.0.4",
    updateAvailable: false,
    engines: [
      { id: "flash-tts", name: "flash-tts", version: "1.0.4", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 1.2 * 1024 ** 3 },
      { id: "kokoro", name: "Kokoro", version: "0.9.0", state: "READY", installed: true, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 0.8 * 1024 ** 3 },
    ],
  },
  {
    slot: "voice-clone",
    nameKey: "slot.voiceClone",
    state: "READY",
    engineName: "flash-clone",
    version: "0.9.1",
    updateAvailable: false,
    engines: [
      { id: "flash-clone", name: "flash-clone", version: "0.9.1", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 2.1 * 1024 ** 3 },
      { id: "cosyvoice", name: "CosyVoice", version: "0.8.0", state: "READY", installed: true, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 2.8 * 1024 ** 3 },
    ],
  },
  {
    slot: "subtitle-erase",
    nameKey: "slot.subtitleErase",
    state: "READY",
    engineName: "flash-erase",
    version: "1.0.0",
    updateAvailable: false,
    engines: [
      { id: "flash-erase", name: "flash-erase", version: "1.0.0", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 1.6 * 1024 ** 3 },
      { id: "lama", name: "LaMa inpainting", version: "1.0.0", state: "NEEDS_SETUP", installed: false, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 1.1 * 1024 ** 3 },
    ],
  },
  {
    slot: "separation",
    nameKey: "slot.separation",
    state: "READY",
    engineName: "flash-sep",
    version: "1.1.0",
    updateAvailable: false,
    engines: [
      { id: "flash-sep", name: "flash-sep", version: "1.1.0", state: "READY", installed: true, selected: true, device: "Apple MPS", source: "Local bundle", sizeBytes: 1.4 * 1024 ** 3 },
      { id: "demucs", name: "Demucs v4", version: "4.0.0", state: "READY", installed: true, selected: false, device: "CPU", source: "Hugging Face", sizeBytes: 1.9 * 1024 ** 3 },
    ],
  },
  {
    slot: "ocr",
    nameKey: "slot.ocr",
    state: "READY",
    engineName: "flash-ocr",
    version: "1.0.0",
    updateAvailable: false,
  },
  { slot: "ffmpeg", nameKey: "slot.ffmpeg", state: "READY", version: "7.0.2", updateAvailable: false },
];

/** 浏览器 mock 按 UA/platform 推断宿主 OS，避免 Windows 开发机显示成 macOS。 */
function detectMockPlatform(): EnvironmentInfo["platform"] {
  if (typeof navigator === "undefined") return "linux";
  const ua = navigator.userAgent.toLowerCase();
  const plat = (navigator.platform || "").toLowerCase();
  if (ua.includes("windows") || plat.startsWith("win")) return "windows";
  if (ua.includes("mac") || plat.startsWith("mac")) return "darwin";
  return "linux";
}

function buildMockDevices(platform: EnvironmentInfo["platform"]): DeviceInfo[] {
  if (platform === "darwin") {
    return [
      {
        id: "cpu",
        name: "Apple Silicon (CPU)",
        kind: "cpu",
        recommended: false,
        selected: false,
        memoryTotalBytes: 8 * 1024 ** 3,
        memoryUsedBytes: 2.4 * 1024 ** 3,
        inUse: false,
      },
      {
        id: "mps",
        name: "Apple MPS",
        kind: "mps",
        recommended: true,
        selected: true,
        memoryTotalBytes: 8 * 1024 ** 3,
        memoryUsedBytes: 2.4 * 1024 ** 3,
        inUse: true,
      },
    ];
  }
  if (platform === "windows") {
    return [
      {
        id: "cpu",
        name: "CPU",
        kind: "cpu",
        recommended: false,
        selected: false,
        memoryTotalBytes: 16 * 1024 ** 3,
        memoryUsedBytes: 6.4 * 1024 ** 3,
        inUse: false,
      },
      {
        id: "cuda0",
        name: "NVIDIA GPU (mock)",
        kind: "cuda",
        recommended: true,
        selected: true,
        memoryTotalBytes: 8 * 1024 ** 3,
        memoryUsedBytes: 1.5 * 1024 ** 3,
        driverVersion: "560.00",
        inUse: true,
      },
    ];
  }
  return [
    {
      id: "cpu",
      name: "CPU",
      kind: "cpu",
      recommended: true,
      selected: true,
      memoryTotalBytes: 16 * 1024 ** 3,
      memoryUsedBytes: 5.2 * 1024 ** 3,
      inUse: false,
    },
  ];
}

function buildMockEnvironment(platform: EnvironmentInfo["platform"]): EnvironmentInfo {
  const devices = buildMockDevices(platform);
  const checks: EnvironmentInfo["checks"] = [
    {
      code: "FFMPEG_AVAILABLE",
      severity: "pass",
      messageKey: "environment.ffmpeg_ready",
      actionIds: [],
    },
  ];
  if (platform === "windows") {
    checks.push(
      {
        code: "WSL_RUNTIME_READY",
        severity: "pass",
        messageKey: "environment.wsl_runtime_ready",
        actionIds: [],
      },
      {
        code: "WSL2_AVAILABLE",
        severity: "pass",
        messageKey: "environment.wsl_ready",
        actionIds: [],
      },
      {
        code: "WSL_CUDA_AVAILABLE",
        severity: "pass",
        messageKey: "environment.wsl_cuda_ready",
        actionIds: [],
      },
      {
        code: "CUDA_AVAILABLE",
        severity: "pass",
        messageKey: "environment.cuda_ready",
        actionIds: [],
      },
    );
    return {
      platform,
      wsl: { available: true, version: 2, cudaAvailable: true },
      devices,
      checks,
    };
  }
  if (platform === "darwin") {
    return { platform, devices, checks };
  }
  return {
    platform,
    devices,
    checks: [
      ...checks,
      {
        code: "CUDA_AVAILABLE",
        severity: "warning",
        messageKey: "environment.cuda_unavailable",
        actionIds: [],
      },
    ],
  };
}

const environmentInfo: EnvironmentInfo = buildMockEnvironment(detectMockPlatform());
const devices: DeviceInfo[] = environmentInfo.devices;

const storageUsage: StorageUsage = {
  models: 12.4 * 1024 ** 3,
  media: 3.1 * 1024 ** 3,
  cache: 1.2 * 1024 ** 3,
  exports: 0.48 * 1024 ** 3,
};

const systemInfo: SystemInfo = {
  appId: "yaomustudio",
  appVersion: "0.1.0",
  protocolVersion: "0.2.0",
  instanceId: "instance-mock",
  workspaceId: "ws-1",
  serverEpoch: 1,
  coreReady: true,
};

const localApiStats: LocalApiStats = {
  enabled: false,
  totalCalls: 0,
  byEndpoint: [],
};

const localApiSettings: LocalApiSettings = {
  enabled: false,
  concurrencyLimit: 2,
  tokenPresent: false,
};

let localApiToken: string | null = null;

const plans = new Map<ID, InstallationPlan>();

const installations: Installation[] = [];

// ---------------------------------------------------------------------------
// 共享发布安装（仅 DEV/E2E mock）
//
// 只模拟控制面事实：一份物理安装被多个槽位引用、同一 ID 共享进度、完成时
// 同一事务激活全部覆盖槽、删除只解绑请求槽。真实字节/并发/事务由 Core 测试
// 与受控 loopback Downloader 证明，这里不冒充真实下载。
// ---------------------------------------------------------------------------

/** TTS 与克隆共用一份发布声明（真实覆盖由 Core catalog 冻结，不在 UI 猜）。 */
const SHARED_RELEASE_GROUP: CapabilitySlotKey[] = ["tts", "voice-clone"];
/** 基线红用例的 1952 字节物理发布；计划体积只计一份。 */
const MOCK_PACKAGE_BYTES = 1952;

const installationReleaseById = new Map<ID, ReleaseRef>();
const installationVerifiedById = new Map<ID, number>();
let installationFixturesApplied = false;

function defaultCoverageForSlot(slot: CapabilitySlotKey): CapabilitySlotKey[] {
  if (!SHARED_RELEASE_GROUP.includes(slot)) return [slot];
  return CAPABILITY_SLOTS.map((meta) => meta.key).filter((key) =>
    SHARED_RELEASE_GROUP.includes(key),
  );
}

function defaultReleaseForSlot(slot: CapabilitySlotKey): ReleaseRef {
  return SHARED_RELEASE_GROUP.includes(slot)
    ? {
        releaseId: "release-tts-voice",
        version: "0.9.1-mock",
        digest: "sha256:mock-tts-voice",
        channel: "stable",
      }
    : {
        releaseId: `release-${slot}`,
        version: "0.0.0-mock",
        digest: `sha256:mock-${slot}`,
        channel: "stable",
      };
}

function releaseKey(release: ReleaseRef): string {
  return [release.releaseId, release.version, release.digest, release.channel ?? ""].join("|");
}

function uniqueSlots(slots: CapabilitySlotKey[]): CapabilitySlotKey[] {
  return [...new Set(slots)];
}

function sameSlots(left: CapabilitySlotKey[], right: CapabilitySlotKey[]): boolean {
  return left.length === right.length && left.every((slot) => right.includes(slot));
}

function installationError(code: string, messageKey: string): ApiError {
  return new ApiError({ code, messageKey, retryable: false, actionIds: [] });
}

function setMockCapabilityState(slot: CapabilitySlotKey, state: SlotState): void {
  const cap = capFor(slot);
  if (cap) cap.state = state;
  const control = mockControl();
  if (control) {
    // capabilityStates 覆盖优先于可变目录；同步写回，避免安装完成后仍读到旧状态。
    control.capabilityStates = { ...(control.capabilityStates ?? {}), [slot]: state };
  }
}

/** 安装生命周期变化后，按 Core 投影口径重算受影响槽位的显示状态。 */
function syncCapabilitySlots(slots: CapabilitySlotKey[]): void {
  for (const slot of uniqueSlots(slots)) {
    if (
      installations.some(
        (inst) =>
          inst.phase === "done" && installationCurrentSlots(inst).includes(slot),
      )
    ) {
      setMockCapabilityState(slot, "READY");
      continue;
    }
    const active = installations.find(
      (inst) => isInstallationActive(inst) && installationBoundSlots(inst).includes(slot),
    );
    if (active) {
      setMockCapabilityState(slot, active.phase === "verifying" ? "VERIFYING" : "INSTALLING");
      continue;
    }
    setMockCapabilityState(slot, "NEEDS_SETUP");
  }
}

function applyInstallationFixtures(): void {
  const fixtures = mockControl()?.installationFixtures;
  if (!fixtures || installationFixturesApplied) return;
  installationFixturesApplied = true;
  installations.splice(0, installations.length, ...structuredClone(fixtures));
  for (const inst of installations) {
    if (!installationReleaseById.has(inst.id)) {
      installationReleaseById.set(inst.id, defaultReleaseForSlot(inst.slot));
    }
    if (inst.verifiedFiles !== undefined) {
      installationVerifiedById.set(inst.id, inst.verifiedFiles);
    }
  }
}

function plannedCoverage(slot: CapabilitySlotKey): CapabilitySlotKey[] | undefined {
  const override = mockControl()?.planCoverage?.[slot];
  if (override === undefined) return defaultCoverageForSlot(slot);
  // null = 字段缺失的旧 Core 形状；显式数组（含非法/空）原样返回供 UI 拒绝。
  return override === null ? undefined : [...override];
}

function findReusableInstallation(release: ReleaseRef): Installation | undefined {
  return installations.find((inst) => {
    if (!isInstallationActive(inst) && inst.phase !== "done") return false;
    const known = installationReleaseById.get(inst.id);
    return known !== undefined && releaseKey(known) === releaseKey(release);
  });
}

function activateInstallation(inst: Installation, slots: CapabilitySlotKey[]): void {
  inst.boundSlots = uniqueSlots([...(inst.boundSlots ?? [inst.slot]), ...slots]);
  inst.currentSlots = uniqueSlots([...(inst.currentSlots ?? []), ...slots]);
  syncCapabilitySlots(slots);
}

function completeInstallation(installationId: ID): void {
  const inst = installations.find((item) => item.id === installationId);
  if (!inst || inst.phase === "done") return;
  const covered = installationCoveredSlots(inst);
  const verified = Math.max(installationVerifiedById.get(inst.id) ?? 0, 1);
  inst.phase = "done";
  inst.progress = 100;
  inst.verifiedFiles = verified;
  installationVerifiedById.set(inst.id, verified);
  // 只有全部文件验证/落位完成后才在同一状态更新里激活全部覆盖槽。
  activateInstallation(inst, covered);
  emit("installation.completed", {
    data: { installation_id: inst.id, slot: inst.slot },
  });
}

function failInstallation(installationId: ID): void {
  const inst = installations.find((item) => item.id === installationId);
  if (!inst || inst.phase === "done") return;
  inst.phase = "failed";
  inst.retryable = true;
  inst.error = {
    code: "DOWNLOAD_FAILED",
    messageKey: "installation.download_failed",
    retryable: true,
    actionIds: [],
  };
  syncCapabilitySlots(installationCoveredSlots(inst));
  emit("installation.failed", {
    data: { installation_id: inst.id, slot: inst.slot },
  });
}

function setInstallationProgress(installationId: ID, progress: number): void {
  const inst = installations.find((item) => item.id === installationId);
  if (!inst || inst.phase === "done") return;
  inst.progress = Math.max(0, Math.min(100, Math.round(progress)));
  emit("installation.progress", {
    data: {
      installation_id: inst.id,
      phase: inst.phase,
      progress: inst.progress,
    },
  });
}

function makeRun(projectId: ID, name: string): WorkflowRun {
  return {
    id: id("run"),
    projectId,
    name,
    template: "full_pipeline",
    modeType: "full_pipeline",
    status: "WAITING_REVIEW",
    sourceLanguage: "zh",
    languages: ["en"],
    inputAssetIds: [],
    nodes: [
      { key: "01_import", status: "done", progress: 100 },
      { key: "02_analyze", status: "done", progress: 100 },
      { key: "03_separate", status: "done", progress: 100 },
      { key: "04_extract", status: "done", progress: 100 },
      { key: "05_erase", status: "done", progress: 100 },
      { key: "06_translate", status: "waiting_review", progress: 100 },
      { key: "07_clone", status: "pending", progress: 0 },
      { key: "08_mix", status: "pending", progress: 0 },
    ],
    params: {
      eraseMode: "dialog_only",
      validateTimestamps: true,
      adjustDuration: false,
      bilingual: false,
      extractMode: "asr",
      asrLanguage: "zh",
      // 运行级样式回退：revision 未设置 style 时，人工校正页展示该值。
      subtitleStyle: { x: 0.5, y: 0.85, fontSize: 40, align: "center" },
    },
    createdAt: now(),
    updatedAt: now(),
  };
}

/** 与 Core 一致：创建时把受理的配置冻结为 Run.params，供只读回显。 */
function frozenRunParams(input: CreateWorkflowRunInput): RunParams | undefined {
  const params: RunParams = {};
  if (input.eraseMode !== undefined) params.eraseMode = input.eraseMode;
  if (input.validateTimestamps !== undefined) params.validateTimestamps = input.validateTimestamps;
  if (input.adjustDuration !== undefined) params.adjustDuration = input.adjustDuration;
  if (input.bilingual !== undefined) params.bilingual = input.bilingual;
  if (input.extract?.mode !== undefined) params.extractMode = input.extract.mode;
  if (input.extract?.ocrLanguage !== undefined) params.ocrLanguage = input.extract.ocrLanguage;
  if (input.extract?.ocrQuality !== undefined) params.ocrQuality = input.extract.ocrQuality;
  if (input.subtitleStyle !== undefined) {
    params.subtitleStyle = {
      ...input.subtitleStyle,
      outline: input.subtitleStyle.outline ? { ...input.subtitleStyle.outline } : undefined,
      shadow: input.subtitleStyle.shadow ? { ...input.subtitleStyle.shadow } : undefined,
    };
  }
  return Object.keys(params).length > 0 ? params : undefined;
}

const candidateStore = new Map<string, AudioCandidate[]>();
const candidateKey = (segmentId: ID, language: string): string => `${segmentId}:${language}`;
const selectionStore = new Map<string, CandidateSelection>();
const engineConfigs = new Map<ModelSlotKey, EngineConfig>();

function candidatesFor(segmentId: ID, language: string): AudioCandidate[] {
  return candidateStore.get(candidateKey(segmentId, language)) ?? [];
}

function makeCandidates(segmentId: ID, language: string, revision: number): AudioCandidate[] {
  const base = { segmentId, language, revision, modelVersion: "flash-clone 0.9.1" };
  return [
    { id: id("cand"), ...base, audioAssetId: id("asset"), isRecommended: true, createdAt: now() },
    { id: id("cand"), ...base, audioAssetId: id("asset"), isRecommended: false, createdAt: now() },
    { id: id("cand"), ...base, audioAssetId: id("asset"), isRecommended: false, createdAt: now() },
  ];
}

const demoRun = makeRun("proj-demo", "星际译制 · 试播集");
// 固定 Run ID 供审核 fixture（e2e 显式构造 currentBindings）引用。
demoRun.id = "run-demo-1";

const customRunInputs = new Map<ID, CreateWorkflowRunInput>();
let customTemplates: CustomPipelineTemplate[] = [];

// 默认 demo 门槛不存 currentBindings：列表读取时按当前源投影（与 Core 同口径）。
// 同时提供 mix 与字幕 revision 两个交付门槛，覆盖视频与字幕导出选择。
const reviews: ReviewGate[] = [
  {
    id: "rev-trans-en",
    runId: demoRun.id,
    type: "translation",
    language: "en",
    revisionDigest: translationRevisionDigest("rev-demo-en-1"),
    status: "pending",
  },
  {
    id: "rev-delivery-mix-en",
    runId: demoRun.id,
    type: "delivery",
    language: "en",
    revisionDigest: deliveryMixDigest(demoRun.id, "en"),
    status: "pending",
  },
  {
    id: "rev-delivery-sub-en",
    runId: demoRun.id,
    type: "delivery",
    language: "en",
    revisionDigest: deliveryRevisionDigest("rev-demo-en-1"),
    status: "pending",
  },
];

const tasks: Task[] = [  { id: "task-1", projectId: "proj-demo", runId: demoRun.id, kind: "node", nodeKey: "05_erase", label: "字幕擦除", status: "SUCCEEDED", progress: 100, artifactIds: [], createdAt: now(), updatedAt: now() },
  { id: "task-2", projectId: "proj-demo", runId: demoRun.id, kind: "segment", segmentId: "seg-6", label: "英语第 6 句配音", status: "RUNNING", progress: 62, steps: [{ stepId: "segment", status: "RUNNING", attemptNo: 1, epoch: 1 }], artifactIds: [], createdAt: now(), updatedAt: now() },
  { id: "task-3", projectId: "proj-demo", runId: demoRun.id, kind: "segment", segmentId: "seg-3", label: "英语第 3 句配音", status: "FAILED", progress: 40, artifactIds: [], error: { code: "RESOURCE_EXHAUSTED", messageKey: "task.oom", retryable: true, actionIds: ["retry"] }, createdAt: now(), updatedAt: now() },
  { id: "task-4", projectId: "proj-demo", runId: demoRun.id, kind: "segment", segmentId: "seg-7", label: "英语第 7 句配音", status: "QUEUED", progress: 0, artifactIds: [], createdAt: now(), updatedAt: now() },
  { id: "task-5", projectId: "proj-demo", runId: demoRun.id, kind: "node", nodeKey: "08_mix", label: "混合成片", status: "WAITING_RESOURCE", progress: 0, artifactIds: [], createdAt: now(), updatedAt: now() },
];

const exportRecords: ExportRecord[] = [];

const demoAsset: Asset = {
  id: "asset-video-1",
  projectId: "proj-demo",
  type: "video",
  role: "original",
  name: "episode01.mp4",
  path: "/Users/demo/media/episode01.mp4",
  sizeBytes: 812 * 1024 ** 2,
  sha256: "sha256:demo",
  meta: { container: "mp4", codec: "h264", width: 1920, height: 1080, durationMs: 34_000, audioTracks: 2, sampleRate: 48000 },
  status: "ready",
  importedAt: now(),
};

const demoAudioAsset: Asset = {
  id: "asset-vocal-1",
  projectId: "proj-demo",
  type: "audio_track",
  role: "voice",
  language: "zh",
  runId: demoRun.id,
  name: "vocal_clean.wav",
  path: "/Users/demo/media/vocal_clean.wav",
  sizeBytes: 12 * 1024 ** 2,
  meta: { container: "wav", codec: "pcm", durationMs: 34_000, audioTracks: 1, sampleRate: 48000 },
  status: "ready",
  importedAt: now(),
};

const demoCloneAudioAsset: Asset = {
  id: "asset-clone-source",
  projectId: "proj-clone",
  type: "audio",
  name: "product-voice.wav",
  path: "/Users/demo/media/product-voice.wav",
  sizeBytes: 4 * 1024 ** 2,
  meta: { container: "wav", codec: "pcm", durationMs: 28_000, audioTracks: 1, sampleRate: 48000 },
  status: "ready",
  importedAt: now(),
};

const demoCloneOriginalSubtitle: Asset = {
  id: "asset-clone-original-srt",
  projectId: "proj-clone",
  type: "subtitle",
  name: "product-original.srt",
  path: "/Users/demo/media/product-original.srt",
  sizeBytes: 3 * 1024,
  status: "ready",
  importedAt: now(),
};

const demoCloneTranslatedSubtitle: Asset = {
  id: "asset-clone-en-srt",
  projectId: "proj-clone",
  type: "subtitle",
  name: "product-en.srt",
  path: "/Users/demo/media/product-en.srt",
  sizeBytes: 3 * 1024,
  status: "ready",
  importedAt: now(),
};

const demoExtractAudioAsset: Asset = {
  id: "asset-trans-meeting",
  projectId: "proj-trans",
  type: "audio",
  name: "meeting.wav",
  path: "/Users/demo/media/meeting.wav",
  sizeBytes: 18 * 1024 ** 2,
  meta: { container: "wav", codec: "pcm", durationMs: 65_000, audioTracks: 1, sampleRate: 48000 },
  status: "ready",
  importedAt: now(),
};

const projects: Project[] = [
  {
    id: "proj-demo",
    workspaceId: "ws-1",
    episodeId: "ep_demo",
    name: "星际译制 · 试播集",
    directory: "/Users/demo/yaomu/projects/proj-demo",
    schemaVersion: 1,
    editVersion: 3,
    createdAt: now(),
    updatedAt: now(),
    lastOpenedAt: now(),
    assets: [demoAsset, demoAudioAsset],
    tracks: [demoTranscriptionTrack, demoTranslationTrack],
    runs: [demoRun],
  },
  {
    id: "proj-clone",
    workspaceId: "ws-1",
    episodeId: "ep_clone",
    name: "产品宣传片配音",
    directory: "/Users/demo/yaomu/projects/proj-clone",
    schemaVersion: 1,
    editVersion: 1,
    createdAt: now(),
    updatedAt: now(),
    assets: [demoCloneAudioAsset, demoCloneOriginalSubtitle, demoCloneTranslatedSubtitle],
    tracks: [cloneTranscriptionTrack, cloneTranslationTrack],
    runs: [],
  },
  {
    id: "proj-trans",
    workspaceId: "ws-1",
    episodeId: "ep_trans",
    name: "会议录音转写",
    directory: "/Users/demo/yaomu/projects/proj-trans",
    schemaVersion: 1,
    editVersion: 1,
    createdAt: now(),
    updatedAt: now(),
    assets: [demoExtractAudioAsset],
    tracks: [extractTranscriptionTrack],
    runs: [],
  },
];

/** E2E 可替换项目资产；生产构建 mockControl 恒为 undefined。 */
function projectAssets(p: Project): Asset[] {
  return mockControl()?.assetFixtures?.[p.id] ?? p.assets;
}

/**
 * 夹具数据：真实 Core 的 ProjectSummary.modeType 由最新 Run 派生；无 Run 的示例项目
 * 没有该来源，但仍需按创建类型展示卡片摘要。只补 mock fixture，不扩展契约字段。
 */
const DEMO_PROJECT_MODE_TYPES: Partial<Record<ID, string>> = {
  "proj-clone": "high_sentiment_tts_clone",
  "proj-trans": "subtitle_extract_asr",
};

const DEMO_PROJECT_SEQ: Partial<Record<ID, number>> = {
  "proj-demo": 1,
  "proj-clone": 2,
  "proj-trans": 3,
};

const toSummary = (p: Project): ProjectSummary => ({
  seq: DEMO_PROJECT_SEQ[p.id],
  id: p.id,
  name: mockControl()?.projectNameFixtures?.[p.id] ?? p.name,
  editVersion: p.editVersion,
  episodeId: p.episodeId,
  modeType: p.runs.at(-1)?.modeType ?? DEMO_PROJECT_MODE_TYPES[p.id],
  createdAt: p.createdAt,
  updatedAt: p.updatedAt,
  lastOpenedAt: p.lastOpenedAt,
  assetCount: projectAssets(p).length,
  runCount: p.runs.length,
  latestRunStatus:
    mockControl()?.projectRunStatusFixtures?.[p.id] ?? p.runs.at(-1)?.status,
  totalDurationMs: projectAssets(p).reduce((acc, a) => acc + (a.meta?.durationMs ?? 0), 0),
  waitingResource:
    mockControl()?.projectWaitingResourceFixtures?.[p.id] ??
    (p.runs.at(-1)?.status === "RUNNING" ? false : undefined),
});

function notFound(): ApiError {
  return new ApiError({ code: "NOT_FOUND", messageKey: "api.notFound", retryable: false, actionIds: [] });
}

function invalidRequest(): ApiError {
  return new ApiError({
    code: "INVALID_REQUEST",
    messageKey: "api.invalidRequest",
    retryable: false,
    actionIds: [],
  });
}

function revisionConflict(): ApiError {
  return new ApiError({
    code: "REVISION_CONFLICT",
    messageKey: "api.revisionConflict",
    retryable: false,
    actionIds: [],
  });
}

/** mock 的句级内容摘要：与 Core 同口径只含文本与说话人，供句级版本比较。 */
function mockSegmentDigest(text: string, speaker?: string): string {
  let hash = 5381;
  const value = `${text}\u0000${speaker ?? ""}`;
  for (let i = 0; i < value.length; i += 1) {
    hash = ((hash << 5) + hash + value.charCodeAt(i)) >>> 0;
  }
  return `mock-${hash.toString(16)}`;
}

const TERMINAL_RUN_STATUS = new Set<WorkflowRun["status"]>([
  "SUCCEEDED",
  "PARTIAL",
  "FAILED",
  "CANCELED",
]);

/** 与 Core 的审核失效范围一致：只有非终止且含审核节点的 Run 生成门槛。 */
function reviewableRun(run: WorkflowRun): boolean {
  if (TERMINAL_RUN_STATUS.has(run.status)) return false;
  return run.template === "full_pipeline" || run.template === "custom_pipeline";
}

function addMockGate(
  run: WorkflowRun,
  type: ReviewGate["type"],
  language: string,
  digest: string,
): void {
  const exists = reviews.some(
    (gate) =>
      gate.runId === run.id &&
      gate.type === type &&
      (gate.language ?? "") === language &&
      gate.status === "pending" &&
      gate.revisionDigest === digest,
  );
  if (exists) return;
  reviews.push({
    id: id("review"),
    runId: run.id,
    type,
    language: language || undefined,
    revisionDigest: digest,
    status: "pending",
  });
}

function findProject(projectId: ID): Project {
  const p = projects.find((x) => x.id === projectId);
  if (!p) throw notFound();
  return p;
}

function findTask(taskId: ID): Task {
  const t = tasks.find((x) => x.id === taskId);
  if (!t) throw notFound();
  return t;
}

function findRun(runId: ID): WorkflowRun | undefined {
  for (const project of projects) {
    const run = project.runs.find((item) => item.id === runId);
    if (run) return run;
  }
  return undefined;
}

/** 当前译文 revision；没有 translation 轨或 currentRevision 失效时返回 undefined。 */
function currentTranslationRevision(project: Project, language: string): SubtitleRevision | undefined {
  const track = project.tracks.find(
    (item) =>
      item.type === "translation" && item.language === language && item.currentRevisionId,
  );
  return track?.revisions.find((revision) => revision.id === track.currentRevisionId);
}

/** 与 Core 的 LatestSucceededNodeTask(08, language) 同口径。 */
function latestSucceededMixTask(runId: ID, language: string): Task | undefined {
  return tasks
    .filter(
      (task) =>
        task.runId === runId &&
        task.nodeKey === "08_mix" &&
        task.status === "SUCCEEDED" &&
        (!task.language || task.language === language),
    )
    .sort(
      (a, b) =>
        b.updatedAt.localeCompare(a.updatedAt) ||
        b.createdAt.localeCompare(a.createdAt) ||
        a.id.localeCompare(b.id),
    )[0];
}

/**
 * 只读 current_bindings 投影（mock 无 DB，读取时按当前轨道/任务派生）。
 * 只匹配对应当前源的精确摘要：旧记录得到空数组，不会被 UI 当作当前门槛。
 */
function projectCurrentBindings(gate: ReviewGate): ReviewCurrentBinding[] {
  const run = findRun(gate.runId);
  if (!run) return [];
  const project = findProject(run.projectId);
  const languages =
    mockControl()?.reviewRunLanguages ??
    (run.languages.length > 0 ? run.languages : gate.language ? [gate.language] : []);
  const bindings: ReviewCurrentBinding[] = [];
  for (const language of languages) {
    if (gate.language && gate.language !== language) continue;
    if (gate.type === "translation") {
      const revision = currentTranslationRevision(project, language);
      if (!revision) continue;
      // custom mock 用 run:language 作为合成摘要；保存产生的门槛用 raw revision。
      if (
        gate.revisionDigest === translationRevisionDigest(revision.id) ||
        gate.revisionDigest === `${run.id}:${language}`
      ) {
        bindings.push({
          language,
          sourceKind: "translation_revision",
          sourceId: revision.id,
        });
      }
      continue;
    }
    const revision = currentTranslationRevision(project, language);
    if (revision && gate.revisionDigest === deliveryRevisionDigest(revision.id)) {
      bindings.push({
        language,
        sourceKind: "delivery_revision",
        sourceId: revision.id,
      });
    }
    if (
      gate.revisionDigest === deliveryMixDigest(run.id, language) ||
      gate.revisionDigest === `${run.id}:delivery`
    ) {
      const mixTask = latestSucceededMixTask(run.id, language);
      bindings.push({
        language,
        sourceKind: "delivery_mix",
        sourceId: mixTask?.id ?? deliveryMixSourceId(run.id, language),
      });
    }
  }
  return bindings;
}

/** fixture 模式原样返回（含缺失 currentBindings 的旧服务场景），默认模式按源投影。 */
function readGate(gate: ReviewGate, fixtureMode: boolean): ReviewGate {
  return fixtureMode
    ? structuredClone(gate)
    : { ...structuredClone(gate), currentBindings: projectCurrentBindings(gate) };
}

/** e2e 显式 fixture 只在首次读取时替换默认列表；后续批准保留 fixture 中的绑定。 */
let reviewFixturesApplied = false;
function applyReviewFixtures(): void {
  const fixtures = mockControl()?.reviewFixtures;
  if (!fixtures || reviewFixturesApplied) return;
  reviewFixturesApplied = true;
  reviews.splice(0, reviews.length, ...structuredClone(fixtures));
}

function capFor(slot: CapabilitySlotKey): Capability | undefined {
  return capabilities.find((cap) => cap.slot === slot);
}

function requireCap(slot: ID): Capability {
  const cap = CAPABILITY_SLOTS.find((meta) => meta.key === slot);
  const found = cap ? capFor(cap.key) : undefined;
  if (!cap || !found) throw notFound();
  return found;
}

function clone<T>(value: T): T {
  return structuredClone(value);
}

/** 与 Core 一致：列表/项目快照不嵌入步骤历史；身份由 getTask 详情读取。 */
function taskSummary(value: Task): Task {
  const { steps: _steps, ...rest } = value;
  return structuredClone(rest);
}

function countCall(control: MockControl | undefined, field: keyof MockControl): void {
  if (!control) return;
  const record = control as Record<string, unknown>;
  record[field] = (Number(record[field]) || 0) + 1;
}

// ---- 专属链路 mock ----

function customHas(features: CustomPipelineFeature[] | undefined, key: CustomPipelineFeature): boolean {
  return (features ?? []).includes(key);
}

const CUSTOM_FEATURES: CustomPipelineFeature[] = [
  "separation",
  "erase",
  "translate",
  "clone",
  "mix",
];

/** 与 Core 对齐：custom features 必须非空、唯一且属于固定枚举。 */
function validCustomFeatures(features: CustomPipelineFeature[] | undefined): boolean {
  if (!features || features.length === 0) return false;
  if (new Set(features).size !== features.length) return false;
  return features.every((feature) => CUSTOM_FEATURES.includes(feature));
}

function setNode(run: WorkflowRun, key: WorkflowRun["nodes"][number]["key"], status: string): void {
  const node = run.nodes.find((item) => item.key === key);
  if (!node) return;
  node.status = status as WorkflowRun["nodes"][number]["status"];
  node.progress = status === "pending" ? 0 : 100;
}

/**
 * 仅供 e2e（control.remixOnApprove）：模拟重新合成推进——译文放行后完成 07/08，
 * 08 停在交付审核；交付门槛仍需人工放行，不在这里自动 approved。
 * 真实 Core 的推进、冻结 revision 与交付失效语义由 Go 用例覆盖。
 */
function advanceRemixAfterTranslationApproval(gate: ReviewGate): void {
  const run = findRun(gate.runId);
  if (!run || TERMINAL_RUN_STATUS.has(run.status)) return;
  const language = gate.language ?? run.languages[0] ?? "";
  if (!language) return;
  setNode(run, "06_translate", "done");
  setNode(run, "07_clone", "done");
  setNode(run, "08_mix", "waiting_review");
  run.status = "WAITING_REVIEW";
  run.updatedAt = now();
  addMockGate(run, "delivery", language, deliveryMixDigest(run.id, language));
  emit("review.created", { projectId: run.projectId, taskId: run.id });
}

function mockArtifact(project: Project, type: Asset["type"], name: string): Asset {
  const asset: Asset = {
    id: id("asset"),
    projectId: project.id,
    type,
    name,
    sizeBytes: 1024,
    status: "ready",
    importedAt: now(),
  };
  project.assets.push(asset);
  return asset;
}

function ensureCustomTranscription(project: Project, language: string): SubtitleTrack {
  const existing = project.tracks.find(
    (item) => item.type === "transcription" && item.language === language && item.currentRevisionId,
  );
  if (existing) return existing;
  const created = track(project.id, language, "transcription", zhSegments.slice(0, 2));
  project.tracks.push(created);
  return created;
}

function ensureCustomTranslation(project: Project, language: string, index: number): boolean {
  const existing = project.tracks.find(
    (item) => item.type === "translation" && item.language === language && item.currentRevisionId,
  );
  if (existing) return false;
  project.tracks.push(
    track(project.id, language, "translation", [
      seg(500 + index * 10 + 1, 0, 1600, `Translated ${language} line one.`),
      seg(500 + index * 10 + 2, 1800, 3600, `Translated ${language} line two.`),
    ]),
  );
  return true;
}

function addCustomGate(run: WorkflowRun, type: ReviewGate["type"], language: string, digest: string): ReviewGate {
  const gate: ReviewGate = {
    id: id("review"),
    runId: run.id,
    type,
    language,
    revisionDigest: digest,
    status: "pending",
  };
  reviews.push(gate);
  return gate;
}

function generateCustomCandidates(project: Project, input: CreateWorkflowRunInput): void {
  if (mockControl()?.customNoCandidates) return;
  for (const language of input.targetLanguages) {
    const translated = project.tracks.find(
      (item) => item.type === "translation" && item.language === language,
    );
    const revision = translated?.revisions.find((item) => item.id === translated.currentRevisionId);
    for (const segment of revision?.segments ?? []) {
      const existing = candidatesFor(segment.id, language);
      // 审核放行前后都会调用本函数；已有候选时不再追加，避免 07 候选重复。
      if (existing.length > 0) continue;
      candidateStore.set(candidateKey(segment.id, language), [
        ...existing,
        ...makeCandidates(segment.id, language, segment.contentRevision),
      ]);
    }
  }
}

function finishCustomRun(project: Project, run: WorkflowRun, input: CreateWorkflowRunInput): void {
  const features = input.features ?? [];
  const addNodeTask = (nodeKey: string, artifactIds: ID[]): void => {
    tasks.push({
      id: id("task"),
      projectId: project.id,
      runId: run.id,
      kind: "node",
      nodeKey: nodeKey as Task["nodeKey"],
      label: nodeKey,
      status: "SUCCEEDED",
      progress: 100,
      artifactIds,
      createdAt: now(),
      updatedAt: now(),
    });
  };
  if (customHas(features, "separation")) {
    const voice = mockArtifact(project, "audio_track", "voice.wav");
    const ambience = mockArtifact(project, "audio_track", "ambience.wav");
    addNodeTask("03_separate", [voice.id, ambience.id]);
  }
  if (customHas(features, "erase")) {
    const clean = mockArtifact(project, "video", "clean.mp4");
    addNodeTask("05_erase", [clean.id]);
  }
  if (customHas(features, "clone")) {
    generateCustomCandidates(project, input);
  }
  if (customHas(features, "mix")) {
    for (const language of input.targetLanguages) {
      const output = mockArtifact(project, "video", `${language}-mix.mp4`);
      addNodeTask("08_mix", [output.id]);
    }
  }
  for (const node of run.nodes) {
    if (node.status === "waiting_review" || node.status === "running" || node.status === "pending") {
      node.status = node.key === "04_extract" && input.reuseSubtitleTrackId && !customHas(features, "erase")
        ? "reused"
        : "done";
      node.progress = 100;
    }
  }
  run.status = "SUCCEEDED";
  run.updatedAt = now();
  emit("run.succeeded", { projectId: project.id, taskId: run.id });
}

function advanceCustomAfterTranslation(project: Project, run: WorkflowRun, input: CreateWorkflowRunInput): void {
  setNode(run, "06_translate", "done");
  if (customHas(input.features, "clone")) {
    generateCustomCandidates(project, input);
  }
  if (customHas(input.features, "mix") && !mockControl()?.customSkipReview) {
    addCustomGate(run, "delivery", input.targetLanguages[0] ?? "", `${run.id}:delivery`);
    setNode(run, "08_mix", "waiting_review");
    run.status = "WAITING_REVIEW";
    run.updatedAt = now();
    emit("review.created", { projectId: project.id, taskId: run.id });
    return;
  }
  finishCustomRun(project, run, input);
}

function runCustomPipeline(project: Project, run: WorkflowRun, input: CreateWorkflowRunInput): void {
  const features = input.features ?? [];
  const needsText =
    customHas(features, "translate") || customHas(features, "clone") || customHas(features, "mix");
  const erase = customHas(features, "erase");
  const skipReview = mockControl()?.customSkipReview === true;

  if (needsText || erase) {
    const source = input.reuseSubtitleTrackId
      ? project.tracks.find((item) => item.id === input.reuseSubtitleTrackId)
      : undefined;
    if (!source) {
      ensureCustomTranscription(project, input.sourceLanguage ?? "und");
    }
    if (input.reuseSubtitleTrackId && !erase) {
      setNode(run, "04_extract", "reused");
    }
  }

  const createdTranslations: string[] = [];
  if (needsText) {
    input.targetLanguages.forEach((language, index) => {
      if (ensureCustomTranslation(project, language, index)) {
        createdTranslations.push(language);
      }
    });
  }

  if (needsText && createdTranslations.length === 0 && !erase && !input.reuseSubtitleTrackId) {
    // 全部目标译文都已导入时 Core 会跳过 04；mock 保持同一终态语义。
    setNode(run, "04_extract", "skipped");
  }

  if (!skipReview && createdTranslations.length > 0) {
    for (const language of createdTranslations) {
      addCustomGate(run, "translation", language, `${run.id}:${language}`);
    }
    setNode(run, "06_translate", "waiting_review");
    run.status = "WAITING_REVIEW";
    run.updatedAt = now();
    emit("review.created", { projectId: project.id, taskId: run.id });
    return;
  }

  setNode(run, "06_translate", "done");
  if (customHas(features, "clone")) {
    generateCustomCandidates(project, input);
  }
  if (customHas(features, "mix") && !skipReview) {
    addCustomGate(run, "delivery", input.targetLanguages[0] ?? "", `${run.id}:delivery`);
    setNode(run, "08_mix", "waiting_review");
    run.status = "WAITING_REVIEW";
    run.updatedAt = now();
    emit("review.created", { projectId: project.id, taskId: run.id });
    return;
  }
  finishCustomRun(project, run, input);
}

/**
 * 仅开发演示源：把可释放测试控制挂到 window.__yaomuMock。
 * e2e 通过 addInitScript 在页面脚本前创建该对象；生产构建别名到 disabled 模块，
 * 本文件与其控制钩子不会进入产物。
 */
function installMockControlHooks(control: MockControl): void {
  control.emitDomainEvent = (type, payload) => emit(type, payload);
  control.emitRawEvent = (event) => {
    for (const subscription of [...listeners]) {
      if (subscription.active) subscription.cb(event as DomainEvent);
    }
  };
  control.emitTaskProgress = (input) => {
    const task = tasks.find((item) => item.id === input.taskId);
    emitTransient("task.progress", {
      projectId: input.projectId ?? task?.projectId ?? "",
      taskId: input.taskId,
      stateVersion: input.stateVersion,
      data: {
        step_id: input.stepId,
        attempt: input.attempt,
        server_epoch: String(input.epoch),
        progress: input.progress,
      },
    });
  };
  control.setTask = (taskId, patch) => {
    const task = tasks.find((item) => item.id === taskId);
    if (task) Object.assign(task, patch as Task);
  };
  control.addTask = (task) => {
    if (!tasks.some((item) => item.id === task.id)) tasks.push(structuredClone(task));
  };
  control.emitEventStreamConnected = () => {
    countCall(control, "connectedNotificationCount");
    for (const subscription of [...listeners]) notifyConnected(subscription);
  };
  control.emitStaleSubscriptionEvent = (event) => {
    const subscription = staleSubscriptions.at(-1);
    if (subscription) subscription.cb(event as DomainEvent);
  };
  control.emitStaleSubscriptionConnected = () => {
    const subscription = staleSubscriptions.at(-1);
    if (!subscription) return;
    // 明确调用已退订订阅保存的回调（不能走 notifyConnected，它会因 inactive 提前返回）。
    countCall(control, "staleConnectedCalls");
    try {
      subscription.onConnected?.();
    } catch {
      // 与真实旧流迟到回调一致：消费者异常不影响测试控制。
    }
  };
  control.appendExecutionLog = (taskId, attempt, stream, line) =>
    appendExecutionLog(taskId, attempt, stream, line);
  control.completeInstallation = (installationId) => completeInstallation(installationId);
  control.failInstallation = (installationId) => failInstallation(installationId);
  control.setInstallationProgress = (installationId, progress) =>
    setInstallationProgress(installationId, progress);
  control.readInstallations = () => structuredClone(installations);
}

const initialMockControl = mockControl();
if (initialMockControl) installMockControlHooks(initialMockControl);

export const fixtureApi: YaoMuApi = {
  async getSystem() {
    return { ...systemInfo };
  },
  async getCapabilities() {
    const control = mockControl();
    const states = control?.capabilityStates;
    const fixtures = control?.capabilityFixtures;
    return capabilities.map((c) => ({
      ...c,
      ...fixtures?.[c.slot],
      state: states?.[c.slot] ?? c.state,
    }));
  },
  async getDevices() {
    return devices.map((d) => ({ ...d }));
  },
  async getEnvironment(_options?: { refresh?: boolean }) {
    const control = mockControl();
    countCall(control, "environmentCalls");
    if (control?.environmentFailure) {
      // 未知错误：UI 只能显示已注册回退，不能暴露此正文。
      throw new Error("mock environment secret");
    }
    const source = control?.environment ?? environmentInfo;
    return {
      ...source,
      devices: source.devices.map((d) => ({ ...d })),
      checks: source.checks.map((c) => ({ ...c, actionIds: [...c.actionIds] })),
    };
  },
  async getStorageUsage() {
    return { ...storageUsage };
  },
  async getLocalApiStats() {
    return { ...localApiStats, byEndpoint: localApiStats.byEndpoint.map((e) => ({ ...e })) };
  },
  async getLocalApiSettings() {
    return { ...localApiSettings };
  },
  async updateLocalApiSettings(input) {
    if (input.enabled !== undefined) localApiSettings.enabled = input.enabled;
    if (input.concurrencyLimit !== undefined) {
      localApiSettings.concurrencyLimit = Math.max(0, input.concurrencyLimit);
    }
    localApiStats.enabled = localApiSettings.enabled;
    emit("local_api.settings_changed");
    return { ...localApiSettings };
  },
  async issueLocalApiToken() {
    localApiToken = `ym_mock_${Math.random().toString(36).slice(2, 12)}`;
    localApiSettings.tokenPresent = true;
    localApiSettings.tokenMasked = `••••${localApiToken.slice(-4)}`;
    return { token: localApiToken, issuedAt: now() } satisfies LocalApiToken;
  },
  async revokeLocalApiToken() {
    localApiToken = null;
    localApiSettings.tokenPresent = false;
    localApiSettings.tokenMasked = undefined;
  },
  async getEngineConfig(slot) {
    const existing = engineConfigs.get(slot);
    return existing
      ? clone(existing)
      : { slot, deviceId: "auto", values: {} } satisfies EngineConfig;
  },
  async setEngineConfig(slot, input) {
    countCall(mockControl(), "engineConfigCalls");
    const existing: EngineConfig =
      engineConfigs.get(slot) ?? { slot, deviceId: "auto", values: {} };
    const capability = capabilities.find((capability) => capability.slot === slot);
    if (input.engineId && capability?.engines?.length) {
      const engine = capability.engines.find((item) => item.id === input.engineId);
      if (!engine || !engine.installed || engine.state !== "READY") throw invalidRequest();
      for (const item of capability.engines) item.selected = item.id === engine.id;
      capability.engineName = engine.name;
      capability.version = engine.version;
      capability.state = engine.state;
    }
    const next: EngineConfig = {
      slot,
      engineId: input.engineId ?? existing.engineId,
      deviceId: input.deviceId ?? existing.deviceId ?? "auto",
      values: { ...(existing.values ?? {}), ...(input.values ?? {}) },
      updatedAt: now(),
    };
    engineConfigs.set(slot, next);
    if (next.deviceId) {
      for (const device of devices) {
        device.selected = next.deviceId === "auto" ? device.recommended : device.id === next.deviceId;
      }
    }
    emit("engine.config_updated", { data: { slot, device_id: next.deviceId } });
    return clone(next);
  },

  async listProjects() {
    throwMockFault("listProjects");
    const override = mockControl()?.projectListOverride;
    if (override) return structuredClone(override);
    return projects.map(toSummary);
  },
  async getProject(projectId: ID) {
    throwMockFault("getProject");
    const control = mockControl();
    if (control) control.getProjectCalls = (Number(control.getProjectCalls) || 0) + 1;
    const p = findProject(projectId);
    const project = structuredClone(p);
    if (control?.clearTranslationInput) {
      for (const track of project.tracks) {
        if (track.type !== "translation") continue;
        for (const revision of track.revisions) revision.inputRevisionId = undefined;
      }
    }
    if (control?.reviewRunLanguages) {
      for (const run of project.runs) run.languages = [...control.reviewRunLanguages];
    }
    if (control?.runStatusOverride) {
      for (const run of project.runs) run.status = control.runStatusOverride;
    }
    const fixture = control?.assetFixtures?.[projectId];
    if (fixture) project.assets = structuredClone(fixture);
    const paramFixtures = control?.runParamsFixtures?.[projectId];
    if (paramFixtures) {
      for (const run of project.runs) {
        const params: Record<string, unknown> = { ...(run.params ?? {}) };
        for (const [key, value] of Object.entries(paramFixtures)) {
          if (value === null) delete params[key];
          else params[key] = value;
        }
        run.params = params as RunParams;
      }
    }
    const nameFixture = control?.projectNameFixtures?.[projectId];
    if (nameFixture) project.name = nameFixture;
    // 只挂起下一次调用；快照在挂起前已捕获，晚释放会返回旧数据（e2e 旧读拒绝）。
    const gate = control?.getProjectGate;
    if (control && gate) control.getProjectGate = undefined;
    if (gate) await gate;
    return project;
  },
  async createProject(input) {
    const projectId = id("proj");
    const p: Project = {
      id: projectId,
      workspaceId: "ws-1",
      episodeId: id("ep"),
      name: input.name,
      directory: `/Users/demo/yaomu/projects/${projectId}`,
      schemaVersion: 1,
      editVersion: 1,
      createdAt: now(),
      updatedAt: now(),
      assets: [],
      tracks: [],
      runs: [],
    };
    projects.unshift(p);
    return structuredClone(p);
  },
  async updateProject(projectId: ID, input: { name: string; editVersion?: number }) {
    const project = findProject(projectId);
    const name = input.name.trim();
    if (!name || [...name].length > 30) throw invalidRequest();
    if (
      input.editVersion !== undefined &&
      input.editVersion > 0 &&
      input.editVersion !== project.editVersion
    ) {
      throw new ApiError({
        code: "REVISION_CONFLICT",
        messageKey: "error.project_name_conflict",
        retryable: false,
        actionIds: [],
      });
    }
    project.name = name;
    project.editVersion += 1;
    project.updatedAt = now();
    return structuredClone(project);
  },
  async deleteProject(projectId: ID) {
    const i = projects.findIndex((p) => p.id === projectId);
    if (i >= 0) projects.splice(i, 1);
  },
  async importAssets(input): Promise<AssetImportResult> {
    const control = mockControl();
    const created: Asset[] = input.paths.map((path, i) => {
      const name = path.split("/").pop() ?? `asset-${i}`;
      const lower = name.toLowerCase();
      const type: Asset["type"] = /\.(mp3|wav|aac|flac|m4a)$/.test(lower)
        ? "audio"
        : /\.srt$/.test(lower)
          ? "subtitle"
          : "video";
      const missing = control?.missingImportNames?.includes(name) === true;
      // 与 Core assets.go 对齐：导入音视频为 original，导入 SRT 为 source_subtitle，不写语言。
      const role: Asset["role"] =
        type === "video" || type === "audio"
          ? "original"
          : type === "subtitle"
            ? "source_subtitle"
            : undefined;
      return {
        id: id("asset"),
        projectId: input.projectId,
        type,
        role,
        name,
        path,
        sizeBytes: (100 + i * 37) * 1024 ** 2,
        meta: type === "subtitle"
          ? undefined
          : {
              container: type === "audio" ? "wav" : "mp4",
              codec: type === "audio" ? "pcm_s16le" : "h264",
              width: type === "video" ? 1920 : undefined,
              height: type === "video" ? 1080 : undefined,
              durationMs: 20 * 60 * 1000,
              audioTracks: 1,
              sampleRate: 48000,
            },
        status: missing ? "missing" : "ready",
        importedAt: now(),
      };
    });
    findProject(input.projectId).assets.push(...created);
    return { assetIds: created.map((asset) => asset.id) };
  },

  async relocateAsset(assetId, path): Promise<Asset> {
    const control = mockControl();
    if (control) control.lastRelocateInput = { assetId, path };
    const project = projects.find((candidate) =>
      candidate.assets.some((asset) => asset.id === assetId),
    );
    const asset = project?.assets.find((candidate) => candidate.id === assetId);
    if (!project || !asset) {
      throw new ApiError({
        code: "NOT_FOUND",
        messageKey: "api.notFound",
        retryable: false,
        actionIds: [],
      });
    }
    // mock 没有位置来源信息，只能按状态模拟 Core 的拒绝：copy/generated 资产在
    // Core 侧同样是 ready，不会进入这里。
    if (asset.status !== "missing") {
      throw new ApiError({
        code: "INVALID_REQUEST",
        messageKey: "api.invalidRequest",
        retryable: false,
        actionIds: [],
      });
    }
    asset.path = path;
    asset.status = "ready";
    if (asset.type !== "subtitle") {
      asset.meta = {
        container: asset.type === "audio" ? "wav" : "mp4",
        codec: asset.type === "audio" ? "pcm_s16le" : "h264",
        width: asset.type === "video" ? 1920 : undefined,
        height: asset.type === "video" ? 1080 : undefined,
        durationMs: 20 * 60 * 1000,
        audioTracks: 1,
        sampleRate: 48000,
      };
    }
    emit("asset.relocated", { projectId: project.id, data: { asset_id: asset.id } });
    return structuredClone(asset);
  },

  async listTasks(query?): Promise<TaskList> {
    const control = mockControl();
    countCall(control, "listTasksCalls");
    const items = tasks
      .filter((task) => !query?.projectId || task.projectId === query.projectId)
      .filter((task) => !query?.runId || task.runId === query.runId)
      .filter((task) => !query?.status || task.status === query.status)
      .map(taskSummary);
    if (control?.listTasksGate) await control.listTasksGate;
    return { items };
  },
  async importSubtitleTrack(input) {
    const control = mockControl();
    if (control) {
      control.lastImportTrackInput = input;
      control.lastImportTrackInputs = [...(control.lastImportTrackInputs ?? []), input];
    }
    const project = findProject(input.projectId);
    const type = input.type === "translation" ? "translation" : "transcription";
    const imported = track(input.projectId, input.language, type, [
      seg(301, 0, 1600, "Imported line one."),
      seg(302, 1800, 3600, "Imported line two."),
    ]);
    project.tracks.push(imported);
    return structuredClone(imported);
  },
  async listCustomPipelineTemplates() {
    const seeded = mockControl()?.customTemplateFixtures;
    if (seeded && customTemplates.length === 0) customTemplates = structuredClone(seeded);
    return { items: structuredClone(customTemplates) };
  },
  async saveCustomPipelineTemplates(items) {
    const control = mockControl();
    if (control) control.lastTemplateSave = structuredClone(items);
    const stamp = now();
    customTemplates = items.map((item) => ({
      ...structuredClone(item),
      createdAt: item.createdAt || stamp,
      updatedAt: stamp,
    }));
    return { items: structuredClone(customTemplates) };
  },
  async getTask(taskId) {
    const control = mockControl();
    countCall(control, "getTaskCalls");
    const task = clone(findTask(taskId));
    if (control?.getTaskGate) await control.getTaskGate;
    return task;
  },
  async getExecutionLog(taskId, options) {
    const control = mockControl();
    countCall(control, "executionLogCalls");
    const requested = options?.attempt ?? 0;
    const stored = executionLogs.get(taskId);
    let result: ExecutionLog;
    if (!stored || (requested > 0 && requested !== stored.attempt)) {
      result = { taskId, attempt: 0, lines: [] } satisfies ExecutionLog;
    } else {
      const tail = Math.min(Math.max(options?.tail ?? 200, 1), 1000);
      result = {
        taskId,
        attempt: stored.attempt,
        lines: clone(stored.lines.slice(-tail)),
      } satisfies ExecutionLog;
    }
    if (control?.executionLogGate) await control.executionLogGate;
    return result;
  },
  async cancelTask(taskId) {
    const control = mockControl();
    countCall(control, "cancelTaskCalls");
    if (control?.taskActionFailure) throw new Error("mock task action secret");
    const t = findTask(taskId);
    t.status = "CANCEL_REQUESTED";
    t.updatedAt = now();
    emit("task.cancel_requested", { taskId });
    t.status = "CANCELED";
    emit("task.canceled", { taskId });
  },
  async retryTask(taskId) {
    const control = mockControl();
    countCall(control, "retryTaskCalls");
    if (control?.taskActionFailure) throw new Error("mock task action secret");
    const t = findTask(taskId);
    t.status = "QUEUED";
    t.error = undefined;
    t.updatedAt = now();
    emit("task.queued", { taskId });
    return clone(t);
  },
  async getRun(runId) {
    const control = mockControl();
    if (control?.getRunFailures && control.getRunFailures > 0) {
      control.getRunFailures -= 1;
      throw new ApiError({
        code: "CORE_NOT_READY",
        messageKey: "api.coreNotReady",
        retryable: true,
        actionIds: [],
      });
    }
    const run = findRun(runId);
    if (!run) throw notFound();
    const cloned = clone(run);
    if (control?.reviewRunLanguages) cloned.languages = [...control.reviewRunLanguages];
    if (control?.runStatusOverride) cloned.status = control.runStatusOverride;
    return cloned;
  },
  async pauseRun(runId) {
    const run = findRun(runId) ?? projects[0].runs[0];
    if (run) {
      run.status = "PAUSE_REQUESTED";
      run.updatedAt = now();
      emit("run.pause_requested", { projectId: run.projectId, taskId: run.id });
    }
    return clone(run ?? makeRun("proj-demo", "mock"));
  },
  async resumeRun(runId) {
    const run = findRun(runId) ?? projects[0].runs[0];
    if (run) {
      run.status = "RUNNING";
      run.updatedAt = now();
      emit("run.resumed", { projectId: run.projectId, taskId: run.id });
    }
    return clone(run ?? makeRun("proj-demo", "mock"));
  },
  async preflight(input) {
    const control = mockControl();
    if (control?.preflightError) throw new Error(control.preflightError);
    if (control?.preflightIssues) return { issues: clone(control.preflightIssues) };
    // 与 Core 同序：仅 tool_voice_clone 且 validate_timestamps=true 时检查 07 时间窗。
    if (
      control?.cloneTimelineIssue &&
      input.template === "tool_voice_clone" &&
      input.validateTimestamps === true
    ) {
      return {
        issues: [
          {
            severity: "blocker",
            code:
              control.cloneTimelineIssue === "out_of_range"
                ? "CLONE_TIMELINE_OUT_OF_RANGE"
                : "CLONE_TIMELINE_UNKNOWN_DURATION",
            messageKey:
              control.cloneTimelineIssue === "out_of_range"
                ? "preflight.clone_timeline_out_of_range"
                : "preflight.clone_timeline_unknown_duration",
            actionIds: [],
          },
        ],
      };
    }
    const invalid = (): ApiError =>
      new ApiError({
        code: "INVALID_REQUEST",
        messageKey: "api.invalidRequest",
        retryable: false,
        actionIds: [],
      });
    if (input.template !== "custom_pipeline") {
      if (input.features && input.features.length > 0) throw invalid();
      return { issues: [] };
    }
    const features = input.features ?? [];
    if (!validCustomFeatures(features)) throw invalid();
    const project = findProject(input.projectId);
    const asset = project.assets.find((item) => item.id === input.inputRevisionIds?.[0]);
    const needsVideo = features.includes("erase") || features.includes("mix");
    const validAsset =
      asset !== undefined &&
      (needsVideo ? asset.type === "video" : asset.type === "video" || asset.type === "audio");
    const issues: PreflightIssue[] = [];
    if (!validAsset) {
      issues.push({
        severity: "blocker",
        code: "INPUT_ASSET_INVALID",
        messageKey: "preflight.input_asset_invalid",
        actionIds: ["reselect_input_media"],
      });
    }
    if (
      features.includes("erase") &&
      (!input.extract || input.extract.mode !== "ocr" || !input.extract.ocrLanguage)
    ) {
      issues.push({
        severity: "blocker",
        code: "OCR_LANGUAGE_REQUIRED",
        messageKey: "preflight.ocr_language_required",
        actionIds: ["choose_ocr_language"],
      });
    }
    return { issues };
  },
  async createWorkflowRun(input) {
    const control = mockControl();
    if (control) control.lastRunInput = input;
    const project = findProject(input.projectId);
    if (input.template === "custom_pipeline") {
      const features = input.features ?? [];
      if (!validCustomFeatures(features)) {
        throw new ApiError({
          code: "INVALID_REQUEST",
          messageKey: "api.invalidRequest",
          retryable: false,
          actionIds: [],
        });
      }
      const needsText = features.some((item) =>
        item === "translate" || item === "clone" || item === "mix",
      );
      if (needsText && input.targetLanguages.length === 0) {
        throw new ApiError({
          code: "INVALID_REQUEST",
          messageKey: "api.invalidRequest",
          retryable: false,
          actionIds: [],
        });
      }
      const source = project.assets.find((item) => item.id === input.inputRevisionIds?.[0]);
      if (source?.type === "audio" && (features.includes("erase") || features.includes("mix"))) {
        throw new ApiError({
          code: "INVALID_REQUEST",
          messageKey: "api.invalidRequest",
          retryable: false,
          actionIds: [],
        });
      }
      const nodes: WorkflowRun["nodes"] = [{ key: "01_import", status: "done", progress: 100 }];
      if (features.includes("separation")) {
        nodes.push({ key: "03_separate", status: "running", progress: 30 });
      }
      if (features.includes("erase") || needsText) {
        const reused = Boolean(input.reuseSubtitleTrackId) && !features.includes("erase");
        nodes.push({ key: "04_extract", status: reused ? "reused" : "running", progress: reused ? 100 : 30 });
      }
      if (features.includes("erase")) {
        nodes.push({ key: "05_erase", status: "running", progress: 30 });
      }
      if (needsText) {
        nodes.push({ key: "06_translate", status: "running", progress: 30 });
      }
      if (features.includes("clone")) {
        nodes.push({ key: "07_clone", status: "pending", progress: 0 });
      }
      if (features.includes("mix")) {
        nodes.push({ key: "08_mix", status: "pending", progress: 0 });
      }
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "custom_pipeline",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes,
        params: frozenRunParams(input),
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      customRunInputs.set(run.id, input);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      // 仅 DEV/E2E mock：模拟节点推进、06/08 审核与候选/产物。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        runCustomPipeline(project, run, input);
      }, 30);
      return structuredClone(run);
    }
    if (input.template === "tool_voice_clone") {
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "tool_voice_clone",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes: [
          { key: "01_import", status: "done", progress: 100 },
          { key: "07_clone", status: "running", progress: 40 },
        ],
        params: frozenRunParams(input),
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      // 仅 DEV/E2E mock：模拟 07 逐句克隆候选；真实 Core 由 segment task 驱动。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        if (!control?.voiceCloneNoCandidates) {
          for (const language of input.targetLanguages) {
            project.assets.push({
              id: id("asset"),
              projectId: input.projectId,
              runId: run.id,
              type: "audio",
              role: "full_dub_audio",
              language,
              name: `full-${language}.wav`,
              sizeBytes: 1024 * 1024,
              status: "ready",
              importedAt: now(),
            });
          }
        }
        const task: Task = {
          id: id("task"),
          projectId: input.projectId,
          runId: run.id,
          kind: "node",
          nodeKey: "07_clone",
          label: "Voice clone",
          status: "SUCCEEDED",
          progress: 100,
          artifactIds: [],
          createdAt: now(),
          updatedAt: now(),
        };
        tasks.push(task);
        appendExecutionLog(task.id, 1, "runner", "启动受管 FlashCLI 克隆");
        appendExecutionLog(task.id, 1, "runner", "FlashCLI serve 健康检查通过");
        appendExecutionLog(task.id, 1, "runner", "克隆音频合成完成");
        run.status = "SUCCEEDED";
        run.updatedAt = now();
        run.nodes = run.nodes.map((node) => ({ ...node, status: "done", progress: 100 }));
        emit("run.succeeded", { projectId: input.projectId, taskId: run.id });
      }, 30);
      return structuredClone(run);
    }
    if (input.template === "tool_translate") {
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "tool_translate",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes: [
          { key: "01_import", status: "done", progress: 100 },
          { key: "06_translate", status: "running", progress: 0 },
        ],
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      const task: Task = {
        id: id("task"),
        projectId: input.projectId,
        runId: run.id,
        kind: "node",
        nodeKey: "06_translate",
        label: "Subtitle translation",
        status: "RUNNING",
        progress: 0,
        artifactIds: [],
        createdAt: now(),
        updatedAt: now(),
      };
      tasks.push(task);
      appendExecutionLog(task.id, 1, "runner", "启动 mlstools 字幕翻译");
      // 仅 DEV/E2E mock：模拟 06 逐语翻译并回写 translation 轨；真实 Core 由任务驱动。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          task.status = "FAILED";
          task.updatedAt = now();
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        const partial = control?.toolRunPartial === true || control?.toolRunPartialNoTracks === true;
        const completed = control?.toolRunPartialNoTracks
          ? []
          : control?.toolRunPartial
            ? input.targetLanguages.slice(0, 1)
            : input.targetLanguages;
        for (const language of completed) {
          if (project.tracks.some((item) => item.type === "translation" && item.language === language)) {
            continue;
          }
          project.tracks.push(
            track(project.id, language, "translation", [
              seg(401, 0, 1600, `Translated ${language} line one.`),
              seg(402, 1800, 3600, `Translated ${language} line two.`),
            ]),
          );
        }
        run.updatedAt = now();
        if (control?.toolRunPartialNoTracks) {
          task.status = "FAILED";
          task.updatedAt = now();
        } else {
          task.status = "SUCCEEDED";
          task.progress = 100;
          task.updatedAt = now();
          appendExecutionLog(task.id, 1, "runner", "翻译执行中，已用时 10 秒");
          appendExecutionLog(task.id, 1, "runner", "mlstools 字幕翻译完成");
        }
        if (partial) {
          run.status = "PARTIAL";
          run.nodes = run.nodes.map((node) =>
            node.key === "06_translate"
              ? { ...node, status: "failed", progress: 100 }
              : { ...node, status: "done", progress: 100 },
          );
          return;
        }
        run.status = "SUCCEEDED";
        run.nodes = run.nodes.map((node) => ({ ...node, status: "done", progress: 100 }));
        emit("run.succeeded", { projectId: input.projectId, taskId: run.id });
      }, control?.toolRunDelayMs ?? 30);
      return structuredClone(run);
    }
    if (input.template === "tool_erase") {
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "tool_erase",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes: [
          { key: "01_import", status: "done", progress: 100 },
          { key: "04_extract", status: "running", progress: 40 },
          { key: "05_erase", status: "pending", progress: 0 },
        ],
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      // 仅 DEV/E2E mock：模拟 04 OCR/05 擦除完成并回写纯净视频；真实 Core 由任务驱动。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        const clean: Asset = {
          id: id("asset"),
          projectId: input.projectId,
          type: "video",
          name: "clean.mp4",
          sizeBytes: 120 * 1024 ** 2,
          status: "ready",
          importedAt: now(),
        };
        project.assets.push(clean);
        tasks.push({
          id: id("task"),
          projectId: input.projectId,
          runId: run.id,
          kind: "node",
          nodeKey: "05_erase",
          label: "Subtitle erase",
          status: "SUCCEEDED",
          progress: 100,
          artifactIds: [clean.id],
          createdAt: now(),
          updatedAt: now(),
        });
        run.status = "SUCCEEDED";
        run.updatedAt = now();
        run.nodes = run.nodes.map((node) => ({ ...node, status: "done", progress: 100 }));
        emit("run.succeeded", { projectId: input.projectId, taskId: run.id });
      }, 30);
      return structuredClone(run);
    }
    if (input.template === "tool_separate") {
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "tool_separate",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes: [
          { key: "01_import", status: "done", progress: 100 },
          { key: "03_separate", status: "running", progress: 40 },
        ],
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      // 仅 DEV/E2E mock：模拟 03 完成并回写两个人声/环境音轨；真实 Core 由任务驱动。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        const voice: Asset = {
          id: id("asset"),
          projectId: input.projectId,
          type: "audio_track",
          name: "voice.wav",
          sizeBytes: 12 * 1024 ** 2,
          status: "ready",
          importedAt: now(),
        };
        const ambience: Asset = {
          id: id("asset"),
          projectId: input.projectId,
          type: "audio_track",
          name: "ambience.wav",
          sizeBytes: 8 * 1024 ** 2,
          status: "ready",
          importedAt: now(),
        };
        project.assets.push(voice, ambience);
        const task: Task = {
          id: id("task"),
          projectId: input.projectId,
          runId: run.id,
          kind: "node",
          nodeKey: "03_separate",
          label: "Audio separation",
          status: "SUCCEEDED",
          progress: 100,
          artifactIds: [voice.id, ambience.id],
          createdAt: now(),
          updatedAt: now(),
        };
        tasks.push(task);
        appendExecutionLog(task.id, 1, "runner", "启动 FlashCLI 分离");
        appendExecutionLog(task.id, 1, "stderr", "分离进度 100%");
        appendExecutionLog(task.id, 1, "runner", "FlashCLI 分离完成");
        run.status = "SUCCEEDED";
        run.updatedAt = now();
        run.nodes = run.nodes.map((node) => ({ ...node, status: "done", progress: 100 }));
        emit("run.succeeded", { projectId: input.projectId, taskId: run.id });
      }, 30);
      return structuredClone(run);
    }
    if (input.template === "tool_extract") {
      const run: WorkflowRun = {
        id: id("run"),
        projectId: input.projectId,
        name: input.name,
        template: "tool_extract",
        status: "RUNNING",
        sourceLanguage: input.sourceLanguage ?? "und",
        languages: input.targetLanguages,
        inputAssetIds: input.inputRevisionIds ?? [],
        nodes: [
          { key: "01_import", status: "done", progress: 100 },
          { key: "02_analyze", status: "skipped", progress: 100 },
          { key: "04_extract", status: "running", progress: 0 },
        ],
        createdAt: now(),
        updatedAt: now(),
      };
      project.runs.push(run);
      emit("run.created", { projectId: input.projectId, taskId: run.id });
      const task: Task = {
        id: id("task"),
        projectId: input.projectId,
        runId: run.id,
        kind: "node",
        nodeKey: "04_extract",
        label: "Subtitle extraction",
        status: "RUNNING",
        progress: 0,
        artifactIds: [],
        createdAt: now(),
        updatedAt: now(),
      };
      tasks.push(task);
      appendExecutionLog(task.id, 1, "runner", "转换 ASR 输入为 16 kHz 单声道 WAV");
      appendExecutionLog(task.id, 1, "runner", "启动 FlashCLI ASR");
      // 仅 DEV/E2E mock：模拟 02/04 完成并回写 transcription 轨；真实 Core 由任务驱动。
      setTimeout(() => {
        if (control?.toolRunFailure) {
          task.status = "FAILED";
          task.updatedAt = now();
          run.status = "FAILED";
          run.updatedAt = now();
          run.nodes = run.nodes.map((node) => ({ ...node, status: "failed", progress: 100 }));
          emit("run.failed", { projectId: input.projectId, taskId: run.id });
          return;
        }
        task.status = "SUCCEEDED";
        task.progress = 100;
        task.updatedAt = now();
        appendExecutionLog(task.id, 1, "runner", "ASR 执行中，已用时 10 秒");
        appendExecutionLog(task.id, 1, "runner", "ASR 完成，生成 2 条字幕");
        run.status = "SUCCEEDED";
        run.updatedAt = now();
        run.nodes = run.nodes.map((node) => ({ ...node, status: "done", progress: 100 }));
        if (!project.tracks.some((item) => item.type === "transcription")) {
          project.tracks.push(
            track(project.id, input.sourceLanguage ?? "und", "transcription", [
              seg(201, 0, 1600, "Extracted line one."),
              seg(202, 1800, 3600, "Extracted line two."),
            ]),
          );
        }
        emit("run.succeeded", { projectId: input.projectId, taskId: run.id });
      }, control?.toolRunDelayMs ?? 30);
      return structuredClone(run);
    }
    const run: WorkflowRun = {
      id: id("run"),
      projectId: input.projectId,
      name: input.name,
      template: input.template ?? "full_pipeline",
      status: "QUEUED",
      sourceLanguage: input.sourceLanguage ?? "und",
      languages: input.targetLanguages,
      inputAssetIds: input.inputRevisionIds ?? [],
      nodes: PIPELINE_NODE_KEYS.map((key) => ({ key, status: "pending", progress: 0 })),
      params: frozenRunParams(input),
      createdAt: now(),
      updatedAt: now(),
    };
    project.runs.push(run);
    emit("run.created", { projectId: input.projectId, taskId: run.id });
    return structuredClone(run);
  },
  async getSnapshot(projectId): Promise<ProjectSnapshot> {
    const control = mockControl();
    countCall(control, "getSnapshotCalls");
    const project = findProject(projectId);
    const snapshot: ProjectSnapshot = {
      projectId,
      project: toSummary(project),
      runs: structuredClone(project.runs),
      tasks: tasks.filter((task) => task.projectId === projectId).map(taskSummary),
      eventSeq: seq,
    };
    if (control?.getSnapshotGate) await control.getSnapshotGate;
    return snapshot;
  },

  async saveSubtitle(input) {
    const control = mockControl();
    countCall(control, "saveSubtitleCalls");
    if (control?.saveSubtitleGate) await control.saveSubtitleGate;
    if (control?.saveSubtitleFailure === "conflict") throw revisionConflict();
    const project = projects.find((p) => p.tracks.some((t) => t.id === input.trackId));
    const track = project?.tracks.find((t) => t.id === input.trackId);
    if (!project || !track) throw notFound();
    // 乐观并发：base revision 必须仍是当前 revision。
    if ((track.currentRevisionId ?? "") !== input.baseRevisionId) throw revisionConflict();

    const current = track.revisions.find((revision) => revision.id === track.currentRevisionId);
    const previous = new Map((current?.segments ?? []).map((segment) => [segment.id, segment]));
    const baseIDs = new Set(previous.keys());

    // 与 Core.ValidateSegmentInputs + ValidateSegmentLineage 同口径的输入校验。
    const seen = new Set<ID>();
    let prevEnd = 0;
    input.segments.forEach((segment, index) => {
      if (segment.startMs < 0 || segment.endMs <= segment.startMs) throw invalidRequest();
      if (index > 0 && segment.startMs < prevEnd) throw invalidRequest();
      prevEnd = segment.endMs;
      if (!segment.id) {
        if (segment.lineage?.length) {
          const refs = new Set<ID>();
          for (const ref of segment.lineage) {
            if (!ref || refs.has(ref) || !baseIDs.has(ref)) throw invalidRequest();
            refs.add(ref);
          }
        }
        return;
      }
      if (seen.has(segment.id)) throw invalidRequest();
      seen.add(segment.id);
    });

    const segments: Segment[] = input.segments.map((segment) => {
      const segmentID = segment.id || id("seg");
      const contentDigest = mockSegmentDigest(segment.text, segment.speaker);
      const old = previous.get(segmentID);
      // 与 Core 一致：用旧内容重算摘要再比较，避免 seed 数据的摘要格式不同导致
      // 未改动的句段被误判为内容变化（Core 存储的摘要本身由内容计算）。
      const oldDigest = old ? mockSegmentDigest(old.text, old.speaker) : undefined;
      const contentRevision = old
        ? oldDigest === contentDigest
          ? old.contentRevision
          : old.contentRevision + 1
        : 1;
      return {
        id: segmentID,
        startMs: segment.startMs,
        endMs: segment.endMs,
        text: segment.text,
        speaker: segment.speaker,
        contentRevision,
        contentDigest,
        lineage: segment.lineage && segment.lineage.length > 0 ? [...segment.lineage] : undefined,
      };
    });

    // 从 Vue reactive 代理复制成普通对象：structuredClone 不能克隆 Proxy。
    const style = input.style
      ? {
          ...input.style,
          outline: input.style.outline ? { ...input.style.outline } : undefined,
          shadow: input.style.shadow ? { ...input.style.shadow } : undefined,
        }
      : undefined;
    const eraseBlocks = input.eraseBlocks?.map((block) => ({
      ...block,
      bbox: [...block.bbox] as [number, number, number, number],
    }));
    const inputRevisionId =
      track.type === "translation"
        ? (() => {
            const runs = [...project.runs].sort(
              (a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt),
            );
            const sourceLanguage = runs.at(-1)?.sourceLanguage ?? "";
            let sourceTrack = sourceLanguage
              ? project.tracks.find(
                  (item) =>
                    item.type === "transcription" && item.language === sourceLanguage,
                )
              : undefined;
            if (!sourceTrack) {
              const candidates = project.tracks.filter(
                (item) => item.type === "transcription" && item.currentRevisionId,
              );
              if (candidates.length === 1) sourceTrack = candidates[0];
            }
            return sourceTrack?.currentRevisionId;
          })()
        : undefined;
    const rev: SubtitleRevision = {
      id: id("rev"),
      trackId: track.id,
      parentRevisionId: track.currentRevisionId,
      authorType: "user",
      segments,
      inputRevisionId,
      // 与 Core 整轨替换语义一致：调用方回传什么就保存什么；省略即清空。
      style,
      eraseBlocks,
      createdAt: now(),
    };
    track.revisions.push(rev);
    track.currentRevisionId = rev.id;

    // 与 Core 同口径：受影响的非终止 Run 生成新的 pending 审核门槛；
    // 旧 approved 绑定旧摘要，不再放行新内容。translation 门槛使用 raw revision
    // （不再生成 `摘要:语言` 后缀），delivery 按语种绑定实际译文 revision。
    for (const run of project.runs) {
      if (!reviewableRun(run)) continue;
      const languages = track.type === "translation" ? [track.language] : run.languages;
      for (const language of languages) {
        const translationRevision =
          track.type === "translation" ? rev : currentTranslationRevision(project, language);
        if (!translationRevision) continue;
        if (track.type === "translation") {
          addMockGate(
            run,
            "translation",
            language,
            translationRevisionDigest(translationRevision.id),
          );
        }
        addMockGate(run, "delivery", language, deliveryRevisionDigest(translationRevision.id));
      }
    }

    if (control) {
      control.lastSavedRevision = structuredClone(rev);
      control.lastSavedTracks = [
        ...(control.lastSavedTracks ?? []),
        {
          trackId: rev.trackId,
          text: rev.segments[0]?.text ?? "",
          startMs: rev.segments[0]?.startMs ?? 0,
          endMs: rev.segments[0]?.endMs ?? 0,
        },
      ];
      const runIds = new Set(project.runs.map((run) => run.id));
      control.lastSavedReviews = reviews
        .filter((gate) => runIds.has(gate.runId))
        .map((gate) => ({ ...gate }));
    }
    return structuredClone(rev);
  },

  async generateCandidates(input): Promise<SegmentCandidateJob> {
    const existing = candidatesFor(input.segmentId, input.language);
    const revision = (existing[0]?.revision ?? 0) + 1;
    const created = makeCandidates(input.segmentId, input.language, revision);
    candidateStore.set(candidateKey(input.segmentId, input.language), [...existing, ...created]);
    const job: SegmentCandidateJob = {
      taskId: id("task"),
      segmentId: input.segmentId,
      language: input.language,
      attemptNo: revision,
    };
    emit("segment.candidates_ready", { taskId: job.taskId });
    return job;
  },
  async listCandidates(segmentId, language) {
    return candidatesFor(segmentId, language).map((candidate) => ({ ...candidate }));
  },
  async selectCandidate(segmentId, language, candidateId): Promise<CandidateSelection> {
    const candidate = candidatesFor(segmentId, language).find((item) => item.id === candidateId);
    if (!candidate) throw notFound();
    const selection: CandidateSelection = {
      segmentId,
      language,
      candidateId,
      revision: candidate.revision,
      selectedAt: now(),
    };
    selectionStore.set(candidateKey(segmentId, language), selection);
    return { ...selection };
  },
  async getSelection(segmentId, language) {
    const selection = selectionStore.get(candidateKey(segmentId, language));
    return selection ? { ...selection } : null;
  },
  async listReviews(projectId, runId?) {
    applyReviewFixtures();
    const control = mockControl();
    if (control?.reviewReadFailure || control?.failNextReviewList) {
      if (control.failNextReviewList) control.failNextReviewList = false;
      throw coreNotReadyError();
    }
    const project = findProject(projectId);
    const runIds = new Set(project.runs.map((run) => run.id));
    const fixtureMode = control?.reviewFixtures !== undefined;
    // 调用时捕获快照：挂起读取释放后返回旧列表，验证 UI 丢弃晚回包。
    const captured = reviews
      .filter((gate) => runIds.has(gate.runId) && (!runId || gate.runId === runId))
      .map((gate) => readGate(gate, fixtureMode));
    if (control?.reviewListGate) await control.reviewListGate;
    return captured;
  },
  async getReview(reviewId) {
    applyReviewFixtures();
    const gate = reviews.find((item) => item.id === reviewId);
    if (!gate) throw notFound();
    return readGate(gate, mockControl()?.reviewFixtures !== undefined);
  },
  async approveReview(input) {
    applyReviewFixtures();
    const control = mockControl();
    if (control) {
      // input 可能来自 Vue reactive proxy，不能直接 structuredClone（DataCloneError）。
      const record: ApproveReviewInput = {
        reviewId: input.reviewId,
        revisionDigest: input.revisionDigest,
        type: input.type,
        currentBinding: input.currentBinding ? { ...input.currentBinding } : undefined,
      };
      (control.approveReviewInputs ??= []).push(record);
    }
    const gate = reviews.find((r) => r.id === input.reviewId);
    if (!gate) throw notFound();
    const conflict = () =>
      new ApiError({
        code: "REVISION_CONFLICT",
        messageKey: "api.revisionConflict",
        retryable: false,
        actionIds: [],
      });
    if (gate.revisionDigest !== input.revisionDigest || gate.type !== input.type) {
      throw conflict();
    }
    // 新客户端提交的 current_binding 必须在批准事务内仍为当前绑定。
    if (input.currentBinding) {
      const bindings =
        control?.reviewFixtures !== undefined
          ? (gate.currentBindings ?? [])
          : projectCurrentBindings(gate);
      const found = bindings.some(
        (binding) =>
          binding.language === input.currentBinding?.language &&
          binding.sourceKind === input.currentBinding?.sourceKind &&
          binding.sourceId === input.currentBinding?.sourceId,
      );
      if (!found) throw conflict();
    }
    gate.status = "approved";
    gate.confirmedAt = now();
    // 显式测试开关：批准后的权威重读失败（验证 UI 不假同步）。
    if (control?.reviewRefreshFailure) control.failNextReviewList = true;
    emit("review.approved", { projectId: gate.runId });
    const run = findRun(gate.runId);
    const runInput = customRunInputs.get(gate.runId);
    if (run && runInput && run.template === "custom_pipeline") {
      const project = findProject(run.projectId);
      if (gate.type === "translation") {
        const pending = reviews.some(
          (item) => item.runId === run.id && item.type === "translation" && item.status === "pending",
        );
        if (!pending) advanceCustomAfterTranslation(project, run, runInput);
      } else if (gate.type === "delivery") {
        finishCustomRun(project, run, runInput);
      }
    }
    // 显式 e2e 开关：重新合成只推进 07/08 到交付审核，不自动放行 delivery。
    if (
      mockControl()?.remixOnApprove &&
      gate.type === "translation" &&
      run &&
      run.template !== "custom_pipeline"
    ) {
      advanceRemixAfterTranslationApproval(gate);
    }
    if (control?.approveReviewGate) await control.approveReviewGate;
    // 批准响应不保证携带最新投影；客户端必须重新读取列表。
    return structuredClone(gate);
  },

  async export(input) {
    const control = mockControl();
    countCall(control, "exportCalls");
    if (control) {
      control.lastExportInput = input;
      control.exportInputs = [...(control.exportInputs ?? []), input];
    }
    const record: ExportRecord = {
      id: id("exp"),
      projectId: input.projectId,
      runId: input.runId,
      kind: input.kind,
      format: input.format,
      status: "QUEUED",
      createdAt: now(),
    };
    exportRecords.push(record);
    emit("export.queued", { projectId: input.projectId, taskId: record.id });
    // 仅 DEV/E2E mock：模拟导出完成/失败，供工具页轮询；真实 Core 由任务驱动。
    setTimeout(() => {
      record.status = control?.exportFailure ? "FAILED" : "SUCCEEDED";
      if (record.status === "SUCCEEDED") {
        record.outputPath = `outputs/${record.id}.srt`;
      }
      emit(record.status === "SUCCEEDED" ? "export.succeeded" : "export.failed", {
        projectId: input.projectId,
        taskId: record.id,
      });
    }, 30);
    return structuredClone(record);
  },
  async listExports(projectId) {
    return exportRecords
      .filter((record) => record.projectId === projectId)
      .map((record) => structuredClone(record));
  },

  async saveAsset(input) {
    const control = mockControl();
    if (control) {
      control.lastSaveAssetInput = input;
      countCall(control, "saveAssetCalls");
    }
    if (control?.saveAssetFailure) {
      throw new ApiError({
        code: "FILE_SAVE_FAILED",
        messageKey: "api.fileSaveFailed",
        retryable: true,
        actionIds: [],
      });
    }
    const name = input.suggestedName?.trim() || `${input.assetId}.bin`;
    const base = name.split(/[\\/]/).pop() || `${input.assetId}.bin`;
    return { path: `~/Downloads/${base}` };
  },

  async getInstallations() {
    applyInstallationFixtures();
    return installations.map((i) => structuredClone(i));
  },
  async planInstallation(slot): Promise<InstallationPlan> {
    const control = mockControl();
    if (control) {
      control.lastPlanSlot = slot as CapabilitySlotKey;
      countCall(control, "planRequests");
    }
    const cap = requireCap(slot);
    const release = defaultReleaseForSlot(cap.slot);
    const coveredSlots = plannedCoverage(cap.slot);
    const forcedReuse = control?.planReuse?.[cap.slot];
    const reusable = forcedReuse
      ? installations.find((inst) => inst.id === forcedReuse)
      : findReusableInstallation(release);
    const plan: InstallationPlan = {
      planId: id("plan"),
      planHash: `hash-${cap.slot}-${Math.random().toString(36).slice(2, 10)}`,
      slot: cap.slot,
      ...(coveredSlots !== undefined ? { coveredSlots } : {}),
      ...(reusable ? { reuseInstallationId: reusable.id } : {}),
      release,
      // 一份物理安装的体积只计一次：join 活动操作不产生新下载；
      // 已验证 Done 复用报告可复用字节；安装后占用仍是该发布的一份。
      downloadBytes: reusable ? 0 : MOCK_PACKAGE_BYTES,
      cacheReuseBytes: reusable?.phase === "done" ? MOCK_PACKAGE_BYTES : 0,
      installBytes: MOCK_PACKAGE_BYTES,
      peakStagingBytes: MOCK_PACKAGE_BYTES,
      permissions: [],
      license: { licenseId: "mock", licenseName: "Mock license", noticeKeys: [] },
      requiresRestart: false,
    };
    plans.set(plan.planId, plan);
    return clone(plan);
  },
  async install(input) {
    const control = mockControl();
    countCall(control, "installRequests");
    const plan = plans.get(input.planId);
    if (!plan || plan.planHash !== input.planHash) {
      throw installationError("PREFLIGHT_STALE", "api.preflightStale");
    }
    const covered =
      plan.coveredSlots === undefined ? [plan.slot] : [...plan.coveredSlots];
    // 异常覆盖不能静默按单槽执行：与 UI 的确认拦截保持同一 fail-closed 口径。
    if (
      covered.length === 0 ||
      new Set(covered).size !== covered.length ||
      !covered.includes(plan.slot) ||
      !covered.every((slot) => CAPABILITY_SLOTS.some((meta) => meta.key === slot))
    ) {
      throw installationError("INVALID_RESPONSE", "api.invalidResponse");
    }

    const reuse = plan.reuseInstallationId
      ? installations.find((inst) => inst.id === plan.reuseInstallationId)
      : undefined;
    if (plan.reuseInstallationId && !reuse) {
      throw installationError("PREFLIGHT_STALE", "api.preflightStale");
    }
    if (reuse) {
      const known = installationReleaseById.get(reuse.id);
      if (!known || releaseKey(known) !== releaseKey(plan.release)) {
        throw installationError("PREFLIGHT_STALE", "api.preflightStale");
      }
      const frozen = installationCoveredSlots(reuse);
      if (!sameSlots(frozen, covered)) {
        // 同一实体不同覆盖是冲突，不能借复用悄悄扩大同意范围。
        throw installationError("IDEMPOTENCY_CONFLICT", "api.conflict");
      }
      if (reuse.phase === "done") {
        const verified = installationVerifiedById.get(reuse.id) ?? reuse.verifiedFiles ?? 0;
        if (verified <= 0) {
          throw installationError("CAPABILITY_UNVERIFIED", "installation.verify_failed");
        }
        activateInstallation(reuse, covered);
        emit("installation.completed", {
          data: { installation_id: reuse.id, slot: reuse.slot },
        });
        return clone(reuse);
      }
      if (isInstallationActive(reuse)) {
        reuse.boundSlots = uniqueSlots([
          ...installationBoundSlots(reuse),
          ...covered,
        ]);
        syncCapabilitySlots(covered);
        return clone(reuse);
      }
      // 失败/取消的历史实体不能复用为新的成功安装。
      throw installationError("PREFLIGHT_STALE", "api.preflightStale");
    }

    // 不同发布覆盖重叠槽位时拒绝并发，避免晚完成的旧版本覆盖新选择。
    const overlapping = installations.find(
      (inst) =>
        isInstallationActive(inst) &&
        installationCoveredSlots(inst).some((slot) => covered.includes(slot)) &&
        releaseKey(installationReleaseById.get(inst.id) ?? defaultReleaseForSlot(inst.slot)) !==
          releaseKey(plan.release),
    );
    if (overlapping) {
      throw installationError("IDEMPOTENCY_CONFLICT", "api.conflict");
    }

    // 相同精确发布的并发请求 join 同一物理安装，不启动第二传输/进度。
    const sameRelease = installations.find(
      (inst) =>
        isInstallationActive(inst) &&
        releaseKey(installationReleaseById.get(inst.id) ?? defaultReleaseForSlot(inst.slot)) ===
          releaseKey(plan.release) &&
        sameSlots(installationCoveredSlots(inst), covered),
    );
    if (sameRelease) {
      sameRelease.boundSlots = uniqueSlots([
        ...installationBoundSlots(sameRelease),
        ...covered,
      ]);
      // 第二个槽位加入后也按该物理操作显示进行中，不新建第二条进度。
      syncCapabilitySlots(covered);
      return clone(sameRelease);
    }

    const inst: Installation = {
      id: id("inst"),
      slot: plan.slot,
      coveredSlots: [...covered],
      boundSlots: [...covered],
      currentSlots: [],
      phase: "downloading",
      progress: 0,
      planHash: plan.planHash,
    };
    installationReleaseById.set(inst.id, clone(plan.release));
    installations.unshift(inst);
    countCall(control, "installationStarts");
    syncCapabilitySlots(covered);
    emit("installation.started", {
      data: { installation_id: inst.id, slot: inst.slot },
    });
    return clone(inst);
  },
  async cancelInstallation(idValue) {
    const i = installations.find((x) => x.id === idValue);
    if (!i) throw notFound();
    const control = mockControl();
    if (control) {
      control.cancelInstallationIds = [
        ...(control.cancelInstallationIds ?? []),
        i.id,
      ];
    }
    // 取消作用于该物理安装的全部覆盖槽；旧当前版本保持选中。
    i.phase = "canceled";
    syncCapabilitySlots([
      ...installationCoveredSlots(i),
      ...installationBoundSlots(i),
    ]);
    emit("installation.phase_changed", {
      data: { installation_id: i.id, phase: "canceled", slot: i.slot },
    });
    return structuredClone(i);
  },
  async rollbackInstallation(slot) {
    const cap = requireCap(slot);
    const done = installations.filter(
      (inst) => inst.phase === "done" && installationCurrentSlots(inst).includes(cap.slot),
    );
    const current = done[0];
    const currentRelease = current
      ? installationReleaseById.get(current.id)
      : undefined;
    const previous = done.slice(1).find((inst) => {
      const known = installationReleaseById.get(inst.id);
      return (
        known !== undefined &&
        (currentRelease === undefined || releaseKey(known) !== releaseKey(currentRelease))
      );
    });
    if (!current || !previous) {
      throw installationError(
        "MODEL_NOT_INSTALLED",
        "installation.rollback_unavailable",
      );
    }
    // 只切请求槽的当前指针；不复制 Done 行，不改其他槽或冻结锁。
    current.currentSlots = installationCurrentSlots(current).filter(
      (entry) => entry !== cap.slot,
    );
    previous.boundSlots = uniqueSlots([
      ...installationBoundSlots(previous),
      cap.slot,
    ]);
    previous.currentSlots = uniqueSlots([
      ...installationCurrentSlots(previous),
      cap.slot,
    ]);
    syncCapabilitySlots([cap.slot]);
    emit("installation.phase_changed", {
      data: { installation_id: previous.id, phase: "done", slot: cap.slot },
    });
    return structuredClone(previous);
  },
  async deleteInstallation(slot) {
    const cap = requireCap(slot);
    for (let i = installations.length - 1; i >= 0; i -= 1) {
      const inst = installations[i];
      if (inst.boundSlots === undefined) {
        // 旧 Core 单槽记录：删除该槽即移除记录（原语义）。
        if (inst.slot === cap.slot) {
          installations.splice(i, 1);
          installationReleaseById.delete(inst.id);
          installationVerifiedById.delete(inst.id);
        }
        continue;
      }
      if (!inst.boundSlots.includes(cap.slot)) continue;
      // 只解绑请求槽；其他槽的引用继续保护同一份物理文件。
      inst.boundSlots = inst.boundSlots.filter((entry) => entry !== cap.slot);
      if (inst.currentSlots !== undefined) {
        inst.currentSlots = inst.currentSlots.filter((entry) => entry !== cap.slot);
      }
      if (inst.boundSlots.length === 0 && (inst.currentSlots?.length ?? 0) === 0) {
        installations.splice(i, 1);
        installationReleaseById.delete(inst.id);
        installationVerifiedById.delete(inst.id);
      }
    }
    syncCapabilitySlots([cap.slot]);
    emit("installation.deleted", { data: { slot: cap.slot } });
  },

  subscribeEvents(cb, _onError, options) {
    // 模拟 SSE 的补读语义：先回放 afterSeq 之后的事件，再接收实时事件。
    const afterSeq = options?.afterSeq ?? 0;
    for (const event of eventLog) {
      if (event.seq > afterSeq) cb(event);
    }
    const subscription: MockSubscription = {
      cb,
      onConnected: options?.onConnected,
      active: true,
    };
    listeners.add(subscription);
    countCall(mockControl(), "eventSubscriptionCount");
    // 初连成功通知：与真实 SSE 一样异步到达，消费者只做合并只读刷新。
    queueMicrotask(() => notifyConnected(subscription));
    return () => {
      if (!subscription.active) return;
      subscription.active = false;
      listeners.delete(subscription);
      staleSubscriptions.push(subscription);
      if (staleSubscriptions.length > 8) staleSubscriptions.shift();
    };
  },
};

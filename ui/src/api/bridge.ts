// Electron 桥客户端：window.yaomu.api → YaoMuApi。
// 负责契约 snake_case ↔ UI camelCase 映射、错误信封展开与字段校验；页面只看到领域类型。
import { CAPABILITY_SLOTS } from "@/domain/capabilities";
import { isCapabilitySlotKey } from "@/domain/installations";
import type {
  Asset,
  AssetImportResult,
  AssetRole,
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
  EnvironmentCheck,
  EnvironmentInfo,
  ErrorInfo,
  EraseBlock,
  ExecutionLog,
  ExecutionLogLine,
  ExecutionLogStream,
  ExportFormat,
  ExportKind,
  ExportRecord,
  Installation,
  InstallationPlan,
  LicenseSummary,
  LocalApiSettings,
  LocalApiStats,
  LocalApiToken,
  MediaMeta,
  ModelSlotKey,
  NodeStatus,
  ParamField,
  PipelineNodeKey,
  PipelineNodeState,
  PreflightResult,
  Project,
  ProjectSnapshot,
  ProjectSummary,
  ReleaseRef,
  ReviewCurrentBinding,
  ReviewGate,
  ReviewType,
  RunParams,
  RunStatus,
  Segment,
  SegmentCandidateJob,
  SlotState,
  StorageCleanupCategory,
  StorageCleanupResult,
  StorageUsage,
  SubtitleRevision,
  SubtitleStyle,
  SubtitleTrack,
  SystemInfo,
  Task,
  TaskKind,
  TaskList,
  TaskStatus,
  TaskStep,
  WSLStatus,
  WorkflowRun,
} from "@/domain/types";
import {
  ApiError,
  coreNotReadyError,
  type ApproveReviewInput,
  type CreateWorkflowRunInput,
  type EventSubscriptionOptions,
  type ExportRequest,
  type GenerateCandidatesInput,
  type ImportAssetsInput,
  type InstallPlanRef,
  type ListTasksQuery,
  type SaveSubtitleInput,
  type SubtitleStyleInput,
  type YaoMuApi,
} from "./types";

type Wire = Record<string, unknown>;

// ---- 线上值校验 ----

function asRecord(value: unknown): Wire {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Wire)
    : {};
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function str(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function optStr(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function optBool(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function num(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function optNum(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** 非负整数百分比；缺失/非法保持 undefined，不把未知进度洗成 0。 */
function optProgress(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 100
    ? value
    : undefined;
}

function invalidResponseError(): ApiError {
  return new ApiError({
    code: "INVALID_RESPONSE",
    messageKey: "api.invalidResponse",
    retryable: false,
    actionIds: [],
  });
}

function requiredSlot(value: unknown): CapabilitySlotKey {
  if (!isCapabilitySlotKey(value)) throw invalidResponseError();
  return value;
}

/**
 * 槽位引用/覆盖数组的严格映射（contracts/shared-installations.md）：
 * 只有字段缺失（undefined）才代表旧 Core 的单槽兼容形状；显式数组必须
 * 全是已知槽位、无重复，按需非空并包含请求槽。非法形状直接报可读错误，
 * 不能静默 wash 成单槽后仍让用户确认。
 */
function mapSlotArray(
  value: unknown,
  options: { allowEmpty: boolean; requireSlot?: CapabilitySlotKey },
): CapabilitySlotKey[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value)) throw invalidResponseError();
  const slots = value.filter(isCapabilitySlotKey);
  if (slots.length !== value.length || new Set(slots).size !== slots.length) {
    throw invalidResponseError();
  }
  if (!options.allowEmpty && slots.length === 0) throw invalidResponseError();
  if (options.requireSlot && !slots.includes(options.requireSlot)) {
    throw invalidResponseError();
  }
  return slots;
}

/** 复用目标 ID：字段可缺省；一旦出现必须是非空字符串。 */
function optIdentifier(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string" || value.length === 0) throw invalidResponseError();
  return value;
}

/** 安全正整数；用于 step attempt/epoch 等身份字段，非法即未知。 */
function optPositiveInt(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0
    ? value
    : undefined;
}

function bool(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function stringArray(value: unknown): string[] {
  return asArray(value).filter((item): item is string => typeof item === "string");
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === "string" && (allowed as readonly string[]).includes(value)
    ? (value as T)
    : fallback;
}

const SLOT_STATES: readonly SlotState[] = [
  "UNKNOWN",
  "UNSUPPORTED",
  "UNVERIFIED",
  "NEEDS_SETUP",
  "INSTALLING",
  "VERIFYING",
  "READY",
  "NEEDS_ACTION",
  "RESTART_REQUIRED",
  "FAILED",
  "DEGRADED",
];

const RUN_STATUSES: readonly RunStatus[] = [
  "DRAFT",
  "QUEUED",
  "RUNNING",
  "PAUSE_REQUESTED",
  "PAUSED",
  "WAITING_REVIEW",
  "SUCCEEDED",
  "PARTIAL",
  "FAILED",
  "CANCELED",
];

const TASK_STATUSES: readonly TaskStatus[] = [
  "QUEUED",
  "WAITING_RESOURCE",
  "RUNNING",
  "PAUSE_REQUESTED",
  "PAUSED",
  "SUCCEEDED",
  "FAILED",
  "CANCEL_REQUESTED",
  "CANCELED",
  "INTERRUPTED",
];

const NODE_STATUSES: readonly NodeStatus[] = [
  "pending",
  "running",
  "waiting_review",
  "done",
  "skipped",
  "reused",
  "failed",
];

const TASK_KINDS: readonly TaskKind[] = ["node", "segment", "export"];

const INSTALLATION_PHASES = [
  "planning",
  "downloading",
  "verifying",
  "installing",
  "done",
  "failed",
  "canceled",
] as const;

const EXPORT_FORMATS: readonly ExportFormat[] = ["mp4_h264", "srt", "ass", "wav", "mp3"];

const EXPORT_KINDS: readonly ExportKind[] = ["video", "audio", "subtitle"];

const REVIEW_TYPES: readonly ReviewType[] = ["translation", "delivery"];
const REVIEW_BINDING_KINDS = [
  "translation_revision",
  "delivery_revision",
  "delivery_mix",
] as const;

const PARAM_TYPES = ["string", "number", "boolean", "enum"] as const;

const ASSET_TYPES = [
  "video",
  "audio",
  "subtitle",
  "audio_track",
  "image",
  "archive",
  "other",
] as const;

const ASSET_STATUSES = ["importing", "ready", "missing", "corrupt"] as const;

const ASSET_ROLES = [
  "original",
  "voice",
  "ambience",
  "clean_video",
  "source_subtitle",
  "translated_subtitle",
  "dub_audio",
  "full_dub_audio",
  "translated_video",
  "final_video",
] as const;

const ERASE_MODES = ["dialog_only", "dialog_and_art"] as const;
const EXTRACT_MODES = ["asr", "ocr", "asr_ocr"] as const;
const OCR_LANGUAGES = ["chinese", "chinese_traditional", "english", "japanese"] as const;

const TRACK_TYPES = ["transcription", "translation", "correction"] as const;

const DEVICE_KINDS = ["cpu", "cuda", "mps"] as const;

const ENVIRONMENT_PLATFORMS = ["windows", "darwin", "linux"] as const;

const STEP_STATUSES = [
  "PENDING",
  "RUNNING",
  "COMMITTING",
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
  "INTERRUPTED",
] as const;

const PREFLIGHT_SEVERITIES = ["pass", "warning", "blocker"] as const;

const SLOT_NAME_KEYS = new Map<string, string>(
  CAPABILITY_SLOTS.map((meta) => [meta.key, meta.nameKey]),
);

// ---- 响应映射（snake_case → camelCase） ----

function mapParamField(value: unknown): ParamField {
  const raw = asRecord(value);
  return {
    key: str(raw.key),
    labelKey: str(raw.label_key),
    type: oneOf(raw.type, PARAM_TYPES, "string"),
    options: asArray(raw.options).map((option) => {
      const item = asRecord(option);
      return { value: str(item.value), labelKey: str(item.label_key) };
    }),
    min: optNum(raw.min),
    max: optNum(raw.max),
    default: raw.default,
  };
}

function mapCapability(value: unknown, slot: CapabilitySlotKey, nameKey: string): Capability {
  const raw = asRecord(value);
  return {
    slot,
    nameKey,
    state: oneOf(raw.state, SLOT_STATES, "UNKNOWN"),
    engineName: optStr(raw.engine_name),
    version: optStr(raw.version),
    languages: Array.isArray(raw.languages) ? stringArray(raw.languages) : undefined,
    params: Array.isArray(raw.params) ? asArray(raw.params).map(mapParamField) : undefined,
    engines: Array.isArray(raw.engines)
      ? asArray(raw.engines).map((item) => {
          const engine = asRecord(item);
          return {
            id: str(engine.id),
            name: str(engine.name),
            version: optStr(engine.version),
            state: oneOf(engine.state, SLOT_STATES, "UNKNOWN"),
            installed: bool(engine.installed),
            selected: bool(engine.selected),
            device: optStr(engine.device),
            source: optStr(engine.source),
            sizeBytes: optNum(engine.size_bytes),
          };
        })
      : undefined,
    reasonCode: optStr(raw.reason_code),
    actionIds: stringArray(raw.action_ids),
    progress: optNum(raw.progress),
    runtimeState: raw.runtime_state === "running" ? "running" : "idle",
    runningTasks: optNum(raw.running_tasks),
    updateAvailable: bool(raw.update_available),
    updateVersion: optStr(raw.update_version),
    updateNotes: optStr(raw.update_notes),
  };
}

function mapCapabilities(value: unknown): Capability[] {
  const result: Capability[] = [];
  for (const item of asArray(value)) {
    const slot = str(asRecord(item).slot);
    const nameKey = SLOT_NAME_KEYS.get(slot);
    if (!nameKey) continue;
    result.push(mapCapability(item, slot as CapabilitySlotKey, nameKey));
  }
  return result;
}

function mapDevice(value: unknown): DeviceInfo {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    name: str(raw.name),
    kind: oneOf(raw.kind, DEVICE_KINDS, "cpu"),
    recommended: bool(raw.recommended),
    selected: bool(raw.selected),
    memoryTotalBytes: optNum(raw.memory_total_bytes),
    memoryUsedBytes: optNum(raw.memory_used_bytes),
    inUse: bool(raw.in_use),
    driverVersion: optStr(raw.driver_version),
  };
}

function mapWSLStatus(value: unknown): WSLStatus {
  const raw = asRecord(value);
  const version = raw.version === 1 || raw.version === 2 ? raw.version : undefined;
  return {
    available: bool(raw.available),
    version,
    cudaAvailable:
      raw.cuda_available === undefined ? undefined : bool(raw.cuda_available),
  };
}

function mapEnvironmentCheck(value: unknown): EnvironmentCheck {
  const raw = asRecord(value);
  return {
    code: str(raw.code),
    severity: oneOf(raw.severity, PREFLIGHT_SEVERITIES, "warning"),
    messageKey: str(raw.message_key),
    actionIds: stringArray(raw.action_ids),
  };
}

function mapEnvironment(value: unknown): EnvironmentInfo {
  const raw = asRecord(value);
  return {
    platform: oneOf(raw.platform, ENVIRONMENT_PLATFORMS, "linux"),
    wsl: raw.wsl === undefined || raw.wsl === null ? undefined : mapWSLStatus(raw.wsl),
    devices: asArray(raw.devices).map(mapDevice),
    checks: asArray(raw.checks).map(mapEnvironmentCheck),
  };
}

function mapStorageUsage(value: unknown): StorageUsage {
  const raw = asRecord(value);
  return {
    models: num(raw.models_bytes),
    media: num(raw.media_bytes),
    cache: num(raw.cache_bytes),
    exports: num(raw.exports_bytes),
  };
}

function mapStorageCleanupResult(value: unknown): StorageCleanupResult {
  const raw = asRecord(value);
  return {
    category: str(raw.category) as StorageCleanupCategory,
    freedBytes: num(raw.freed_bytes),
  };
}

function mapSystem(value: unknown): SystemInfo {
  const raw = asRecord(value);
  return {
    appId: str(raw.app_id),
    appVersion: optStr(raw.app_version),
    protocolVersion: str(raw.protocol_version),
    instanceId: str(raw.instance_id),
    workspaceId: str(raw.workspace_id),
    serverEpoch: num(raw.server_epoch),
    coreReady: bool(raw.core_ready),
  };
}

function mapErrorInfo(value: unknown): ErrorInfo | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const raw = asRecord(value);
  return {
    code: str(raw.code),
    messageKey: str(raw.message_key),
    retryable: bool(raw.retryable),
    actionIds: stringArray(raw.action_ids),
  };
}

function mapMediaMeta(value: unknown): MediaMeta | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const raw = asRecord(value);
  return {
    container: str(raw.container),
    codec: optStr(raw.codec),
    width: optNum(raw.width),
    height: optNum(raw.height),
    durationMs: num(raw.duration_ms),
    audioTracks: num(raw.audio_tracks),
    sampleRate: optNum(raw.sample_rate),
  };
}

function mapAsset(value: unknown): Asset {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    projectId: str(raw.project_id),
    type: oneOf(raw.type, ASSET_TYPES, "other"),
    role:
      typeof raw.role === "string" && (ASSET_ROLES as readonly string[]).includes(raw.role)
        ? (raw.role as AssetRole)
        : undefined,
    language: optStr(raw.language),
    runId: optStr(raw.run_id),
    name: str(raw.name),
    path: optStr(raw.path),
    sizeBytes: num(raw.size_bytes),
    sha256: optStr(raw.sha256),
    meta: mapMediaMeta(raw.meta),
    status: oneOf(raw.status, ASSET_STATUSES, "importing"),
    importedAt: str(raw.imported_at),
  };
}

function mapSegment(value: unknown): Segment {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    startMs: num(raw.start_ms),
    endMs: num(raw.end_ms),
    text: str(raw.text),
    speaker: optStr(raw.speaker),
    contentRevision: num(raw.content_revision),
    contentDigest: str(raw.content_digest),
    lineage: Array.isArray(raw.lineage) ? stringArray(raw.lineage) : undefined,
  };
}

function mapSubtitleStyleFromWire(value: unknown): SubtitleStyle | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const raw = value as Wire;
  const outline = typeof raw.outline === "object" && raw.outline !== null
    ? raw.outline as Wire
    : undefined;
  const shadow = typeof raw.shadow === "object" && raw.shadow !== null
    ? raw.shadow as Wire
    : undefined;
  const align = raw.align;
  return {
    x: optNum(raw.x),
    y: optNum(raw.y),
    font: optStr(raw.font),
    fontSize: optNum(raw.font_size),
    lineSpacing: optNum(raw.line_spacing),
    bold: optBool(raw.bold),
    align: align === "left" || align === "center" || align === "right" ? align : undefined,
    outline: outline
      ? { color: optStr(outline.color), width: optNum(outline.width) }
      : undefined,
    shadow: shadow
      ? {
          color: optStr(shadow.color),
          blur: optNum(shadow.blur),
          x: optNum(shadow.x),
          y: optNum(shadow.y),
        }
      : undefined,
    background: optStr(raw.background),
    maxTranslationLines: optNum(raw.max_translation_lines),
  };
}

function mapEraseBlockFromWire(value: unknown): EraseBlock | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const raw = value as Wire;
  const bbox = asArray(raw.bbox).map((item) => num(item));
  if (bbox.length !== 4) return undefined;
  return {
    id: optStr(raw.id),
    segmentId: optStr(raw.segment_id),
    startMs: num(raw.start_ms),
    endMs: num(raw.end_ms),
    bbox: [bbox[0], bbox[1], bbox[2], bbox[3]],
    source: raw.source === "clean" || raw.source === "source" ? raw.source : undefined,
  };
}

function mapEraseBlocksFromWire(value: unknown): EraseBlock[] | undefined {
  if (!Array.isArray(value)) return undefined;
  return value
    .map(mapEraseBlockFromWire)
    .filter((block): block is EraseBlock => block !== undefined);
}

function mapRevision(value: unknown): SubtitleRevision {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    trackId: str(raw.track_id),
    parentRevisionId: optStr(raw.parent_revision_id),
    authorType: raw.author_type === "user" ? "user" : "system",
    segments: asArray(raw.segments).map(mapSegment),
    inputRevisionId: optStr(raw.input_revision_id),
    style: mapSubtitleStyleFromWire(raw.style),
    eraseBlocks: mapEraseBlocksFromWire(raw.erase_blocks),
    revisionDigest: optStr(raw.revision_digest),
    createdAt: str(raw.created_at),
  };
}

function mapTrack(value: unknown): SubtitleTrack {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    projectId: str(raw.project_id),
    language: str(raw.language),
    type: oneOf(raw.type, TRACK_TYPES, "transcription"),
    currentRevisionId: str(raw.current_revision_id),
    revisions: asArray(raw.revisions).map(mapRevision),
  };
}

function mapNodeState(value: unknown): PipelineNodeState {
  const raw = asRecord(value);
  return {
    key: str(raw.key) as PipelineNodeKey,
    status: oneOf(raw.status, NODE_STATUSES, "pending"),
    progress: num(raw.progress),
    detail: optStr(raw.detail),
  };
}

function mapRunParams(value: unknown): RunParams | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const raw = asRecord(value);
  const params: RunParams = {
    eraseMode:
      raw.erase_mode === undefined
        ? undefined
        : oneOf(raw.erase_mode, ERASE_MODES, "dialog_only"),
    validateTimestamps: optBool(raw.validate_timestamps),
    adjustDuration: optBool(raw.adjust_duration),
    bilingual: optBool(raw.bilingual),
    extractMode:
      raw.extract_mode === undefined ? undefined : oneOf(raw.extract_mode, EXTRACT_MODES, "asr"),
    ocrLanguage:
      raw.ocr_language === undefined
        ? undefined
        : oneOf(raw.ocr_language, OCR_LANGUAGES, "chinese"),
    asrLanguage: optStr(raw.asr_language),
    subtitleStyle: mapSubtitleStyleFromWire(raw.subtitle_style),
  };
  return Object.values(params).some((item) => item !== undefined) ? params : undefined;
}

function mapRun(value: unknown): WorkflowRun {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    projectId: str(raw.project_id),
    name: str(raw.name),
    template: str(raw.template, "full_pipeline"),
    modeType: optStr(raw.mode_type),
    status: oneOf(raw.status, RUN_STATUSES, "DRAFT"),
    sourceLanguage: str(raw.source_language),
    languages: stringArray(raw.languages),
    inputAssetIds: stringArray(raw.input_asset_ids),
    nodes: asArray(raw.nodes).map(mapNodeState),
    params: mapRunParams(raw.params),
    createdAt: str(raw.created_at),
    updatedAt: str(raw.updated_at),
  };
}

function mapProjectSummary(value: unknown): ProjectSummary {
  const raw = asRecord(value);
  return {
    seq: optNum(raw.seq),
    id: str(raw.id),
    name: str(raw.name),
    editVersion: num(raw.edit_version),
    episodeId: optStr(raw.episode_id),
    modeType: optStr(raw.mode_type),
    createdAt: str(raw.created_at),
    updatedAt: str(raw.updated_at),
    lastOpenedAt: optStr(raw.last_opened_at),
    assetCount: num(raw.asset_count),
    runCount: num(raw.run_count),
    latestRunStatus:
      raw.latest_run_status === undefined || raw.latest_run_status === null
        ? undefined
        : oneOf(raw.latest_run_status, RUN_STATUSES, "DRAFT"),
    totalDurationMs: optNum(raw.total_duration_ms),
    waitingResource: raw.waiting_resource === true ? true : undefined,
  };
}

function mapProject(value: unknown): Project {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    workspaceId: str(raw.workspace_id),
    episodeId: optStr(raw.episode_id),
    name: str(raw.name),
    directory: str(raw.directory),
    schemaVersion: num(raw.schema_version),
    editVersion: num(raw.edit_version),
    createdAt: str(raw.created_at),
    updatedAt: str(raw.updated_at),
    lastOpenedAt: optStr(raw.last_opened_at),
    assets: asArray(raw.assets).map(mapAsset),
    tracks: asArray(raw.tracks).map(mapTrack),
    runs: asArray(raw.runs).map(mapRun),
  };
}

function mapTaskStep(value: unknown): TaskStep {
  const raw = asRecord(value);
  return {
    stepId: str(raw.step_id),
    status: oneOf(raw.status, STEP_STATUSES, "PENDING"),
    attemptNo: optPositiveInt(raw.attempt_no),
    epoch: optPositiveInt(raw.epoch),
  };
}

function mapTask(value: unknown): Task {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    projectId: str(raw.project_id),
    runId: optStr(raw.run_id),
    kind: oneOf(raw.kind, TASK_KINDS, "node"),
    nodeKey: optStr(raw.node_key) as PipelineNodeKey | undefined,
    segmentId: optStr(raw.segment_id),
    language: optStr(raw.language),
    label: str(raw.label),
    status: oneOf(raw.status, TASK_STATUSES, "QUEUED"),
    progress: optProgress(raw.progress),
    steps: Array.isArray(raw.steps) ? asArray(raw.steps).map(mapTaskStep) : undefined,
    artifactIds: stringArray(raw.artifact_ids),
    error: mapErrorInfo(raw.error),
    createdAt: str(raw.created_at),
    updatedAt: str(raw.updated_at),
  };
}

function mapTaskList(value: unknown): TaskList {
  const raw = asRecord(value);
  return {
    items: asArray(raw.items).map(mapTask),
    nextCursor: optStr(raw.next_cursor),
  };
}

function mapPreflight(value: unknown): PreflightResult {
  const raw = asRecord(value);
  return {
    planId: optStr(raw.plan_id),
    planHash: optStr(raw.plan_hash),
    issues: asArray(raw.issues).map((item) => {
      const issue = asRecord(item);
      return {
        severity: oneOf(issue.severity, PREFLIGHT_SEVERITIES, "warning"),
        code: str(issue.code),
        messageKey: str(issue.message_key),
        actionIds: stringArray(issue.action_ids),
      };
    }),
  };
}

function mapReviewBinding(value: unknown): ReviewCurrentBinding | null {
  const raw = asRecord(value);
  const language = optStr(raw.language);
  const sourceId = optStr(raw.source_id);
  if (!language || !sourceId) return null;
  const sourceKind = REVIEW_BINDING_KINDS.find((kind) => kind === raw.source_kind);
  if (!sourceKind) return null;
  return { language, sourceKind, sourceId };
}

function mapReview(value: unknown): ReviewGate {
  const raw = asRecord(value);
  // 缺省（旧服务）保持 undefined=未知；空数组保持空数组=无当前绑定。不能互相替代。
  const currentBindings = Array.isArray(raw.current_bindings)
    ? raw.current_bindings
        .map(mapReviewBinding)
        .filter((binding): binding is ReviewCurrentBinding => binding !== null)
    : undefined;
  return {
    id: str(raw.id),
    runId: str(raw.run_id),
    type: oneOf(raw.type, REVIEW_TYPES, "translation"),
    language: optStr(raw.language),
    revisionDigest: str(raw.revision_digest),
    status: raw.status === "approved" ? "approved" : "pending",
    confirmedAt: optStr(raw.confirmed_at),
    currentBindings,
  };
}

function mapCandidate(value: unknown): AudioCandidate {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    segmentId: str(raw.segment_id),
    language: str(raw.language),
    revision: num(raw.revision),
    modelVersion: str(raw.model_version),
    audioAssetId: str(raw.audio_asset_id),
    isRecommended: bool(raw.is_recommended),
    createdAt: str(raw.created_at),
  };
}

function mapSelection(value: unknown): CandidateSelection {
  const raw = asRecord(value);
  return {
    segmentId: str(raw.segment_id),
    language: str(raw.language),
    candidateId: str(raw.candidate_id),
    revision: num(raw.revision),
    selectedAt: str(raw.selected_at),
  };
}

function mapEngineConfig(value: unknown): EngineConfig {
  const raw = asRecord(value);
  const values =
    typeof raw.values === "object" && raw.values !== null && !Array.isArray(raw.values)
      ? (raw.values as Record<string, unknown>)
      : undefined;
  return {
    slot: str(raw.slot) as ModelSlotKey,
    engineId: optStr(raw.engine_id),
    deviceId: optStr(raw.device_id),
    values,
    updatedAt: optStr(raw.updated_at),
  };
}

function mapSnapshot(value: unknown): ProjectSnapshot {
  const raw = asRecord(value);
  return {
    projectId: str(raw.project_id),
    project: mapProjectSummary(raw.project),
    runs: asArray(raw.runs).map(mapRun),
    tasks: asArray(raw.tasks).map(mapTask),
    eventSeq: num(raw.event_seq),
  };
}

function mapCandidateJob(value: unknown): SegmentCandidateJob {
  const raw = asRecord(value);
  return {
    taskId: str(raw.task_id),
    segmentId: str(raw.segment_id),
    language: str(raw.language),
    attemptNo: num(raw.attempt_no),
  };
}

function mapExport(value: unknown): ExportRecord {
  const raw = asRecord(value);
  return {
    id: str(raw.id),
    projectId: str(raw.project_id),
    runId: optStr(raw.run_id),
    kind: oneOf(raw.kind, EXPORT_KINDS, "video"),
    format: oneOf(raw.format, EXPORT_FORMATS, "mp4_h264"),
    status: oneOf(raw.status, TASK_STATUSES, "QUEUED"),
    outputPath: optStr(raw.output_path),
    createdAt: str(raw.created_at),
  };
}

function mapRelease(value: unknown): ReleaseRef {
  const raw = asRecord(value);
  return {
    releaseId: str(raw.release_id),
    version: str(raw.version),
    digest: str(raw.digest),
    channel: optStr(raw.channel),
  };
}

function mapLicense(value: unknown): LicenseSummary {
  const raw = asRecord(value);
  return {
    licenseId: str(raw.license_id),
    licenseName: str(raw.license_name),
    noticeKeys: Array.isArray(raw.notice_keys) ? stringArray(raw.notice_keys) : undefined,
  };
}

function mapInstallationPlan(value: unknown, requestedSlot?: string): InstallationPlan {
  const raw = asRecord(value);
  const slot = requiredSlot(raw.slot);
  // 计划必须对应当前请求的入口槽；否则会把未请求槽的范围当成同意范围。
  if (requestedSlot !== undefined && slot !== requestedSlot) throw invalidResponseError();
  return {
    planId: str(raw.plan_id),
    planHash: str(raw.plan_hash),
    slot,
    coveredSlots: mapSlotArray(raw.covered_slots, { allowEmpty: false, requireSlot: slot }),
    reuseInstallationId: optIdentifier(raw.reuse_installation_id),
    release: mapRelease(raw.release),
    downloadBytes: num(raw.download_bytes),
    cacheReuseBytes: num(raw.cache_reuse_bytes),
    installBytes: num(raw.install_bytes),
    peakStagingBytes: optNum(raw.peak_staging_bytes),
    permissions: stringArray(raw.permissions),
    license: mapLicense(raw.license),
    requiresRestart: raw.requires_restart === undefined ? undefined : bool(raw.requires_restart),
  };
}

function mapInstallation(value: unknown): Installation {
  const raw = asRecord(value);
  const slot = requiredSlot(raw.slot);
  return {
    id: str(raw.id),
    slot,
    // 显式空数组保持空（不回退发起槽）；只有字段缺失才是旧 Core 单槽语义。
    coveredSlots: mapSlotArray(raw.covered_slots, { allowEmpty: false, requireSlot: slot }),
    boundSlots: mapSlotArray(raw.bound_slots, { allowEmpty: true }),
    currentSlots: mapSlotArray(raw.current_slots, { allowEmpty: true }),
    phase: oneOf(raw.phase, INSTALLATION_PHASES, "planning"),
    progress: num(raw.progress),
    verifiedFiles: optNum(raw.verified_files),
    error: mapErrorInfo(raw.error),
    retryable: raw.retryable === undefined ? undefined : bool(raw.retryable),
    planHash: optStr(raw.plan_hash),
  };
}

function mapLocalApiStats(value: unknown): LocalApiStats {
  const raw = asRecord(value);
  return {
    enabled: bool(raw.enabled),
    totalCalls: num(raw.total_calls),
    byEndpoint: asArray(raw.by_endpoint).map((item) => {
      const entry = asRecord(item);
      return {
        endpoint: str(entry.endpoint),
        calls: num(entry.calls),
        lastError: optStr(entry.last_error),
      };
    }),
  };
}

function mapLocalApiSettings(value: unknown): LocalApiSettings {
  const raw = asRecord(value);
  return {
    enabled: bool(raw.enabled),
    concurrencyLimit: num(raw.concurrency_limit),
    tokenPresent: bool(raw.token_present),
    tokenMasked: optStr(raw.token_masked),
  };
}

function mapLocalApiToken(value: unknown): LocalApiToken {
  const raw = asRecord(value);
  return { token: str(raw.token), issuedAt: str(raw.issued_at) };
}

const EXECUTION_LOG_STREAMS: readonly ExecutionLogStream[] = ["stderr", "stdout", "runner"];

function mapExecutionLogLine(value: unknown): ExecutionLogLine {
  const raw = asRecord(value);
  return {
    seq: num(raw.seq),
    stream: oneOf(raw.stream, EXECUTION_LOG_STREAMS, "runner"),
    line: str(raw.line),
    at: optStr(raw.at),
  };
}

function mapExecutionLog(value: unknown): ExecutionLog {
  const raw = asRecord(value);
  return {
    taskId: str(raw.task_id),
    attempt: num(raw.attempt),
    lines: asArray(raw.lines).map(mapExecutionLogLine),
  };
}

function mapEvent(value: unknown): DomainEvent {
  const raw = asRecord(value);
  return {
    // 缺失/非法 schema/seq 不得默认成合法值：进度消费者只接受严格 seq=0/schema=1。
    schemaVersion: optNum(raw.schema_version) ?? Number.NaN,
    seq: optNum(raw.seq) ?? Number.NaN,
    type: str(raw.type),
    projectId: optStr(raw.project_id),
    taskId: optStr(raw.task_id),
    stateVersion: optNum(raw.state_version),
    data: raw.data,
  };
}

// ---- 请求映射（camelCase → snake_case） ----

function mapSubtitleStyle(style: SubtitleStyleInput): Wire {
  return {
    x: style.x,
    y: style.y,
    font: style.font,
    font_size: style.fontSize,
    line_spacing: style.lineSpacing,
    bold: style.bold,
    align: style.align,
    outline: style.outline
      ? { color: style.outline.color, width: style.outline.width }
      : undefined,
    shadow: style.shadow
      ? {
          color: style.shadow.color,
          blur: style.shadow.blur,
          x: style.shadow.x,
          y: style.shadow.y,
        }
      : undefined,
    background: style.background,
    max_translation_lines: style.maxTranslationLines,
  };
}

const CUSTOM_FEATURES: readonly CustomPipelineFeature[] = [
  "separation",
  "erase",
  "translate",
  "clone",
  "mix",
];

function isCustomFeature(value: string): value is CustomPipelineFeature {
  return (CUSTOM_FEATURES as readonly string[]).includes(value);
}

function mapCustomTemplate(value: unknown): CustomPipelineTemplate {
  const raw = asRecord(value);
  const eraseMode = optStr(raw.erase_mode);
  const ocrLanguage = optStr(raw.ocr_language);
  return {
    id: str(raw.id),
    name: str(raw.name),
    features: stringArray(raw.features).filter(isCustomFeature),
    languages: stringArray(raw.languages),
    sourceLanguage: str(raw.source_language),
    eraseMode:
      eraseMode === "dialog_only" || eraseMode === "dialog_and_art" ? eraseMode : undefined,
    ocrLanguage:
      ocrLanguage === "chinese" ||
      ocrLanguage === "chinese_traditional" ||
      ocrLanguage === "english" ||
      ocrLanguage === "japanese"
        ? ocrLanguage
        : undefined,
    validateTimestamps: bool(raw.validate_timestamps),
    adjustDuration: bool(raw.adjust_duration),
    createdAt: str(raw.created_at),
    updatedAt: str(raw.updated_at),
  };
}

function mapCustomTemplateList(value: unknown): CustomPipelineTemplateList {
  return { items: asArray(asRecord(value).items).map(mapCustomTemplate) };
}

function customTemplateToWire(item: CustomPipelineTemplate): Wire {
  return {
    id: item.id,
    name: item.name,
    features: item.features,
    languages: item.languages,
    source_language: item.sourceLanguage,
    erase_mode: item.eraseMode,
    ocr_language: item.ocrLanguage,
    validate_timestamps: item.validateTimestamps,
    adjust_duration: item.adjustDuration,
    created_at: item.createdAt,
    updated_at: item.updatedAt,
  };
}

function runInputToWire(input: CreateWorkflowRunInput): Wire {
  return {
    project_id: input.projectId,
    name: input.name,
    template: input.template,
    features: input.features,
    source_language: input.sourceLanguage,
    target_languages: input.targetLanguages,
    input_revision_ids: input.inputRevisionIds,
    extract: input.extract
      ? {
          mode: input.extract.mode,
          ocr_language: input.extract.ocrLanguage,
          ocr_quality: input.extract.ocrQuality,
        }
      : undefined,
    reuse_audio_track_id: input.reuseAudioTrackId,
    reuse_subtitle_track_id: input.reuseSubtitleTrackId,
    erase_mode: input.eraseMode,
    validate_timestamps: input.validateTimestamps,
    adjust_duration: input.adjustDuration,
    bilingual: input.bilingual,
    subtitle_style: input.subtitleStyle ? mapSubtitleStyle(input.subtitleStyle) : undefined,
  };
}

function importInputToWire(input: ImportAssetsInput): Wire {
  return {
    project_id: input.projectId,
    file_grants: input.paths,
    copy_mode: input.copyMode,
  };
}

function mapEraseBlockToWire(block: EraseBlock): Wire {
  return {
    id: block.id,
    segment_id: block.segmentId,
    start_ms: block.startMs,
    end_ms: block.endMs,
    // 复制成普通数组：Vue reactive 代理不能经 Electron IPC structured clone。
    bbox: [...block.bbox],
    source: block.source,
  };
}

function saveSubtitleToWire(input: SaveSubtitleInput): Wire {
  return {
    base_revision_id: input.baseRevisionId,
    // content_revision / content_digest 由 Core 计算，客户端不提交。
    segments: input.segments.map((segment) => ({
      id: segment.id,
      start_ms: segment.startMs,
      end_ms: segment.endMs,
      text: segment.text,
      speaker: segment.speaker,
      // 同 bbox：lineage 可能是 reactive 代理数组，IPC 序列化前复制。
      lineage: segment.lineage ? [...segment.lineage] : undefined,
    })),
    // 整轨替换：页面必须回传当前 revision 已有的样式/擦除块，否则 Core 会清空。
    style: input.style ? mapSubtitleStyle(input.style) : undefined,
    erase_blocks: input.eraseBlocks?.map(mapEraseBlockToWire),
  };
}

function tasksQueryToWire(query?: ListTasksQuery): Wire | undefined {
  if (!query) return undefined;
  return {
    project_id: query.projectId,
    run_id: query.runId,
    status: query.status,
    cursor: query.cursor,
    limit: query.limit,
  };
}

function exportToWire(input: ExportRequest): Wire {
  return {
    project_id: input.projectId,
    run_id: input.runId,
    language: input.language,
    kind: input.kind,
    format: input.format,
    input_revision_id: input.inputRevisionId,
    bilingual: input.bilingual,
    output_dir_grant: input.outputDir,
    naming_template: input.namingTemplate,
  };
}

function localApiSettingsToWire(input: {
  enabled?: boolean;
  concurrencyLimit?: number;
}): Wire {
  return {
    enabled: input.enabled,
    concurrency_limit: input.concurrencyLimit,
  };
}

function engineConfigToWire(input: {
  engineId?: string;
  deviceId?: string;
  values?: Record<string, unknown>;
}): Wire {
  return {
    engine_id: input.engineId,
    device_id: input.deviceId,
    values: input.values,
  };
}

// ---- 调用与错误展开 ----

async function call<T>(pending: Promise<YaomuBridgeResult<unknown>>): Promise<T> {
  let result: YaomuBridgeResult<unknown>;
  try {
    result = await pending;
  } catch {
    throw coreNotReadyError();
  }
  if (!result.ok) throw new ApiError(result.error);
  return result.data as T;
}

export function createBridgeApi(bridge: YaomuBridgeApi): YaoMuApi {
  return {
    async getSystem() {
      return mapSystem(await call(bridge.getSystem()));
    },
    async getCapabilities() {
      return mapCapabilities(await call(bridge.getCapabilities()));
    },
    async getDevices() {
      return asArray(await call(bridge.getDevices())).map(mapDevice);
    },
    async getEnvironment(options) {
      return mapEnvironment(await call(bridge.getEnvironment(options?.refresh === true)));
    },
    async getStorageUsage() {
      return mapStorageUsage(await call(bridge.getStorageUsage()));
    },
    async cleanupStorage(category) {
      const raw = await call(bridge.cleanupStorage({ category }));
      return mapStorageCleanupResult(raw);
    },
    async getLocalApiStats() {
      return mapLocalApiStats(await call(bridge.getLocalApiStats()));
    },
    async getLocalApiSettings() {
      return mapLocalApiSettings(await call(bridge.getLocalApiSettings()));
    },
    async updateLocalApiSettings(input) {
      return mapLocalApiSettings(
        await call(bridge.updateLocalApiSettings(localApiSettingsToWire(input))),
      );
    },
    async issueLocalApiToken() {
      return mapLocalApiToken(await call(bridge.issueLocalApiToken()));
    },
    async revokeLocalApiToken() {
      await call(bridge.revokeLocalApiToken());
    },
    async getEngineConfig(slot) {
      return mapEngineConfig(await call(bridge.getEngineConfig(slot)));
    },
    async setEngineConfig(slot, config) {
      return mapEngineConfig(await call(bridge.setEngineConfig(slot, engineConfigToWire(config))));
    },

    async listProjects() {
      return asArray(await call(bridge.listProjects())).map(mapProjectSummary);
    },
    async getProject(id) {
      return mapProject(await call(bridge.getProject(id)));
    },
    async createProject(input) {
      return mapProject(await call(bridge.createProject({ name: input.name })));
    },
    async updateProject(id, input) {
      return mapProject(
        await call(
          bridge.updateProject(id, {
            name: input.name,
            edit_version: input.editVersion,
          }),
        ),
      );
    },
    async deleteProject(id) {
      await call(bridge.deleteProject(id));
    },
    async importAssets(input) {
      const result = asRecord(await call(bridge.importAssets(importInputToWire(input))));
      return {
        assetIds: stringArray(result.asset_ids),
        taskId: optStr(result.task_id),
      };
    },
    async relocateAsset(assetId, path) {
      return mapAsset(await call(bridge.relocateAsset(assetId, { file_grant: path })));
    },

    async listTasks(query) {
      return mapTaskList(await call(bridge.listTasks(tasksQueryToWire(query))));
    },
    async getTask(id) {
      return mapTask(await call(bridge.getTask(id)));
    },
    async cancelTask(id) {
      await call(bridge.cancelTask(id));
    },
    async retryTask(id) {
      return mapTask(await call(bridge.retryTask(id)));
    },
    async getRun(runId) {
      return mapRun(await call(bridge.getRun(runId)));
    },
    async pauseRun(id) {
      return mapRun(await call(bridge.pauseRun(id)));
    },
    async resumeRun(id) {
      return mapRun(await call(bridge.resumeRun(id)));
    },
    async preflight(input) {
      return mapPreflight(await call(bridge.preflight(runInputToWire(input))));
    },
    async createWorkflowRun(input) {
      return mapRun(await call(bridge.createWorkflowRun(runInputToWire(input))));
    },
    async getSnapshot(projectId) {
      return mapSnapshot(await call(bridge.getSnapshot(projectId)));
    },

    async getExecutionLog(taskId, options) {
      return mapExecutionLog(
        await call(
          bridge.getExecutionLog(taskId, {
            attempt: options?.attempt,
            tail: options?.tail,
          }),
        ),
      );
    },

    async saveSubtitle(input) {
      return mapRevision(
        await call(bridge.saveSubtitle(input.trackId, saveSubtitleToWire(input))),
      );
    },
    async generateCandidates(input: GenerateCandidatesInput) {
      return mapCandidateJob(
        await call(bridge.generateCandidates(input.segmentId, { language: input.language })),
      );
    },
    async listCandidates(segmentId, language) {
      return asArray(await call(bridge.listCandidates(segmentId, language))).map(mapCandidate);
    },
    async selectCandidate(segmentId, language, candidateId) {
      return mapSelection(
        await call(
          bridge.selectCandidate(segmentId, { language, candidate_id: candidateId }),
        ),
      );
    },
    async getSelection(segmentId, language) {
      try {
        return mapSelection(await call(bridge.getSelection(segmentId, language)));
      } catch (err) {
        // 尚无选择时 Core 返回 404 NOT_FOUND；对 UI 是正常空状态，不是错误。
        if (err instanceof ApiError && (err.code === "NOT_FOUND" || err.status === 404)) {
          return null;
        }
        throw err;
      }
    },
    async listReviews(projectId, runId) {
      return asArray(await call(bridge.listReviews(projectId, runId))).map(mapReview);
    },
    async getReview(reviewId) {
      return mapReview(await call(bridge.getReview(reviewId)));
    },
    async approveReview(input: ApproveReviewInput) {
      const body: Wire = {
        revision_digest: input.revisionDigest,
        type: input.type,
      };
      if (input.currentBinding) {
        body.current_binding = {
          language: input.currentBinding.language,
          source_kind: input.currentBinding.sourceKind,
          source_id: input.currentBinding.sourceId,
        };
      }
      return mapReview(await call(bridge.approveReview(input.reviewId, body)));
    },

    async export(input) {
      return mapExport(await call(bridge.export(exportToWire(input))));
    },
    async listExports(projectId) {
      return asArray(await call(bridge.listExports(projectId))).map(mapExport);
    },
    async getInstallations() {
      return asArray(await call(bridge.getInstallations())).map(mapInstallation);
    },
    async planInstallation(slot) {
      // 用实际请求的槽位校验回包，不能只信 raw.slot 自洽。
      return mapInstallationPlan(await call(bridge.planInstallation(slot)), slot);
    },
    async install(input: InstallPlanRef) {
      return mapInstallation(
        await call(bridge.install({ plan_id: input.planId, plan_hash: input.planHash })),
      );
    },
    async cancelInstallation(id) {
      return mapInstallation(await call(bridge.cancelInstallation(id)));
    },
    async rollbackInstallation(slot) {
      return mapInstallation(await call(bridge.rollbackInstallation(slot)));
    },
    async deleteInstallation(slot) {
      await call(bridge.deleteInstallation(slot));
    },

    async saveAsset(input) {
      let result: unknown;
      try {
        result = await bridge.saveAsset({
          assetId: input.assetId,
          suggestedName: input.suggestedName,
          title: input.title,
        });
      } catch {
        throw new ApiError({
          code: "FILE_SAVE_FAILED",
          messageKey: "api.fileSaveFailed",
          retryable: true,
          actionIds: [],
        });
      }
      if (result === null || result === undefined) return null;
      const path = str(asRecord(result).path);
      if (!path) {
        throw new ApiError({
          code: "INVALID_RESPONSE",
          messageKey: "api.invalidResponse",
          retryable: false,
          actionIds: [],
        });
      }
      return { path };
    },

    async importSubtitleTrack(input) {
      return mapTrack(
        await call(
          bridge.importSubtitleTrack({
            project_id: input.projectId,
            asset_id: input.assetId,
            language: input.language,
            type: input.type,
          }),
        ),
      );
    },

    async listCustomPipelineTemplates() {
      return mapCustomTemplateList(await call(bridge.listCustomPipelineTemplates()));
    },

    async saveCustomPipelineTemplates(items) {
      return mapCustomTemplateList(
        await call(bridge.saveCustomPipelineTemplates({ items: items.map(customTemplateToWire) })),
      );
    },

    subscribeEvents(cb, onError, options?: EventSubscriptionOptions) {
      return bridge.subscribeEvents((event) => cb(mapEvent(event)), onError, options);
    },
  };
}

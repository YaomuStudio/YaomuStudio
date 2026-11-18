<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { getLocale, t, tCount, formatDuration, formatTimestamp } from "@/i18n";
import { dedupePreflightIssues, errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import {
  countNotice,
  errorNotice,
  messageNotice,
  noticeList,
  renderNotice,
  type SafeNotice,
} from "@/i18n/notice";
import { api } from "@/api";
import type { CreateWorkflowRunInput } from "@/api/types";
import type {
  AudioCandidate,
  ExportFormat,
  ExportKind,
  ExportRecord,
  ExtractOptions,
  PipelineNodeKey,
  PipelineNodeState,
  PreflightIssue,
  Project,
  ProjectSummary,
  ReviewCurrentBinding,
  ReviewGate,
  Segment,
  SubtitleRevision,
  SubtitleTrack,
  Task,
  WorkflowRun,
} from "@/domain/types";
import { PIPELINE_NODE_KEYS } from "@/domain/capabilities";
import { ASR_LANGUAGES, OCR_LANGUAGES, TARGET_LANGUAGES, languageName, targetLanguageName } from "@/domain/languages";
import {
  nodeStatusLabelKey,
  nodeStatusTone,
  runStatusLabelKey,
  runStatusTone,
  type Tone,
} from "@/domain/status";
import type {
  EditorRow,
  EraseMode,
  ExtractMode,
  ResultTabKey,
  SegmentActionState,
  SelectOption,
  StepKey,
  TimeDraft,
  TrackChoice,
} from "./types";
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CapabilityAccessNotice from "@/components/layout/CapabilityAccessNotice.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import AssetPlayer from "@/components/media/AssetPlayer.vue";
import type { PlayerSource } from "@/components/media/types";
import RunOptionsSummary from "@/components/RunOptionsSummary.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";
import YmStepper from "@/components/ui/YmStepper.vue";
import YmTabs from "@/components/ui/YmTabs.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import ImportStep from "./ImportStep.vue";
import ConfigStep from "./ConfigStep.vue";
import PipelineNodes from "./PipelineNodes.vue";
import SubtitleEditor from "./SubtitleEditor.vue";
import CandidatePanel from "./CandidatePanel.vue";
import DeliveryPanel from "./DeliveryPanel.vue";
import ExportPanel from "./ExportPanel.vue";
import { useDefaultOutputDir } from "@/composables/useDefaultOutputDir";
import { rememberTargetLanguages } from "@/composables/useRecentTargetLanguages";
import { deliveryReviewView, translationReviewView } from "./reviewBinding";
import { useTasksStore } from "@/stores/tasks";
import { useRuntimeStore } from "@/stores/runtime";
import { createReadVersion } from "@/composables/readGeneration";

// Electron 桥存在时文件路径由主进程 dialog / webUtils 授权，Renderer 不直接猜 File.path。
const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;

/** FR-IMP-02 支持的媒体扩展名；与 desktop/src/media-scan.ts 的默认列表一致。 */
const MEDIA_EXTENSIONS = ["mp4", "mov", "mkv", "webm", "wav", "mp3", "m4a", "flac", "aac"];
/** 单次文件夹导入上限，与 Core 的 maxImportGrants 一致。 */
const MAX_FOLDER_FILES = 128;

const route = useRoute();
const router = useRouter();
const tasks = useTasksStore();
const runtime = useRuntimeStore();
/** 项目读取代次/序号：旧 generation 或更早请求的晚回包不得 hydrate 当前草稿。 */
const projectReads = createReadVersion();

// ---- 项目绑定（不再硬编码项目 ID；支持 ?project= 直达） ----
const projectId = ref("");
const projectOptions = ref<ProjectSummary[]>([]);
const projectsLoading = ref(false);
const creatingProject = ref(false);
const newProjectName = ref("");

// ---- 页面/加载状态 ----
const loading = ref(true);
const loadError = ref<SafeNotice | null>(null);
const loadErrorText = computed(() => renderNotice(loadError.value));
const project = ref<Project | null>(null);
const busy = ref(false);
const importing = ref(false);
type NoticeTone = "info" | "success" | "error";
const notice = ref<SafeNotice | null>(null);
const noticeText = computed(() => renderNotice(notice.value));
const noticeTone = ref<NoticeTone>("info");
let noticeTimer: ReturnType<typeof setTimeout> | null = null;
/** 宿主重连时存在草稿/写入：保留本地状态并提示，不自动保存或重新创建。 */
const recoveryNotice = ref(false);
/** 显式保存 PUT 成功但刷新未应用：提示需重新读取，不自动重放 PUT。 */
const saveRefreshPending = ref(false);

// ---- 三步流程 ----
const step = ref<StepKey>("import");
const resultTab = ref<ResultTabKey>("editor");
const activeRunId = ref<string | null>(null);

// ---- 步骤一：导入/提取 ----
const sourceLanguage = ref("zh");
const extractMode = ref<ExtractMode>("asr");
const ocrLanguage = ref("chinese");
const copyToProject = ref(true);

// ---- 步骤二：译制配置 ----
const projectName = ref("");
const targetLanguages = ref<string[]>([]);
const reuseAudio = ref(false);
const reuseSubtitle = ref(false);
const eraseMode = ref<EraseMode>("dialog_only");
const validateTimestamps = ref(false);
const adjustDuration = ref(false);
const bilingual = ref(false);

// ---- 步骤三：字幕校正 ----
const translationSegments = ref<Segment[]>([]);
const baseline = ref<Record<string, string>>({});
const drafts = reactive<Record<string, TimeDraft>>({});
const selectedSegmentId = ref<string | null>(null);

// ---- 步骤三：配音候选 ----
const candidatesBySegment = ref<Record<string, AudioCandidate[]>>({});
const candidateLoading = ref(false);
const candidateError = ref<SafeNotice | null>(null);
const candidateErrorText = computed(() => renderNotice(candidateError.value));
const selectedCandidates = ref<Record<string, string>>({});

// ---- 步骤三：交付审核 + 导出 ----
const trackChoice = ref<TrackChoice>("original");
const deliveryConfirmed = ref(false);
/** 本地确认绑定的当前导出选择（绑定 tuple 或语言+导出类型）；切换后自动失效。 */
const deliveryConfirmedKey = ref("");
const exportKind = ref<ExportKind>("video");
const bilingualSubtitleExport = ref(false);
const exportLanguage = ref("");
const { outputDir, resolve: resolveOutputDir } = useDefaultOutputDir();
const exportRecordDirectory = ref("");
const namingTemplate = ref("{project}_{language}");
const exportRecord = ref<ExportRecord | null>(null);
const exporting = ref(false);
let exportRefreshTimer: ReturnType<typeof setTimeout> | null = null;
let exportViewDisposed = false;
const preflightWarnings = ref<PreflightIssue[]>([]);

// ---- 人工审核门槛（Core 持久化；digest 由 gate 回传，UI 不计算） ----
const reviews = ref<ReviewGate[]>([]);
const reviewBusy = ref(false);
/** 审核列表读取失败：保持明确未知态，不能当作“没有门槛”而本地确认通过。 */
const reviewReadFailed = ref(false);
/** 审核列表读取代次：晚回包不得覆盖已切换的项目/运行/语种视图。 */
const reviewReads = createReadVersion();
/** 审批请求序号：旧批准回包不得写回新上下文或恢复交付确认。 */
let reviewApprovalSeq = 0;

// ---------------------------------------------------------------------------
// 数据派生
// ---------------------------------------------------------------------------
const currentRun = computed<WorkflowRun | undefined>(() => {
  const runs = project.value?.runs ?? [];
  return runs.find((run) => run.id === activeRunId.value) ?? runs.at(-1);
});

const assets = computed(() => project.value?.assets ?? []);

/** 源素材只接受已就绪的视频/音频；字幕与 audio_track 属于复用素材。 */
const sourceAssets = computed(() =>
  assets.value.filter(
    (asset) => (asset.type === "video" || asset.type === "audio") && asset.status === "ready",
  ),
);
const sourceAssetIds = computed(() => sourceAssets.value.map((asset) => asset.id));

const selectedSourceId = ref("");
const sourceAssetId = computed(() => {
  const selected = sourceAssets.value.find((asset) => asset.id === selectedSourceId.value);
  return selected?.id ?? sourceAssets.value[0]?.id ?? "";
});
const sourceAsset = computed(() =>
  sourceAssets.value.find((asset) => asset.id === sourceAssetId.value),
);

function selectSource(id: string): void {
  if (sourceAssets.value.some((asset) => asset.id === id)) {
    selectedSourceId.value = id;
  }
}

const reusableAudio = computed(
  () =>
    assets.value.find((asset) => asset.type === "audio_track") ??
    assets.value.find((asset) => asset.type === "audio"),
);

const reusableSubtitle = computed(
  () =>
    project.value?.tracks.find((track) => track.type === "transcription") ??
    project.value?.tracks[0],
);

const reusableAudioName = computed(() => reusableAudio.value?.name);
const reusableSubtitleName = computed(() => {
  const track = reusableSubtitle.value;
  return track ? `${t("pipeline.assetType.subtitle")} · ${track.language}` : undefined;
});

const transcriptionTrack = computed<SubtitleTrack | undefined>(() =>
  project.value?.tracks.find((track) => track.type === "transcription"),
);

const targetLanguage = computed(
  () => targetLanguages.value[0] ?? currentRun.value?.languages[0] ?? "en",
);

const translationTrack = computed<SubtitleTrack | undefined>(() => {
  const tracks = project.value?.tracks.filter((track) => track.type === "translation") ?? [];
  return tracks.find((track) => track.language === targetLanguage.value) ?? tracks[0];
});

const candidateLanguage = computed(
  () => translationTrack.value?.language ?? targetLanguage.value,
);

const translationLanguageLabel = computed(() =>
  translationTrack.value
    ? targetLanguageName(translationTrack.value.language, getLocale())
    : targetLanguage.value,
);

function revisionOf(track?: SubtitleTrack): SubtitleRevision | undefined {
  if (!track) return undefined;
  return track.revisions.find((revision) => revision.id === track.currentRevisionId) ?? track.revisions.at(-1);
}

const transcriptionRevision = computed(() => revisionOf(transcriptionTrack.value));
const translationRevision = computed(() => revisionOf(translationTrack.value));

/**
 * 原文/译文不再按数组下标硬配对：合并或拆分改变句数后，下标会整体错位。
 * 这里按时间窗重叠匹配；一句译文对应多句原文时，仅展示层用空格连接文本。
 */
const editorRows = computed<EditorRow[]>(() => {
  const originals = transcriptionRevision.value?.segments ?? [];
  return translationSegments.value.map((translation) => {
    const matches = originals
      .filter(
        (original) =>
          original.startMs < translation.endMs && original.endMs > translation.startMs,
      )
      .sort((a, b) => a.startMs - b.startMs || a.endMs - b.endMs);
    const original: Segment | undefined =
      matches.length === 0
        ? undefined
        : matches.length === 1
          ? matches[0]
          : { ...matches[0], text: matches.map((item) => item.text).join(" ") };
    return { translation, original };
  });
});

const mediaDurationMs = computed(() => {
  const video = assets.value.find(
    (asset) => asset.type === "video" && (asset.meta?.durationMs ?? 0) > 0,
  );
  if (video?.meta) return video.meta.durationMs;
  const ends = transcriptionRevision.value?.segments.map((segment) => segment.endMs) ?? [0];
  return Math.max(0, ...ends);
});

const durationLabel = computed(() => formatDuration(mediaDurationMs.value));

const nodeStates = computed<PipelineNodeState[]>(() =>
  PIPELINE_NODE_KEYS.map((key) => {
    const node = currentRun.value?.nodes.find((candidate) => candidate.key === key);
    if (node) return { ...node };
    return { key, status: "pending", progress: 0 };
  }),
);

const currentNodeKey = computed<PipelineNodeKey | null>(() => {
  const running = nodeStates.value.find((node) => node.status === "running");
  if (running) return running.key;
  const review = nodeStates.value.find((node) => node.status === "waiting_review");
  return review?.key ?? null;
});

const doneNodeCount = computed(
  () =>
    nodeStates.value.filter(
      (node) => node.status === "done" || node.status === "reused" || node.status === "skipped",
    ).length,
);

function nodeState(key: PipelineNodeKey): PipelineNodeState {
  return (
    nodeStates.value.find((node) => node.key === key) ?? { key, status: "pending", progress: 0 }
  );
}
function nodeStateLabel(key: PipelineNodeKey): string {
  return t(nodeStatusLabelKey(nodeState(key).status));
}
function nodeStateTone(key: PipelineNodeKey): Tone {
  return nodeStatusTone(nodeState(key).status);
}

const runLabel = computed(() =>
  currentRun.value ? t(runStatusLabelKey(currentRun.value.status)) : t("pipeline.noRun"),
);
const runTone = computed<Tone>(() =>
  currentRun.value ? runStatusTone(currentRun.value.status) : "neutral",
);

const sourceLanguageLabel = computed(() =>
  languageName(currentRun.value?.sourceLanguage ?? sourceLanguage.value, ASR_LANGUAGES),
);
const targetLanguageLabels = computed(() =>
  targetLanguages.value.map((code) => targetLanguageName(code, getLocale())).join(", "),
);

/** 导出语种候选：Run 的目标语种（缺省以第一个为准）。 */
const exportLanguageOptions = computed<SelectOption[]>(() => {
  const codes =
    currentRun.value?.languages && currentRun.value.languages.length > 0
      ? currentRun.value.languages
      : targetLanguages.value;
  return codes.map((code) => ({ value: code, label: targetLanguageName(code, getLocale()) }));
});

/** 字幕导出绑定导出语种的 translation 轨（Core 必须拿到冻结的 revision ID）。 */
const exportSubtitleTrack = computed<SubtitleTrack | undefined>(() => {
  const tracks = project.value?.tracks.filter((track) => track.type === "translation") ?? [];
  const language = exportLanguage.value || candidateLanguage.value;
  return tracks.find((track) => track.language === language) ?? translationTrack.value;
});

/** 预检 warning 是否引导到存储设置/清理（FR-SET-22）。 */
const storageWarning = computed(() =>
  preflightWarnings.value.some(
    (issue) =>
      issue.actionIds.includes("open_storage") || issue.actionIds.includes("cleanup_storage"),
  ),
);

const projectSelectOptions = computed<SelectOption[]>(() =>
  projectOptions.value.map((item) => ({ value: item.id, label: item.name })),
);

/**
 * 06 翻译审核门槛：只认 Core 的 current_bindings 中与当前 Run、可见语种和
 * 可见译文 revision 同时匹配的 tuple；不按列表顺序或 gate.language 猜测。
 */
const translationView = computed(() =>
  translationReviewView(reviews.value, {
    runId: currentRun.value?.id,
    language: candidateLanguage.value,
    revisionId: translationRevision.value?.id,
  }),
);
const translationCurrent = computed(() => translationView.value.current);
const translationApproved = computed(
  () => translationCurrent.value?.gate.status === "approved",
);

const TERMINAL_RUN_STATUSES = new Set<WorkflowRun["status"]>([
  "SUCCEEDED",
  "PARTIAL",
  "FAILED",
  "CANCELED",
]);
/** 终态 Run 拒绝新批准；已批准内容的导出例外仍保留（PAUSED 允许登记审核）。 */
const reviewTerminalBlocked = computed(
  () => !!currentRun.value && TERMINAL_RUN_STATUSES.has(currentRun.value.status),
);

/** 交付审核按导出语种选择当前绑定；预览/字幕修订也跟随该语种。 */
const deliveryLanguage = computed(() => exportLanguage.value || candidateLanguage.value);
const deliveryView = computed(() =>
  deliveryReviewView(reviews.value, {
    runId: currentRun.value?.id,
    language: deliveryLanguage.value,
    exportKind: exportKind.value,
    subtitleRevisionId: revisionOf(exportSubtitleTrack.value)?.id,
  }),
);
const deliveryRows = computed(() => deliveryView.value.rows);
const deliveryPrimary = computed(() => deliveryView.value.primary);
/** 本次导出实际门禁范围：latest mix + 当前选中字幕 revision（Core 授权口径）。 */
const deliveryRelevantAggregate = computed(() => deliveryView.value.relevantAggregate);

// ---------------------------------------------------------------------------
// 步骤条 / 顶栏主操作
// ---------------------------------------------------------------------------
const steps = computed(() => [
  { key: "import", label: t("pipeline.step.import") },
  { key: "config", label: t("pipeline.step.config") },
  { key: "export", label: t("pipeline.step.export") },
]);

const primaryLabel = computed(() => {
  if (step.value === "import") return t("pipeline.primary.transcribe");
  if (step.value === "config") return t("pipeline.primary.produce");
  return t("pipeline.primary.export");
});

const primaryIcon = computed(() => {
  if (step.value === "import") return "wave";
  if (step.value === "config") return "play";
  return "download";
});

const primaryDisabled = computed(
  () => loading.value || busy.value || importing.value || exporting.value || !project.value,
);

const resultTabs = computed<SelectOption[]>(() => [
  { value: "editor", label: t("pipeline.tab.editor") },
  { value: "candidates", label: t("pipeline.tab.candidates") },
  { value: "delivery", label: t("pipeline.tab.delivery") },
]);

const guideTitle = computed(() =>
  step.value === "import"
    ? t("pipeline.guide.importTitle")
    : t("pipeline.guide.configTitle"),
);
const guideDesc = computed(() =>
  step.value === "import" ? t("pipeline.guide.importDesc") : t("pipeline.guide.configDesc"),
);

const sourceOptions = computed<SelectOption[]>(() =>
  ASR_LANGUAGES.map((language) => ({
    value: language.code,
    label: language.dialect
      ? `${language.name} · ${t("pipeline.import.dialectTag")}`
      : language.name,
  })),
);

const ocrOptions = computed<SelectOption[]>(() =>
  OCR_LANGUAGES.map((language) => ({ value: language.code, label: language.name })),
);

const sourceHint = computed(() => {
  const selected = ASR_LANGUAGES.find((language) => language.code === sourceLanguage.value);
  return selected?.dialect ? t("pipeline.import.dialectHint") : t("pipeline.import.codePending");
});

// ---------------------------------------------------------------------------
// 字幕校正：时间戳解析与校验
// ---------------------------------------------------------------------------
function segmentKey(segment: Segment): string {
  return `${segment.startMs}:${segment.endMs}:${segment.text}`;
}

function parseTimestamp(input: string): number | null {
  const value = input.trim();
  if (value === "") return null;
  // 纯数字按秒解析，便于快速输入。
  if (/^\d+(\.\d+)?$/.test(value)) return Math.round(Number(value) * 1000);
  const match = value.match(/^(?:(\d{1,2}):)?(\d{1,3}):(\d{1,2})(?:[.,](\d{1,3}))?$/);
  if (!match) return null;
  const hours = match[1] ? Number(match[1]) : 0;
  const minutes = Number(match[2]);
  const seconds = Number(match[3]);
  if (seconds > 59) return null;
  if (match[1] && minutes > 59) return null;
  const fraction = (match[4] ?? "").padEnd(3, "0").slice(0, 3);
  return (hours * 3600 + minutes * 60 + seconds) * 1000 + Number(fraction || "0");
}

const rowErrors = computed<Record<string, string[]>>(() => {
  const result: Record<string, string[]> = {};
  const duration = mediaDurationMs.value;
  translationSegments.value.forEach((segment, index) => {
    const draft = drafts[segment.id];
    const messages: string[] = [];
    if (draft) {
      if (parseTimestamp(draft.start) === null) messages.push(t("pipeline.editor.invalidTime"));
      if (parseTimestamp(draft.end) === null) messages.push(t("pipeline.editor.invalidTime"));
    }
    if (segment.startMs < 0) messages.push(t("pipeline.editor.negative"));
    if (segment.endMs <= segment.startMs) messages.push(t("pipeline.editor.invalidRange"));
    if (duration > 0 && segment.endMs > duration) {
      messages.push(t("pipeline.editor.outOfBounds", { duration: formatTimestamp(duration) }));
    }
    const previous = translationSegments.value[index - 1];
    if (previous && previous.endMs > segment.startMs) {
      messages.push(t("pipeline.editor.overlapPrev"));
    }
    const next = translationSegments.value[index + 1];
    if (next && segment.endMs > next.startMs) {
      messages.push(t("pipeline.editor.overlapNext"));
    }
    if (messages.length > 0) result[segment.id] = [...new Set(messages)];
  });
  return result;
});

const hasErrors = computed(() => Object.keys(rowErrors.value).length > 0);

const dirty = computed(() =>
  translationSegments.value.some(
    (segment) => baseline.value[segment.id] !== segmentKey(segment),
  ),
);

// 译文/时间戳改动会使已确认的交付审核失效（docs/workflow-and-review.md §3）。
watch(dirty, (isDirty) => {
  if (isDirty) deliveryConfirmed.value = false;
});

// ---------------------------------------------------------------------------
// 合并 / 拆分：只改本地编辑副本；保存时新句段省略 id，由 Core 分配并保留 lineage
// ---------------------------------------------------------------------------
let localSegmentSeq = 0;

/** 新句段在保存前使用本地临时 ID；保存时桥接层发送空 id。 */
function nextLocalSegmentId(): string {
  localSegmentSeq += 1;
  return `new-${localSegmentSeq}`;
}

/** 已保存句段有 baseline；本地新句段没有，直到下一次保存成功。 */
function isPersistedSegment(segment: Segment): boolean {
  return baseline.value[segment.id] !== undefined;
}

/**
 * 新句段的直接来源：已保存句段取自身 ID；本地新句段尚未持久化，取它已有的
 * lineage，避免把 Core 不认识的临时 ID 当作来源提交。
 */
function lineageSources(segment: Segment): string[] {
  if (isPersistedSegment(segment)) return [segment.id];
  return segment.lineage ?? [];
}

function uniqueIds(ids: string[]): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const id of ids) {
    if (!id || seen.has(id)) continue;
    seen.add(id);
    result.push(id);
  }
  return result;
}

/** 优先使用草稿中可解析的时间；非法草稿由 rowErrors 拦截，不会走到这里。 */
function segmentWindow(segment: Segment): { startMs: number; endMs: number } {
  const draft = drafts[segment.id];
  const start = draft ? parseTimestamp(draft.start) : null;
  const end = draft ? parseTimestamp(draft.end) : null;
  return {
    startMs: start ?? segment.startMs,
    endMs: end ?? segment.endMs,
  };
}

function setDraftFor(segment: Segment): void {
  drafts[segment.id] = {
    start: formatTimestamp(segment.startMs),
    end: formatTimestamp(segment.endMs),
  };
}

function removeEditorRow(id: string): void {
  delete drafts[id];
  delete baseline.value[id];
}

/** 合并/拆分按钮禁用原因；存在原因即禁用（最后一行、时间不足、草稿非法、无轨道）。 */
const segmentActions = computed<Record<string, SegmentActionState>>(() => {
  const hasTrack = Boolean(translationTrack.value);
  const result: Record<string, SegmentActionState> = {};
  translationSegments.value.forEach((segment, index) => {
    const next = translationSegments.value[index + 1];
    const errors = rowErrors.value[segment.id]?.length ?? 0;

    let mergeDisabledReason: string | undefined;
    if (!hasTrack) mergeDisabledReason = t("pipeline.editor.actionNoTrack");
    else if (!next) mergeDisabledReason = t("pipeline.editor.mergeLastRow");
    else if (errors > 0 || (rowErrors.value[next.id]?.length ?? 0) > 0) {
      mergeDisabledReason = t("pipeline.editor.mergeInvalidDraft");
    }

    let splitDisabledReason: string | undefined;
    if (!hasTrack) splitDisabledReason = t("pipeline.editor.actionNoTrack");
    else if (errors > 0) splitDisabledReason = t("pipeline.editor.splitInvalidDraft");
    else {
      const { startMs, endMs } = segmentWindow(segment);
      if (endMs - startMs < 2) splitDisabledReason = t("pipeline.editor.splitTooShort");
    }

    result[segment.id] = { mergeDisabledReason, splitDisabledReason };
  });
  return result;
});

/**
 * 合并当前句与下一句：时间取当前句 start 与下一句 end，文本单空格连接，
 * speaker 取第一个非空值，lineage 记录两个直接来源（或本地新句段的来源）。
 */
function onMergeSegment(id: string): void {
  if (!translationTrack.value) {
    setNotice(messageNotice("pipeline.editor.noTranslationTitle"), "error");
    return;
  }
  const index = translationSegments.value.findIndex((segment) => segment.id === id);
  const current = translationSegments.value[index];
  const next = translationSegments.value[index + 1];
  if (!current) return;
  if (!next) {
    setNotice(messageNotice("pipeline.editor.mergeLastRow"), "info");
    return;
  }
  if (
    (rowErrors.value[current.id]?.length ?? 0) > 0 ||
    (rowErrors.value[next.id]?.length ?? 0) > 0
  ) {
    setNotice(messageNotice("pipeline.editor.mergeInvalidDraft"), "error");
    return;
  }

  const { startMs } = segmentWindow(current);
  const { endMs } = segmentWindow(next);
  const text = [current.text.trim(), next.text.trim()].filter(Boolean).join(" ");
  const merged: Segment = {
    id: nextLocalSegmentId(),
    startMs,
    endMs,
    text,
    speaker: current.speaker || next.speaker || undefined,
    contentRevision: 1,
    contentDigest: digestText(text, 1),
    lineage: uniqueIds([...lineageSources(current), ...lineageSources(next)]),
  };

  removeEditorRow(current.id);
  removeEditorRow(next.id);
  translationSegments.value.splice(index, 2, merged);
  setDraftFor(merged);
  selectedSegmentId.value = merged.id;
  setNotice(messageNotice("pipeline.editor.merged"), "success");
}

/** 在目标位置附近找最近空白；返回切分下标（空白本身不进入任一段）。 */
function nearestWhitespace(text: string, target: number): number | null {
  if (text.length === 0) return null;
  const clamped = Math.min(Math.max(target, 0), text.length);
  for (let offset = 0; offset <= text.length; offset += 1) {
    const after = clamped + offset;
    if (after < text.length && /\s/.test(text[after])) return after;
    const before = clamped - offset;
    if (before >= 0 && before < text.length && /\s/.test(text[before])) return before;
  }
  return null;
}

/**
 * 拆分当前句：中点切时间；文本按时间比例定位字符，优先在最近空白处切分，
 * 无空白则按字符位置切分。两段允许为空文本，由用户继续编辑。
 */
function onSplitSegment(id: string): void {
  if (!translationTrack.value) {
    setNotice(messageNotice("pipeline.editor.noTranslationTitle"), "error");
    return;
  }
  const index = translationSegments.value.findIndex((segment) => segment.id === id);
  const segment = translationSegments.value[index];
  if (!segment) return;
  if ((rowErrors.value[segment.id]?.length ?? 0) > 0) {
    setNotice(messageNotice("pipeline.editor.splitInvalidDraft"), "error");
    return;
  }

  const { startMs, endMs } = segmentWindow(segment);
  if (endMs - startMs < 2) {
    setNotice(messageNotice("pipeline.editor.splitTooShort"), "info");
    return;
  }
  const mid = startMs + Math.floor((endMs - startMs) / 2);
  const ratio = (mid - startMs) / (endMs - startMs);
  const target = Math.round(segment.text.length * ratio);
  const splitAt = nearestWhitespace(segment.text, target) ?? target;
  const leftText = segment.text.slice(0, splitAt).trim();
  const rightText = segment.text.slice(splitAt).trim();
  const lineage = uniqueIds(lineageSources(segment));

  const left: Segment = {
    id: nextLocalSegmentId(),
    startMs,
    endMs: mid,
    text: leftText,
    speaker: segment.speaker,
    contentRevision: 1,
    contentDigest: digestText(leftText, 1),
    lineage,
  };
  const right: Segment = {
    id: nextLocalSegmentId(),
    startMs: mid,
    endMs,
    text: rightText,
    speaker: segment.speaker,
    contentRevision: 1,
    contentDigest: digestText(rightText, 1),
    lineage,
  };

  removeEditorRow(segment.id);
  translationSegments.value.splice(index, 1, left, right);
  setDraftFor(left);
  setDraftFor(right);
  selectedSegmentId.value = left.id;
  setNotice(
    leftText && rightText
      ? messageNotice("pipeline.editor.splitDone")
      : messageNotice("pipeline.editor.splitEmptyText"),
    leftText && rightText ? "success" : "info",
  );
}

function digestText(text: string, revision: number): string {
  // UI 侧占位摘要；真实 content_digest 由 Core 计算。
  let hash = 5381;
  for (let i = 0; i < text.length; i += 1) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) >>> 0;
  }
  return `ui-${revision}-${hash.toString(16)}`;
}

function hydrateEditor(): void {
  const revision = translationRevision.value;
  translationSegments.value = revision ? revision.segments.map((segment) => ({ ...segment })) : [];
  for (const key of Object.keys(drafts)) delete drafts[key];
  const nextBaseline: Record<string, string> = {};
  for (const segment of translationSegments.value) {
    drafts[segment.id] = {
      start: formatTimestamp(segment.startMs),
      end: formatTimestamp(segment.endMs),
    };
    nextBaseline[segment.id] = segmentKey(segment);
  }
  baseline.value = nextBaseline;
  if (
    !selectedSegmentId.value ||
    !translationSegments.value.some((segment) => segment.id === selectedSegmentId.value)
  ) {
    selectedSegmentId.value = translationSegments.value[0]?.id ?? null;
  }
  candidatesBySegment.value = {};
  selectedCandidates.value = {};
  candidateError.value = null;
}

// ---------------------------------------------------------------------------
// 候选
// ---------------------------------------------------------------------------
const selectedCandidateList = computed(() =>
  selectedSegmentId.value ? candidatesBySegment.value[selectedSegmentId.value] ?? [] : [],
);
const selectedCandidateId = computed(() =>
  selectedSegmentId.value ? selectedCandidates.value[selectedSegmentId.value] : undefined,
);

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// 202 只表示已受理：候选生成后通过候选列表读回（FR-PIPE-04）。
async function readCandidates(
  segmentId: string,
  language: string,
  knownCount: number,
): Promise<AudioCandidate[]> {
  const deadline = Date.now() + 8_000;
  let list = await api.listCandidates(segmentId, language);
  while (Date.now() < deadline) {
    if (list.length > knownCount) return list;
    await delay(400);
    list = await api.listCandidates(segmentId, language);
  }
  if (list.length === knownCount) {
    setNotice(messageNotice("pipeline.candidates.pending"), "info");
  }
  return list;
}

async function ensureCandidates(force = false): Promise<void> {
  const segmentId = selectedSegmentId.value;
  if (!segmentId) return;
  if (!force && candidatesBySegment.value[segmentId]) return;
  const language = candidateLanguage.value;
  candidateLoading.value = true;
  candidateError.value = null;
  try {
    if (!selectedCandidates.value[segmentId]) await restoreSelectionFor(segmentId);
    let candidates = force
      ? candidatesBySegment.value[segmentId] ?? []
      : await api.listCandidates(segmentId, language);
    if (force || candidates.length === 0) {
      const knownCount = force ? candidates.length : 0;
      await api.generateCandidates({ segmentId, language });
      candidates = await readCandidates(segmentId, language, knownCount);
    }
    candidatesBySegment.value = { ...candidatesBySegment.value, [segmentId]: candidates };
  } catch (err) {
    candidateError.value = errorNotice(err, "pipeline.candidates.error");
  } finally {
    candidateLoading.value = false;
  }
}

function regenerateCandidates(): void {
  void ensureCandidates(true);
}

async function chooseCandidate(candidateId: string): Promise<void> {
  const segmentId = selectedSegmentId.value;
  if (!segmentId) return;
  busy.value = true;
  try {
    await api.selectCandidate(segmentId, candidateLanguage.value, candidateId);
    selectedCandidates.value = { ...selectedCandidates.value, [segmentId]: candidateId };
    setNotice(messageNotice("pipeline.candidates.selectedNotice"), "success");
  } catch (err) {
    setNotice(errorNotice(err), "error");
  } finally {
    busy.value = false;
  }
}

/** 读回 Core 已登记的候选选择（尚无选择时 getSelection 返回 null）。 */
async function restoreSelectionFor(segmentId: string | null): Promise<void> {
  if (!segmentId || !candidateLanguage.value) return;
  try {
    const selection = await api.getSelection(segmentId, candidateLanguage.value);
    if (selection) {
      selectedCandidates.value = {
        ...selectedCandidates.value,
        [segmentId]: selection.candidateId,
      };
    }
  } catch {
    // 读取失败保持空状态，用户仍可重新选择。
  }
}

// ---------------------------------------------------------------------------
// 试听 / 播放 / 08 A/B 预览
// ---------------------------------------------------------------------------
type MediaKind = "audio" | "video";

interface PlaybackTarget {
  key: string;
  assetId: string;
  kind: MediaKind;
  startMs?: number;
  endMs?: number;
}

interface PreviewAsset {
  id: string;
  kind: MediaKind;
}

/** 同一时刻只允许一个播放 key；面板切换/换句/换项目都会停止当前播放。 */
const playback = reactive({
  key: "",
  assetId: "",
  kind: "audio" as MediaKind,
  startMs: undefined as number | undefined,
  endMs: undefined as number | undefined,
  seekMs: undefined as number | undefined,
  playing: false,
  currentMs: 0,
});

const playbackSupported = typeof desktopApi?.assetUrl === "function";

const playerSource = computed<PlayerSource | null>(() => {
  if (!playback.assetId) return null;
  // 播放地址只由桌面桥生成；浏览器 mock 没有桥时返回空串，试听入口会提示不可用。
  const src =
    typeof desktopApi?.assetUrl === "function" ? desktopApi.assetUrl(playback.assetId) : "";
  if (!src) return null;
  return {
    key: playback.key,
    src,
    kind: playback.kind,
    startMs: playback.startMs,
    endMs: playback.endMs,
    seekMs: playback.seekMs,
  };
});

const playingSegmentId = computed(() =>
  playback.playing && playback.key.startsWith("segment:")
    ? playback.key.slice("segment:".length)
    : "",
);
const playingCandidateId = computed(() =>
  playback.playing && playback.key.startsWith("candidate:")
    ? playback.key.slice("candidate:".length)
    : "",
);
const deliveryPlaying = computed(
  () => playback.playing && playback.key.startsWith("preview:"),
);

/** Run 冻结的输入资产（优先），否则回退到页面当前 source asset。 */
const runInputAssetId = computed(() => currentRun.value?.inputAssetIds?.[0] ?? "");
const playbackSourceAssetId = computed(() => {
  const input = assets.value.find((asset) => asset.id === runInputAssetId.value);
  if (input && (input.type === "video" || input.type === "audio") && input.status === "ready") {
    return input.id;
  }
  return sourceAssetId.value;
});

function mediaKindFor(assetId: string): MediaKind {
  const asset = assets.value.find((item) => item.id === assetId);
  return asset?.type === "video" ? "video" : "audio";
}

function ensurePlaybackSupported(): boolean {
  if (playbackSupported) return true;
  setNotice(messageNotice("pipeline.playback.desktopOnly"), "info");
  return false;
}

function startPlayback(target: PlaybackTarget, seekMs = 0): void {
  playback.key = target.key;
  playback.assetId = target.assetId;
  playback.kind = target.kind;
  playback.startMs = target.startMs;
  playback.endMs = target.endMs;
  playback.seekMs = seekMs > 0 ? seekMs : undefined;
  playback.currentMs = seekMs > 0 ? seekMs : target.startMs ?? 0;
  playback.playing = true;
}

function stopPlayback(): void {
  playback.playing = false;
  playback.key = "";
  playback.assetId = "";
  playback.startMs = undefined;
  playback.endMs = undefined;
  playback.seekMs = undefined;
  playback.currentMs = 0;
}

function onPlaybackError(): void {
  playback.playing = false;
  setNotice(messageNotice("pipeline.playback.failed"), "error");
}

/** 06：按当前编辑中的句子时间窗播放源媒体（未保存的时间修改同样生效）。 */
function auditionSegment(id: string): void {
  if (!ensurePlaybackSupported()) return;
  const segment = translationSegments.value.find((item) => item.id === id);
  if (!segment) return;
  // 用当前草稿时间（含未保存修改）；草稿格式非法时不播放。
  const draft = drafts[id];
  const startMs = draft ? parseTimestamp(draft.start) : segment.startMs;
  const endMs = draft ? parseTimestamp(draft.end) : segment.endMs;
  if (startMs === null || endMs === null || startMs < 0 || endMs <= startMs) {
    setNotice(messageNotice("pipeline.playback.invalidRange"), "error");
    return;
  }
  const assetId = playbackSourceAssetId.value;
  if (!assetId) {
    setNotice(messageNotice("pipeline.playback.noSource"), "error");
    return;
  }
  const key = `segment:${id}`;
  if (playback.key === key) {
    playback.playing = !playback.playing;
    return;
  }
  startPlayback({
    key,
    assetId,
    kind: mediaKindFor(assetId),
    startMs,
    endMs,
  });
}

/** 06 试听可用性：与 auditionSegment 的守卫保持一致；无源或时间非法时按钮禁用。 */
const auditionableSegments = computed<Record<string, boolean>>(() => {
  const hasSource = Boolean(playbackSourceAssetId.value);
  const result: Record<string, boolean> = {};
  for (const segment of translationSegments.value) {
    const draft = drafts[segment.id];
    const startMs = draft ? parseTimestamp(draft.start) : segment.startMs;
    const endMs = draft ? parseTimestamp(draft.end) : segment.endMs;
    result[segment.id] =
      hasSource &&
      startMs !== null &&
      endMs !== null &&
      startMs >= 0 &&
      endMs > startMs;
  }
  return result;
});

/** 07：播放候选音频整段；candidateId 从当前语言候选列表解析 audioAssetId。 */
function auditionCandidate(candidateId: string): void {
  if (!ensurePlaybackSupported()) return;
  const candidate = selectedCandidateList.value.find((item) => item.id === candidateId);
  if (!candidate) return;
  const key = `candidate:${candidateId}`;
  if (playback.key === key) {
    playback.playing = !playback.playing;
    return;
  }
  startPlayback({ key, assetId: candidate.audioAssetId, kind: "audio" });
}

/** 08 预览资产：当前 Run + 当前导出语种的最新 SUCCEEDED 08_mix 任务的首个媒体产物。 */
const previewAsset = ref<PreviewAsset | null>(null);
const previewLoading = ref(false);
const previewResolvedKey = ref("");

function pickMixTask(tasks: Task[], runId: string, language: string): Task | undefined {
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
        b.id.localeCompare(a.id),
    )[0];
}

function firstMediaAsset(artifactIds: string[]): { id: string; kind: MediaKind } | null {
  const byId = new Map(assets.value.map((asset) => [asset.id, asset]));
  for (const assetId of artifactIds) {
    const asset = byId.get(assetId);
    if (asset && (asset.type === "video" || asset.type === "audio")) {
      return { id: asset.id, kind: asset.type === "video" ? "video" : "audio" };
    }
  }
  return null;
}

/** 非破坏性刷新：只合并新资产/运行状态，不重载字幕轨，避免丢弃未保存的时间草稿。 */
async function refreshProjectAssets(): Promise<void> {
  if (!projectId.value || !project.value) return;
  const latest = await api.getProject(projectId.value);
  const current = project.value;
  const known = new Set(current.assets.map((asset) => asset.id));
  const added = latest.assets.filter((asset) => !known.has(asset.id));
  project.value = {
    ...current,
    assets: added.length > 0 ? [...current.assets, ...added] : current.assets,
    runs: latest.runs.length > 0 ? latest.runs : current.runs,
  };
}

async function findPreviewAsset(runId: string, language: string): Promise<PreviewAsset | null> {
  // 快照返回项目全部任务，不依赖 listTasks 首页分页（08 可能排在很后面）。
  const snapshot = await api.getSnapshot(projectId.value);
  const task = pickMixTask(snapshot.tasks, runId, language);
  if (!task || task.artifactIds.length === 0) return null;
  let found = firstMediaAsset(task.artifactIds);
  if (!found) {
    // 08 刚完成时 project.assets 可能还是旧的；只补资产，不重建编辑器。
    await refreshProjectAssets();
    found = firstMediaAsset(task.artifactIds);
  }
  return found;
}

async function resolvePreviewAsset(force = false): Promise<void> {
  const run = currentRun.value;
  if (!projectId.value || !run) {
    previewAsset.value = null;
    previewResolvedKey.value = "";
    return;
  }
  const key = `${run.id}:${deliveryLanguage.value}`;
  if (!force && previewResolvedKey.value === key) return;
  previewLoading.value = true;
  try {
    previewAsset.value = await findPreviewAsset(run.id, deliveryLanguage.value);
    previewResolvedKey.value = key;
  } catch (err) {
    previewAsset.value = null;
    previewResolvedKey.value = "";
    setNotice(errorNotice(err, "pipeline.delivery.previewFailed"), "error");
  } finally {
    previewLoading.value = false;
  }
}

function deliveryTarget(choice: TrackChoice): PreviewAsset | null {
  if (choice === "preview") return previewAsset.value;
  const assetId = playbackSourceAssetId.value;
  if (!assetId) return null;
  return { id: assetId, kind: mediaKindFor(assetId) };
}

/** 播放当前 A/B 选项；“原声”用 Run 输入源媒体，“最终混音”用 08 预览资产。 */
function playDelivery(): void {
  if (!ensurePlaybackSupported()) return;
  const target = deliveryTarget(trackChoice.value);
  if (!target) {
    setNotice(
      trackChoice.value === "preview"
        ? messageNotice("pipeline.delivery.noPreview")
        : messageNotice("pipeline.playback.noSource"),
      "info",
    );
    return;
  }
  const key = `preview:${trackChoice.value}`;
  if (playback.key === key) {
    playback.playing = !playback.playing;
    return;
  }
  const preserve = playback.key.startsWith("preview:") ? playback.currentMs : 0;
  startPlayback({ key, assetId: target.id, kind: target.kind }, preserve);
}

// A/B 切换时若正在播放 08 预览，保留时间位置继续播另一轨；预览更短由播放器 clamp。
watch(trackChoice, (choice) => {
  if (!playback.key.startsWith("preview:")) return;
  const wasPlaying = playback.playing;
  const position = playback.currentMs;
  const target = deliveryTarget(choice);
  if (!target) {
    stopPlayback();
    return;
  }
  startPlayback({ key: `preview:${choice}`, assetId: target.id, kind: target.kind }, position);
  playback.playing = wasPlaying;
});

// 换 tab / 步骤 / 项目 / 句段都停止当前播放，避免隐藏元素继续发声。
watch([resultTab, step, projectId, selectedSegmentId], () => stopPlayback());

// 进入交付页时才解析 08 预览；每次进入都重新解析，避免任务完成后仍显示旧空态。
watch([() => currentRun.value?.id ?? "", deliveryLanguage, step, resultTab], () => {
  if (step.value === "export" && resultTab.value === "delivery") void resolvePreviewAsset(true);
});

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------


const exportFormat = computed<ExportFormat>(() => {
  if (exportKind.value === "audio") return "wav";
  if (exportKind.value === "subtitle") return "srt";
  return "mp4_h264";
});

/** 双语字幕需要项目内可用的原文字幕轨；Core 仍会做最终校验。 */
const bilingualAvailable = computed(() => Boolean(transcriptionRevision.value));

const exportFormatLabel = computed(() => {
  if (exportKind.value === "audio") return t("pipeline.export.formatAudio");
  if (exportKind.value === "subtitle") return t("pipeline.export.formatSubtitle");
  return t("pipeline.export.formatVideo");
});

const namingPreview = computed(() => {
  const extension =
    exportKind.value === "audio" ? ".wav" : exportKind.value === "subtitle" ? ".srt" : ".mp4";
  const language = exportLanguage.value || candidateLanguage.value;
  const base = namingTemplate.value
    .replaceAll("{project}", project.value?.name ?? "")
    .replaceAll("{language}", language);
  return `${base}${extension}`;
});

/**
 * 审核放行 / 交付确认 / 导出的统一本地阻断原因（只防误操作，Core 仍是权威）。
 * 任一写入进行中（保存/候选选择/创建运行共用 busy）优先等待；非法草稿先修正；
 * 未保存草稿不得放行旧持久 revision。
 */
const reviewBlockedNotice = computed<SafeNotice | null>(() => {
  if (busy.value) return messageNotice("pipeline.review.blockedSaving");
  if (hasErrors.value) return messageNotice("pipeline.review.blockedInvalid");
  if (dirty.value) return messageNotice("pipeline.review.blockedDirty");
  return null;
});
const reviewBlockedHint = computed(() => renderNotice(reviewBlockedNotice.value));
const reviewBlocked = computed(() => reviewBlockedNotice.value !== null);
/** 审批按钮统一禁用条件：本地写入/草稿 + 终态 Run；导出例外不经过这里。 */
const reviewActionBlocked = computed(() => reviewBlocked.value || reviewTerminalBlocked.value);
const reviewActionTitle = computed(
  () =>
    reviewBlockedHint.value ||
    (reviewTerminalBlocked.value ? t("pipeline.review.blockedTerminal") : ""),
);

/** 当前导出内容在 Core 投影里没有对应当前绑定：不猜 gate、不自动确认。 */
const deliverySelectionBlockedNotice = computed<SafeNotice | null>(() => {
  if (reviewReadFailed.value) return messageNotice("pipeline.review.readFailed");
  const view = deliveryView.value;
  if (view.projectionUnknown) return messageNotice("pipeline.review.projectionUnknown");
  if (!view.selectionMatched) return messageNotice("pipeline.review.deliveryUnmatched");
  if (view.rows.length === 0) {
    // 有交付门槛但当前语种/内容没有当前绑定：不拿“本机确认”冒充审核完成。
    if (view.hasGates) return messageNotice("pipeline.review.deliveryUnmatched");
    // 当前 Run 尚未生成任何当前交付门槛：等待 Core，不做本地审核通过。
    if (currentRun.value) return messageNotice("pipeline.review.gateMissing");
  }
  return null;
});

/** 交付确认被阻断的原因（与审批共用 dirty/busy/invalid；未知投影也 fail closed）。 */
const deliveryConfirmBlockedNotice = computed<SafeNotice | null>(() => {
  const blocked = reviewBlockedNotice.value;
  if (blocked) return blocked;
  const selection = deliverySelectionBlockedNotice.value;
  if (selection) return selection;
  if (reviewTerminalBlocked.value && deliveryPrimary.value?.status === "pending") {
    return messageNotice("pipeline.review.blockedTerminal");
  }
  // 一次批准后仍有其他“本次导出相关”current pending：不能恢复总体确认。
  if (
    deliveryRows.value.length > 0 &&
    deliveryRelevantAggregate.value !== "approved" &&
    deliveryPrimary.value?.status !== "pending"
  ) {
    return messageNotice("pipeline.review.somePending");
  }
  return null;
});
const deliveryConfirmBlockedHint = computed(() =>
  renderNotice(deliveryConfirmBlockedNotice.value),
);

/** 交付确认框只对“当时确认的那条当前绑定”生效；切语种/导出类型不沿用旧确认。 */
const deliverySelectionKey = computed(() => {
  const view = deliveryView.value;
  if (view.primary) {
    return [
      view.primary.binding.language,
      view.primary.binding.sourceKind,
      view.primary.binding.sourceId,
    ].join("\u0000");
  }
  if (
    !view.projectionUnknown &&
    view.rows.length === 0 &&
    !view.hasGates &&
    !currentRun.value
  ) {
    return ["local", deliveryLanguage.value, exportKind.value].join("\u0000");
  }
  return "";
});
/** 确认框只在本次导出相关的当前门槛都已放行时显示勾选。 */
const deliveryConfirmedValue = computed(() => {
  const view = deliveryView.value;
  if (view.rows.length > 0 && view.relevantAggregate !== "approved") return false;
  return (
    deliveryConfirmed.value &&
    deliverySelectionKey.value.length > 0 &&
    deliveryConfirmedKey.value === deliverySelectionKey.value
  );
});

const deliveryReviewHint = computed(() => {
  if (reviewReadFailed.value) return t("pipeline.review.readFailed");
  const view = deliveryView.value;
  if (view.projectionUnknown) return t("pipeline.review.projectionUnknown");
  if (view.rows.length === 0) {
    return currentRun.value ? t("pipeline.review.gateMissing") : "";
  }
  if (view.aggregate === "approved") return t("pipeline.review.allApproved");
  if (view.relevantAggregate === "approved") return t("pipeline.review.otherPending");
  return t("pipeline.review.somePending");
});

const exportBlockedNotice = computed<SafeNotice | null>(() => {
  const blocked = reviewBlockedNotice.value;
  if (blocked) return blocked;
  const selection = deliverySelectionBlockedNotice.value;
  if (selection) return selection;
  // 本次导出相关的 current pending 必须逐条放行；其他当前 track 不阻塞本次导出。
  if (deliveryRows.value.length > 0 && deliveryRelevantAggregate.value !== "approved") {
    return messageNotice("pipeline.delivery.confirmRequired");
  }
  if (!deliveryConfirmedValue.value) return messageNotice("pipeline.delivery.confirmRequired");
  return null;
});
const exportBlockedHint = computed(() => renderNotice(exportBlockedNotice.value));
const canExport = computed(() => exportBlockedNotice.value === null);

// ---------------------------------------------------------------------------
// 加载 / 刷新 / 项目绑定
// ---------------------------------------------------------------------------
function applyInitialDefaults(): void {
  const currentProject = project.value;
  if (!currentProject) return;
  projectName.value = currentProject.name;
  const run = currentRun.value;
  sourceLanguage.value = run?.sourceLanguage ?? "zh";
  targetLanguages.value = run && run.languages.length > 0 ? [...run.languages] : ["en"];
  exportLanguage.value = run?.languages[0] ?? targetLanguages.value[0] ?? "";
  reuseAudio.value = false;
  reuseSubtitle.value = false;
  const node06 = run?.nodes.find((node) => node.key === "06_translate");
  step.value =
    node06 &&
    (node06.status === "waiting_review" ||
      node06.status === "done" ||
      node06.status === "running")
      ? "export"
      : "import";
}

function queryProjectId(): string {
  const value = route.query.project;
  if (typeof value === "string" && value.length > 0) return value;
  // 项目作用域路由 /projects/:id/pipeline 不依赖 query.project。
  const param = route.params.id;
  return typeof param === "string" ? param : "";
}

let autostartHandled = "";

/**
 * 新建页传入的配置 query：语言/擦除/校验/时长/双语。
 * autostart=1 时切到配置态并在项目加载完成后自动预检/创建一次。
 */
function applyRunQuery(): boolean {
  const query = route.query;
  const langs =
    typeof query.langs === "string"
      ? query.langs
          .split(",")
          .map((code) => code.trim())
          .filter((code) => TARGET_LANGUAGES.some((item) => item.code === code))
      : [];
  if (langs.length > 0) targetLanguages.value = langs;
  if (query.erase === "dialog_only" || query.erase === "dialog_and_art") {
    eraseMode.value = query.erase;
  }
  const boolQuery = (value: unknown): boolean | undefined => {
    if (value === "1") return true;
    if (value === "0") return false;
    return undefined;
  };
  const validate = boolQuery(query.validate);
  if (validate !== undefined) validateTimestamps.value = validate;
  const adjust = boolQuery(query.adjust);
  if (adjust !== undefined) adjustDuration.value = adjust;
  const bilingualQuery = boolQuery(query.bilingual);
  if (bilingualQuery !== undefined) bilingual.value = bilingualQuery;
  if (query.autostart === "1") {
    step.value = "config";
    return true;
  }
  return false;
}

async function loadProjectList(): Promise<void> {
  projectsLoading.value = true;
  try {
    projectOptions.value = await api.listProjects();
  } catch (err) {
    projectOptions.value = [];
    loadError.value = errorNotice(err);
  } finally {
    projectsLoading.value = false;
  }
}

type ReviewLoadOutcome = "applied" | "stale" | "failed";

/** 读取权威审核列表；只把与请求时相同项目/运行的回包写回，失败不伪装成空状态覆盖。 */
async function loadReviews(): Promise<ReviewLoadOutcome> {
  const requestedProject = projectId.value;
  if (!requestedProject) return "stale";
  const requestedRun = currentRun.value?.id ?? "";
  const token = reviewReads.begin();
  try {
    const next = await api.listReviews(requestedProject, requestedRun || undefined);
    if (
      !reviewReads.current(token) ||
      projectId.value !== requestedProject ||
      (currentRun.value?.id ?? "") !== requestedRun
    ) {
      return "stale";
    }
    reviews.value = next;
    reviewReadFailed.value = false;
    syncDeliveryConfirmed();
    return "applied";
  } catch {
    if (
      !reviewReads.current(token) ||
      projectId.value !== requestedProject ||
      (currentRun.value?.id ?? "") !== requestedRun
    ) {
      return "stale";
    }
    // 审核读取失败不阻塞编辑，但必须保持明确未知态：不能当作“没有门槛”而本地确认。
    reviews.value = [];
    reviewReadFailed.value = true;
    deliveryConfirmed.value = false;
    return "failed";
  }
}

/**
 * 只按权威回包恢复交付确认：当前导出内容必须已有 approved 当前绑定；
 * dirty 时保留草稿阻断。无门槛的本地确认仍由用户显式触发。
 */
function syncDeliveryConfirmed(): void {
  if (dirty.value) return;
  if (reviewReadFailed.value) {
    deliveryConfirmed.value = false;
    return;
  }
  const view = deliveryView.value;
  if (view.projectionUnknown) {
    deliveryConfirmed.value = false;
    return;
  }
  if (view.rows.length === 0) {
    // 有门槛却无当前绑定、或当前 Run 尚无门槛：都不保留本地“已确认”。
    deliveryConfirmed.value = false;
    return;
  }
  // 总体确认只在本次导出相关 current pending 都放行后恢复；
  // 同语其他当前 track 的 pending 不阻塞本次已审导出。
  if (view.relevantAggregate === "approved" && view.primary?.status === "approved") {
    deliveryConfirmedKey.value = deliverySelectionKey.value;
    deliveryConfirmed.value = true;
    return;
  }
  deliveryConfirmed.value = false;
}

/** 选中项目：加载项目视图、审核门槛、候选选择，并绑定事件快照高水位。 */
async function selectProject(id: string, options: { syncQuery?: boolean } = {}): Promise<void> {
  // 先清空上一项目的审核/确认，避免把放行结果或旧门槛带到新项目。
  if (projectId.value !== id) {
    exportRecord.value = null;
    exportRecordDirectory.value = "";
    reviews.value = [];
    deliveryConfirmed.value = false;
    // 作废在飞审核读取/审批回包：旧响应不得写回新项目。
    reviewReads.invalidate();
    reviewApprovalSeq += 1;
    reviewBusy.value = false;
    reviewReadFailed.value = false;
    selectedSourceId.value = "";
    previewAsset.value = null;
    previewResolvedKey.value = "";
  }
  projectId.value = id;
  loading.value = true;
  loadError.value = null;
  const token = projectReads.begin();
  try {
    const next = await api.getProject(id);
    // 旧 generation / 更早的项目读取晚回：不写回项目或编辑器。
    if (!projectReads.current(token) || projectId.value !== id) return;
    project.value = next;
    applyInitialDefaults();
    hydrateEditor();
    await loadReviews();
    await restoreSelectionFor(selectedSegmentId.value);
    void tasks.syncProject(id);
  } catch (err) {
    if (!projectReads.current(token) || projectId.value !== id) return;
    loadError.value = errorNotice(err);
    project.value = null;
  } finally {
    loading.value = false;
  }
  if (options.syncQuery !== false && queryProjectId() !== id) {
    await router.replace({ query: { ...route.query, project: id } });
  }
  const shouldAutostart = applyRunQuery();
  if (shouldAutostart && autostartHandled !== id) {
    autostartHandled = id;
    await nextTick();
    void startProduction();
  }
}

async function initProject(): Promise<void> {
  const token = projectReads.begin();
  loading.value = true;
  loadError.value = null;
  project.value = null;
  projectId.value = "";
  await loadProjectList();
  // 更新的读取（项目切换/再次初始化）已开始：旧列表不得继续选中项目。
  if (!projectReads.current(token)) return;
  const requested = queryProjectId();
  if (requested) {
    // 直达链接：即使不在列表里也尝试读取（项目可能刚创建）。
    await selectProject(requested, { syncQuery: false });
    if (project.value) return;
  }
  const first = projectOptions.value[0];
  if (first) {
    await selectProject(first.id);
    return;
  }
  // 无项目：显示引导创建/导入的空状态。
  loading.value = false;
  step.value = "import";
  resultTab.value = "editor";
}

type RefreshOutcome =
  | { status: "applied" | "stale" }
  | { status: "failed"; error: unknown };

/**
 * 重读当前项目并 hydrate。自动恢复默认在启动/提交时复核 dirty/saving/当前项目；
 * 显式写入（保存、导入、创建 Run 等）传 force=true，只在代次仍有效时 hydrate。
 */
async function refreshProject(options: { force?: boolean } = {}): Promise<RefreshOutcome> {
  if (!projectId.value) return { status: "stale" };
  const force = options.force === true;
  if (
    !force &&
    (busy.value || importing.value || exporting.value || dirty.value || hasErrors.value)
  ) {
    recoveryNotice.value = true;
    return { status: "stale" };
  }
  const requestedId = projectId.value;
  const token = projectReads.begin();
  try {
    const next = await api.getProject(requestedId);
    // 旧 generation / 更早请求晚回，或用户已切换项目：不写回任何状态。
    if (!projectReads.current(token) || projectId.value !== requestedId) {
      if (!force) recoveryNotice.value = true;
      return { status: "stale" };
    }
    // 读取期间用户开始编辑或写入：自动恢复不得覆盖草稿。
    if (
      !force &&
      (busy.value || importing.value || exporting.value || dirty.value || hasErrors.value)
    ) {
      recoveryNotice.value = true;
      return { status: "stale" };
    }
    project.value = next;
    hydrateEditor();
    await loadReviews();
    recoveryNotice.value = false;
    saveRefreshPending.value = false;
    return { status: "applied" };
  } catch (error) {
    if (!projectReads.current(token) || projectId.value !== requestedId) {
      if (!force) recoveryNotice.value = true;
      return { status: "stale" };
    }
    return { status: "failed", error };
  }
}

/** 项目切换时清理旧审核/候选状态，避免把上一个项目的放行结果带过去。 */
async function onProjectSelect(id: string): Promise<void> {
  if (!id || id === projectId.value) return;
  reviews.value = [];
  deliveryConfirmed.value = false;
  await selectProject(id);
}

async function createProjectFromEmpty(): Promise<void> {
  const name = newProjectName.value.trim();
  if (!name) {
    setNotice(messageNotice("pipeline.project.emptyName"), "error");
    return;
  }
  creatingProject.value = true;
  try {
    const created = await api.createProject({ name });
    newProjectName.value = "";
    await loadProjectList();
    await selectProject(created.id);
    setNotice(messageNotice("pipeline.project.created"), "success");
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.project.createFailed"), "error");
  } finally {
    creatingProject.value = false;
  }
}

// ---------------------------------------------------------------------------
// 人工审核（06 翻译 / 08 交付）：绑定 gate 的 revision_digest，UI 不计算摘要
// ---------------------------------------------------------------------------
interface ApprovalContext {
  projectId: string;
  runId: string;
  generation: number;
  epoch: number;
  language: string;
  exportKind: ExportKind;
  /** 发起时的可见选择快照（翻译 revision / 交付主绑定），与被点击行的 binding 分开。 */
  selectionId: string;
  binding?: ReviewCurrentBinding;
}

/** 交付上下文快照：只用于判断可见选择是否变化，不把每行身份等同导出主轨。 */
const deliverySelectionSnapshot = computed(
  () => deliveryView.value.primary?.binding.sourceId ?? "",
);

/**
 * 批准动作只能写回发起时的项目/运行/运行时代次/语种/可见选择；
 * 被点击行只需仍属于该上下文，不要求它的 source 等于导出主行。
 */
function approvalContextCurrent(seq: number, context: ApprovalContext): boolean {
  if (seq !== reviewApprovalSeq) return false;
  // Core 重启/新 ready 代次：旧回包不能覆盖新 Run 状态。
  if (runtime.generation !== context.generation || runtime.readyEpoch !== context.epoch) {
    return false;
  }
  if (projectId.value !== context.projectId) return false;
  if ((currentRun.value?.id ?? "") !== context.runId) return false;
  const binding = context.binding;
  if (!binding) return true;
  if (binding.sourceKind === "translation_revision") {
    return (
      candidateLanguage.value === context.language &&
      translationRevision.value?.id === context.selectionId
    );
  }
  return (
    deliveryLanguage.value === context.language &&
    exportKind.value === context.exportKind &&
    deliverySelectionSnapshot.value === context.selectionId
  );
}

async function approveGate(gate: ReviewGate, binding?: ReviewCurrentBinding): Promise<void> {
  if (gate.status === "approved" || reviewBusy.value) return;
  const blocked = reviewBlockedNotice.value;
  if (blocked) {
    setNotice(blocked, "info");
    return;
  }
  if (reviewTerminalBlocked.value) {
    setNotice(messageNotice("pipeline.review.blockedTerminal"), "info");
    return;
  }
  const translationBinding = binding?.sourceKind === "translation_revision";
  const context: ApprovalContext = {
    projectId: projectId.value,
    runId: currentRun.value?.id ?? "",
    generation: runtime.generation,
    epoch: runtime.readyEpoch,
    language: translationBinding ? candidateLanguage.value : deliveryLanguage.value,
    exportKind: exportKind.value,
    selectionId: translationBinding
      ? (translationRevision.value?.id ?? "")
      : deliverySelectionSnapshot.value,
    binding,
  };
  const seq = ++reviewApprovalSeq;
  reviewBusy.value = true;
  try {
    const approved = await api.approveReview({
      reviewId: gate.id,
      revisionDigest: gate.revisionDigest,
      type: gate.type,
      currentBinding: binding,
    });
    if (!approvalContextCurrent(seq, context)) return;
    // 批准回包不保证携带最新 current_bindings：重新读取权威列表，
    // 只在回包仍属于当前上下文时恢复确认；读取失败不假装已同步。
    const outcome = await loadReviews();
    if (!approvalContextCurrent(seq, context)) return;
    if (outcome === "applied") {
      setNotice(messageNotice("pipeline.review.approved"), "success");
      await refreshRun(approved.runId, context);
    } else {
      setNotice(messageNotice("pipeline.review.refreshRequired"), "info");
    }
  } catch (err) {
    if (!approvalContextCurrent(seq, context)) return;
    setNotice(errorNotice(err, "pipeline.review.failed"), "error");
  } finally {
    if (seq === reviewApprovalSeq) reviewBusy.value = false;
  }
}

/** 审核放行后节点状态会推进；只刷新该运行，不重载编辑器（避免丢失未保存校正）。 */
async function refreshRun(runId: string, context: ApprovalContext): Promise<void> {
  if (!project.value || projectId.value !== context.projectId) return;
  if (runtime.generation !== context.generation || runtime.readyEpoch !== context.epoch) {
    return;
  }
  try {
    const run = await api.getRun(runId);
    // 同项目 Core 重启后旧响应同样丢弃，避免覆盖新 Run 状态。
    if (projectId.value !== context.projectId) return;
    if (runtime.generation !== context.generation || runtime.readyEpoch !== context.epoch) {
      return;
    }
    const latest = project.value;
    if (!latest || !latest.runs.some((item) => item.id === run.id)) return;
    project.value = {
      ...latest,
      runs: latest.runs.map((item) => (item.id === run.id ? run : item)),
    };
  } catch {
    // 运行快照读取失败不影响已完成的放行结果。
  }
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------
function setNotice(message: SafeNotice, tone: NoticeTone = "info"): void {
  notice.value = message;
  noticeTone.value = tone;
  if (noticeTimer) clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => {
    notice.value = null;
    noticeTimer = null;
  }, 5000);
}

function onStepChange(next: string): void {
  step.value = next === "config" || next === "export" ? next : "import";
}

function goStep(next: StepKey): void {
  step.value = next;
}

function goProjects(): void {
  void router.push("/projects");
}

function setResultTab(next: string): void {
  resultTab.value = next === "candidates" || next === "delivery" ? next : "editor";
}

function switchResultTab(next: ResultTabKey): void {
  resultTab.value = next;
}

function onPrimary(): void {
  if (!project.value) {
    setNotice(messageNotice("pipeline.project.none"), "error");
    return;
  }
  if (step.value === "import") {
    if (!sourceAsset.value) {
      setNotice(
        assets.value.length === 0
          ? messageNotice("pipeline.notice.noAssets")
          : messageNotice("pipeline.notice.noSource"),
        "error",
      );
      return;
    }
    step.value = "config";
    return;
  }
  if (step.value === "config") {
    void startProduction();
    return;
  }
  switchResultTab("delivery");
  void startExport();
}

async function importPaths(paths: string[]): Promise<boolean> {
  if (!projectId.value) {
    setNotice(messageNotice("pipeline.project.none"), "error");
    return false;
  }
  importing.value = true;
  try {
    await api.importAssets({ projectId: projectId.value, paths, copyMode: copyToProject.value });
    const outcome = await refreshProject({ force: true });
    if (outcome.status === "failed") {
      setNotice(errorNotice(outcome.error, "pipeline.import.importFailed"), "error");
      return false;
    }
    setNotice(countNotice("pipeline.import.imported", paths.length), "success");
    return true;
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.import.importFailed"), "error");
    return false;
  } finally {
    importing.value = false;
  }
}

/**
 * 重新定位缺失的外部源文件：由主进程对话框授权新路径，Core 校验同摘要后恢复。
 * 浏览器 mock 没有文件系统桥，只能提示桌面端可用。
 */
async function relocateMissingAsset(assetId: string): Promise<void> {
  if (!desktopApi) {
    setNotice(messageNotice("pipeline.import.relocateDesktopOnly"), "error");
    return;
  }
  try {
    const paths = await desktopApi.selectFiles({
      title: t("pipeline.import.relocate"),
      filters: [{ name: t("pipeline.import.mediaFilter"), extensions: MEDIA_EXTENSIONS }],
    });
    if (paths.length === 0) return;
    await api.relocateAsset(assetId, paths[0]);
    const outcome = await refreshProject({ force: true });
    if (outcome.status === "failed") {
      setNotice(errorNotice(outcome.error, "pipeline.import.relocateFailed"), "error");
      return;
    }
    setNotice(messageNotice("pipeline.import.relocateSuccess"), "success");
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.import.relocateFailed"), "error");
  }
}

/** 用主进程系统对话框选择文件（sandbox 下唯一可靠的真实路径来源）。 */
async function pickImportFiles(): Promise<void> {
  if (!desktopApi) return;
  try {
    const paths = await desktopApi.selectFiles({
      title: t("pipeline.import.selectFiles"),
      filters: [{ name: t("pipeline.import.mediaFilter"), extensions: MEDIA_EXTENSIONS }],
    });
    if (paths.length === 0) return;
    await importPaths(paths);
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.import.importFailed"), "error");
  }
}

/**
 * 文件夹导入：主进程在系统目录选择器授权后递归扫描受支持媒体，
 * Renderer 只导入返回的已授权路径；空目录/截断/不可读子目录都给出可读提示。
 */
async function pickImportFolder(): Promise<void> {
  if (!desktopApi) return;
  try {
    const result = await desktopApi.selectDirectoryFiles({
      title: t("pipeline.import.selectFolder"),
      extensions: MEDIA_EXTENSIONS,
      limit: MAX_FOLDER_FILES,
    });
    // directory 为空表示用户取消，不提示错误。
    if (!result.directory) return;
    if (result.paths.length === 0) {
      // 目录本身或全部子目录不可读时优先提示读取失败，不伪装成空目录。
      setNotice(
        result.errors > 0
          ? countNotice("pipeline.import.folderErrors", result.errors)
          : messageNotice("pipeline.import.folderEmpty"),
        "error",
      );
      return;
    }
    const imported = await importPaths(result.paths);
    if (!imported) return;
    const notes: SafeNotice[] = [];
    if (result.truncated) {
      notes.push(
        messageNotice("pipeline.import.folderTruncated", {
          total: result.total,
          imported: result.paths.length,
        }),
      );
    }
    if (result.errors > 0) {
      notes.push(countNotice("pipeline.import.folderErrors", result.errors));
    }
    if (notes.length > 0) {
      // 截断文案已包含导入数量；其余提示前补成功数量，避免覆盖成功反馈。
      if (!result.truncated) {
        notes.unshift(countNotice("pipeline.import.imported", result.paths.length));
      }
      setNotice(noticeList(notes), "info");
    }
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.import.folderFailed"), "error");
  }
}

function resolveFilePath(file: File): string {
  if (desktopApi) {
    // 只对用户实际选择/拖入的 File 生效；合成 File 返回空串，不能退化成文件名。
    return desktopApi.getPathForFile(file).trim();
  }
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

async function importFiles(files: File[]): Promise<void> {
  if (files.length === 0) return;
  const paths = files.map(resolveFilePath).filter((path) => path.length > 0);
  if (paths.length !== files.length) {
    setNotice(messageNotice("pipeline.import.pathUnavailable"), "error");
    return;
  }
  await importPaths(paths);
}

function setSourceLanguage(value: string): void {
  sourceLanguage.value = value;
}
function setExtractMode(value: string): void {
  if (value === "asr" || value === "ocr" || value === "both") extractMode.value = value;
}
function setOcrLanguage(value: string): void {
  ocrLanguage.value = value;
}
function setCopyToProject(value: boolean): void {
  copyToProject.value = value;
}
function onProjectName(value: string): void {
  projectName.value = value.slice(0, 30);
}

function toggleTarget(code: string): void {
  const selected = new Set(targetLanguages.value);
  if (selected.has(code)) selected.delete(code);
  else selected.add(code);
  targetLanguages.value = TARGET_LANGUAGES.map((language) => language.code).filter((item) =>
    selected.has(item),
  );
}
function selectAllTargets(): void {
  targetLanguages.value = TARGET_LANGUAGES.map((language) => language.code);
}
function clearTargets(): void {
  targetLanguages.value = [];
}
function setReuseAudio(value: boolean): void {
  reuseAudio.value = value;
}
function setReuseSubtitle(value: boolean): void {
  reuseSubtitle.value = value;
}
function setEraseMode(value: string): void {
  if (value === "dialog_only" || value === "dialog_and_art") eraseMode.value = value;
}
function setValidateTimestamps(value: boolean): void {
  validateTimestamps.value = value;
}
function setAdjustDuration(value: boolean): void {
  adjustDuration.value = value;
}
function setBilingual(value: boolean): void {
  bilingual.value = value;
}

async function startProduction(): Promise<void> {
  if (!project.value || !projectId.value) return;
  if (targetLanguages.value.length === 0) {
    setNotice(messageNotice("pipeline.config.noTarget"), "error");
    return;
  }
  const source = sourceAsset.value;
  if (!source) {
    setNotice(messageNotice("pipeline.notice.noSource"), "error");
    return;
  }
  const name = (projectName.value.trim() || project.value.name).slice(0, 30);
  const extract: ExtractOptions = {
    mode: extractMode.value === "both" ? "asr_ocr" : extractMode.value,
    ocrLanguage:
      extractMode.value === "asr"
        ? undefined
        : (ocrLanguage.value as ExtractOptions["ocrLanguage"]),
  };
  const payload: CreateWorkflowRunInput = {
    projectId: projectId.value,
    name,
    sourceLanguage: sourceLanguage.value,
    targetLanguages: [...targetLanguages.value],
    inputRevisionIds: [source.id],
    extract,
    reuseAudioTrackId: reuseAudio.value ? reusableAudio.value?.id : undefined,
    reuseSubtitleTrackId: reuseSubtitle.value ? reusableSubtitle.value?.id : undefined,
    eraseMode: eraseMode.value,
    validateTimestamps: validateTimestamps.value,
    adjustDuration: adjustDuration.value,
    bilingual: bilingual.value,
    // subtitleStyle：字幕样式字段契约待定，暂不提交，避免编造字段。
  };
  busy.value = true;
  preflightWarnings.value = [];
  try {
    const preflight = await api.preflight(payload);
    const uniqueIssues = dedupePreflightIssues(preflight.issues);
    const blockers = uniqueIssues.filter((issue) => issue.severity === "blocker");
    if (blockers.length > 0) {
      // 展示全部阻断原因，不只第一条；同义项已在展示前去重。
      setNotice(noticeList(blockers.map((issue) => errorNotice(issue)), "；"), "error");
      return;
    }
    // warning 不阻断创建：保留在配置列，按 action_ids 引导到存储设置/清理。
    preflightWarnings.value = uniqueIssues.filter((issue) => issue.severity === "warning");
    const created = await api.createWorkflowRun(payload);
    rememberTargetLanguages("pipeline", created.languages);
    activeRunId.value = created.id;
    exportLanguage.value = created.languages[0] ?? targetLanguages.value[0] ?? "";
    const outcome = await refreshProject({ force: true });
    if (outcome.status === "failed") {
      setNotice(errorNotice(outcome.error), "error");
      return;
    }
    step.value = "export";
    resultTab.value = "editor";
    setNotice(messageNotice("pipeline.notice.runCreated"), "success");
  } catch (err) {
    setNotice(errorNotice(err), "error");
  } finally {
    busy.value = false;
  }
}

function onSegmentText(id: string, value: string): void {
  const segment = translationSegments.value.find((item) => item.id === id);
  if (segment) segment.text = value;
}

function onSegmentTime(id: string, field: "start" | "end", value: string): void {
  const segment = translationSegments.value.find((item) => item.id === id);
  const draft = drafts[id];
  if (!segment || !draft) return;
  draft[field] = value;
  const parsed = parseTimestamp(value);
  if (parsed === null) return;
  if (field === "start") segment.startMs = parsed;
  else segment.endMs = parsed;
}

async function saveCorrections(): Promise<void> {
  // 同一时段只允许一个写入；重复点击直接拒绝，避免并发 base revision。
  if (busy.value) {
    setNotice(messageNotice("pipeline.editor.saving"), "info");
    return;
  }
  const track = translationTrack.value;
  if (!track) {
    setNotice(messageNotice("pipeline.editor.noTranslationTitle"), "error");
    return;
  }
  if (hasErrors.value) {
    setNotice(messageNotice("pipeline.editor.validationBlocked"), "error");
    return;
  }
  const revision = translationRevision.value;
  busy.value = true;
  try {
    const segments = translationSegments.value.map((segment) => {
      // 本地新句段（合并/拆分产生）没有 baseline：省略 id 由 Core 分配新 ID，
      // 桥接层只发送时间/文本/说话人/lineage，不提交 content_revision/digest。
      const isNew = baseline.value[segment.id] === undefined;
      const changed = isNew || baseline.value[segment.id] !== segmentKey(segment);
      const contentRevision = isNew
        ? 1
        : changed
          ? segment.contentRevision + 1
          : segment.contentRevision;
      return {
        ...segment,
        id: isNew ? "" : segment.id,
        contentRevision,
        contentDigest: changed ? digestText(segment.text, contentRevision) : segment.contentDigest,
      };
    });
    await api.saveSubtitle({
      trackId: track.id,
      baseRevisionId: revision?.id ?? track.currentRevisionId,
      segments,
      // 整轨替换：省略字段会被 Core 清空；校正页没有样式/擦除编辑时必须原样回传。
      style: revision?.style,
      eraseBlocks: revision?.eraseBlocks,
    });
    const outcome = await refreshProject({ force: true });
    // 新 revision 使旧交付审核失效：需要重新确认（Core 重新出审核记录后再绑定新摘要）。
    deliveryConfirmed.value = false;
    if (outcome.status === "applied") {
      recoveryNotice.value = false;
      saveRefreshPending.value = false;
      setNotice(messageNotice("pipeline.editor.saved"), "success");
    } else {
      // PUT 已成功但刷新未应用：保留草稿与旧基准，明确提示需重新读取，不自动重放 PUT。
      saveRefreshPending.value = true;
    }
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.editor.saveFailed"), "error");
  } finally {
    busy.value = false;
  }
}

function selectSegment(id: string): void {
  selectedSegmentId.value = id;
}

function onNodeSelect(key: PipelineNodeKey): void {
  if (key === "06_translate") {
    step.value = "export";
    switchResultTab("editor");
    return;
  }
  if (key === "07_clone") {
    step.value = "export";
    switchResultTab("candidates");
    return;
  }
  if (key === "08_mix") {
    step.value = "export";
    switchResultTab("delivery");
  }
}

function openStorageSettings(): void {
  void router.push("/settings/storage");
}
async function openExportDirectory(): Promise<void> {
  if (!desktopApi || exportRecord.value?.status !== "SUCCEEDED" || !exportRecordDirectory.value) {
    setNotice(messageNotice("pipeline.notice.openFolderPending"), "info");
    return;
  }
  try {
    await desktopApi.openPath(exportRecordDirectory.value);
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.export.failed"), "error");
  }
}

/** 桌面导出按 Core 终态读回，成功后才能打开目录；浏览器 mock 保留原交互。 */
function refreshDesktopExport(): void {
  if (!desktopApi || exportViewDisposed || !exportRecord.value) return;
  if (exportRefreshTimer) clearTimeout(exportRefreshTimer);
  const current = exportRecord.value;
  exportRefreshTimer = setTimeout(() => {
    exportRefreshTimer = null;
    void (async () => {
      try {
        const records = await api.listExports(current.projectId);
        if (exportViewDisposed || exportRecord.value?.id !== current.id) return;
        const updated = records.find((record) => record.id === current.id);
        if (updated) exportRecord.value = updated;
      } catch {
        // 暂时断连保留当前记录，后续继续读回；不把受理当作成功。
      }
      if (exportViewDisposed || exportRecord.value?.id !== current.id) return;
      if (!["SUCCEEDED", "FAILED", "CANCELED", "INTERRUPTED"].includes(exportRecord.value.status)) {
        refreshDesktopExport();
      }
    })();
  }, 1_000);
}

function setTrackChoice(value: string): void {
  if (value === "original" || value === "preview") {
    trackChoice.value = value;
  }
}

/**
 * 交付审核确认：
 * - 当前导出内容有匹配的 pending 当前绑定 → 只放行这一条（不批量批准其他 pending）；
 * - 已放行 → 允许本地确认；
 * - 没有当前绑定（旧 Core 未提供投影）→ 拒绝伪造持久确认；
 * - 无任何门槛（节点 08 未完成）→ 保持原“仅本机生效”的显式提示。
 */
async function setDeliveryConfirmed(value: boolean): Promise<void> {
  // 取消确认始终允许；恢复确认必须与审核放行走同一阻断原因。
  if (!value) {
    deliveryConfirmed.value = false;
    return;
  }
  const blocked = reviewBlockedNotice.value;
  if (blocked) {
    setNotice(blocked, "info");
    return;
  }
  if (reviewReadFailed.value) {
    setNotice(messageNotice("pipeline.review.readFailed"), "info");
    return;
  }
  const view = deliveryView.value;
  if (view.projectionUnknown) {
    setNotice(messageNotice("pipeline.review.projectionUnknown"), "info");
    return;
  }
  if (!view.selectionMatched) {
    setNotice(messageNotice("pipeline.review.deliveryUnmatched"), "info");
    return;
  }
  const primary = view.primary;
  if (!primary && view.rows.length === 0) {
    // 当前 Run 尚未生成当前交付门槛：不把本机确认冒充审核通过。
    if (currentRun.value) {
      setNotice(messageNotice("pipeline.review.gateMissing"), "info");
      return;
    }
    // 无 Run（工具/空项目）保持原有本机确认提示。
    deliveryConfirmedKey.value = deliverySelectionKey.value;
    deliveryConfirmed.value = true;
    setNotice(messageNotice("pipeline.review.gatePending"), "info");
    return;
  }
  if (primary && primary.status === "pending") {
    if (reviewTerminalBlocked.value) {
      setNotice(messageNotice("pipeline.review.blockedTerminal"), "info");
      return;
    }
    await approveGate(primary.gate, primary.binding);
    return;
  }
  // 本次导出相关 current pending 未全部放行：逐条操作，不能一次确认代表总体已审。
  if (view.relevantAggregate !== "approved") {
    setNotice(messageNotice("pipeline.review.somePending"), "info");
    return;
  }
  deliveryConfirmedKey.value = deliverySelectionKey.value;
  deliveryConfirmed.value = true;
}
function setExportKind(value: string): void {
  if (value === "video" || value === "audio" || value === "subtitle") {
    exportKind.value = value;
    if (value !== "subtitle") bilingualSubtitleExport.value = false;
  }
}
function setBilingualSubtitleExport(value: boolean): void {
  bilingualSubtitleExport.value = value;
}
function setExportLanguage(value: string): void {
  exportLanguage.value = value;
}
function setNamingTemplate(value: string): void {
  namingTemplate.value = value;
}

async function startExport(): Promise<void> {
  if (!project.value || !projectId.value || exporting.value) return;
  if (!namingTemplate.value.trim()) {
    setNotice(messageNotice("pipeline.export.emptyNaming"), "error");
    return;
  }
  if (!canExport.value) {
    setNotice(exportBlockedNotice.value ?? messageNotice("pipeline.editor.validationBlocked"), "error");
    return;
  }
  exporting.value = true;
  try {
    if (desktopApi) {
      try {
        await resolveOutputDir();
      } catch {
        setNotice(messageNotice("pipeline.export.dirUnavailable"), "error");
        return;
      }
    }
    if (!outputDir.value.trim()) {
      setNotice(messageNotice("pipeline.export.emptyDir"), "error");
      return;
    }
    const directory = outputDir.value.trim();
    // 字幕导出必须在 Core 冻结具体 revision；缺 input_revision_id 会被 400 拒绝。
    const subtitleRevision =
      exportKind.value === "subtitle" ? revisionOf(exportSubtitleTrack.value) : undefined;
    const record = await api.export({
      projectId: projectId.value,
      runId: currentRun.value?.id,
      language: exportLanguage.value || undefined,
      kind: exportKind.value,
      format: exportFormat.value,
      inputRevisionId: subtitleRevision?.id,
      bilingual: exportKind.value === "subtitle" && bilingualSubtitleExport.value,
      outputDir: directory,
      namingTemplate: namingTemplate.value.trim(),
    });
    exportRecord.value = record;
    exportRecordDirectory.value = directory;
    refreshDesktopExport();
    setNotice(messageNotice("pipeline.export.started"), "success");
  } catch (err) {
    setNotice(errorNotice(err, "pipeline.export.failed"), "error");
  } finally {
    exporting.value = false;
  }
}

// 候选按需加载：进入候选页签或切换句段时才请求；审核门槛按需刷新（Core 生成门槛后可见）。
watch([step, resultTab, selectedSegmentId], () => {
  if (step.value === "export" && resultTab.value === "candidates") {
    void ensureCandidates();
    void restoreSelectionFor(selectedSegmentId.value);
  }
  if (step.value === "export" && resultTab.value === "delivery") {
    void loadReviews();
  }
});

// ?project= 直接打开：地址变化时切换项目（不重新写回 query，避免循环）。
watch(
  () => route.query.project,
  (value) => {
    if (typeof value === "string" && value.length > 0 && value !== projectId.value) {
      void selectProject(value, { syncQuery: false });
    }
  },
);

// 宿主重连：干净的编辑页按新连接重读项目；有草稿或写入在飞时保留文本/基准，
// 只显示固定恢复提示，绝不自动保存、放行或重新创建 Run。
let refreshedEpoch = runtime.readyEpoch;
watch(
  () => runtime.readyEpoch,
  (epoch) => {
    if (!runtime.gated || !epoch || epoch === refreshedEpoch) return;
    refreshedEpoch = epoch;
    if (!project.value) {
      // 不可用期间进入且没有可保留内容：就绪后补一次读取。
      recoveryNotice.value = false;
      void initProject();
      return;
    }
    if (busy.value || importing.value || exporting.value || dirty.value || hasErrors.value) {
      recoveryNotice.value = true;
      return;
    }
    recoveryNotice.value = false;
    if (!projectId.value) return;
    void refreshProject().then((outcome) => {
      if (outcome.status === "failed") loadError.value = errorNotice(outcome.error);
    });
  },
);

onMounted(() => {
  void initProject();
  void resolveOutputDir().catch(() => undefined);
});

onBeforeUnmount(() => {
  exportViewDisposed = true;
  if (exportRefreshTimer) clearTimeout(exportRefreshTimer);
  stopPlayback();
  if (noticeTimer) clearTimeout(noticeTimer);
});
</script>

<template>
  <WorkbenchPage
    :title="t('pipeline.title')"
    :primary-label="primaryLabel"
    :primary-icon="primaryIcon"
    :primary-disabled="primaryDisabled"
    icon="film"
    scaffold
    @primary="onPrimary"
  >
    <template #actions>
      <YmSelect
        v-if="projectOptions.length"
        class="project-select"
        :aria-label="t('pipeline.project.emptyTitle')"
        :model-value="projectId"
        :options="projectSelectOptions"
        :disabled="loading || projectsLoading"
        @update:model-value="onProjectSelect"
      />
    </template>

    <template #subheader>
      <div class="flow-bar">
        <YmStepper :steps="steps" :current="step" @update:current="onStepChange" />
      </div>
    </template>

    <template #config>
      <div class="config-col">
        <CapabilityAccessNotice access-key="pipeline" />
        <YmNotice v-if="noticeText" :tone="noticeTone">
          <bdi>{{ noticeText }}</bdi>
        </YmNotice>
        <YmNotice
          v-if="recoveryNotice"
          tone="info"
          data-testid="runtime-recovery-notice"
        >
          <bdi>{{ t("runtime.recovery.dirty") }}</bdi>
        </YmNotice>
        <YmNotice
          v-if="saveRefreshPending"
          tone="info"
          data-testid="save-refresh-pending"
        >
          <bdi>{{ t("pipeline.editor.savedReloadRequired") }}</bdi>
        </YmNotice>

        <YmNotice
          v-if="preflightWarnings.length"
          tone="warning"
          class="preflight-warnings"
        >
          <p
            v-for="warning in preflightWarnings"
            :key="preflightIssueKey(warning)"
            class="preflight-warnings__line"
          >
            <bdi>{{ errorText(warning) }}</bdi>
          </p>
          <template #actions>
            <YmButton
              v-if="storageWarning"
              size="sm"
              variant="secondary"
              icon="disk"
              @click="openStorageSettings"
            >
              {{ t("preflight.openStorage") }}
            </YmButton>
          </template>
        </YmNotice>

        <div v-if="loading" class="state-box">
          <YmSpinner :size="20" />
          <span>{{ t("pipeline.loading") }}</span>
        </div>

        <div v-else-if="loadErrorText" class="state-box state-box--error">
          <YmIcon name="alert" :size="22" />
          <span><bdi>{{ loadErrorText || t("pipeline.loadFailed") }}</bdi></span>
          <YmButton size="sm" variant="secondary" icon="refresh" @click="initProject">
            {{ t("common.retry") }}
          </YmButton>
        </div>

        <div v-else-if="!project" class="empty-project">
          <div class="empty-project__head">
            <h2 class="empty-project__title">{{ t("pipeline.project.emptyTitle") }}</h2>
            <p class="empty-project__desc">{{ t("pipeline.project.emptyDesc") }}</p>
          </div>
          <CreationNotice
            :items="[
              t('pipeline.new.notice1'),
              t('pipeline.new.notice2'),
              t('pipeline.new.notice3'),
              t('pipeline.new.notice4'),
            ]"
          />
          <YmTextInput
            v-model="newProjectName"
            :placeholder="t('pipeline.project.namePlaceholder')"
            :disabled="creatingProject"
          />
          <div class="empty-project__actions">
            <YmButton
              variant="primary"
              icon="plus"
              :loading="creatingProject"
              :disabled="!newProjectName.trim()"
              @click="createProjectFromEmpty"
            >
              {{ t("pipeline.project.create") }}
            </YmButton>
            <YmButton variant="ghost" icon="folder" @click="goProjects">
              {{ t("pipeline.project.openProjects") }}
            </YmButton>
          </div>
        </div>

        <template v-else>
          <ImportStep
            v-if="step === 'import'"
            :assets="assets"
            :source-asset-id="sourceAssetId"
            :source-asset-ids="sourceAssetIds"
            :source-language="sourceLanguage"
            :source-options="sourceOptions"
            :source-hint="sourceHint"
            :extract-mode="extractMode"
            :ocr-language="ocrLanguage"
            :ocr-options="ocrOptions"
            :copy-to-project="copyToProject"
            :importing="importing"
            :use-shell-picker="Boolean(desktopApi)"
            @pick="pickImportFiles"
            @pick-folder="pickImportFolder"
            @files="importFiles"
            @relocate="relocateMissingAsset"
            @select-source="selectSource"
            @update:source-language="setSourceLanguage"
            @update:extract-mode="setExtractMode"
            @update:ocr-language="setOcrLanguage"
            @update:copy-to-project="setCopyToProject"
          />

          <ConfigStep
            v-else-if="step === 'config'"
            :project-name="projectName"
            :target-languages="targetLanguages"
            :target-options="TARGET_LANGUAGES"
            :reusable-audio-name="reusableAudioName"
            :reusable-subtitle-name="reusableSubtitleName"
            :reuse-audio="reuseAudio"
            :reuse-subtitle="reuseSubtitle"
            :erase-mode="eraseMode"
            :validate-timestamps="validateTimestamps"
            :adjust-duration="adjustDuration"
            :bilingual="bilingual"
            @update:project-name="onProjectName"
            @toggle-target="toggleTarget"
            @select-all-targets="selectAllTargets"
            @clear-targets="clearTargets"
            @update:reuse-audio="setReuseAudio"
            @update:reuse-subtitle="setReuseSubtitle"
            @update:erase-mode="setEraseMode"
            @update:validate-timestamps="setValidateTimestamps"
            @update:adjust-duration="setAdjustDuration"
            @update:bilingual="setBilingual"
          />

          <div v-else class="summary">
            <div class="summary__head">
              <h2 class="summary__title">{{ t("pipeline.summary.title") }}</h2>
              <YmBadge :tone="runTone">{{ runLabel }}</YmBadge>
            </div>
            <dl class="summary__list">
              <div class="summary__row">
                <dt>{{ t("pipeline.summary.languages") }}</dt>
                <dd>{{ sourceLanguageLabel }} → {{ targetLanguageLabels || "—" }}</dd>
              </div>
              <div class="summary__row">
                <dt>{{ t("pipeline.summary.nodes") }}</dt>
                <dd>
                  {{ t("pipeline.summary.nodeCount", { done: doneNodeCount, total: nodeStates.length }) }}
                </dd>
              </div>
              <div class="summary__row">
                <dt>{{ t("pipeline.summary.segments") }}</dt>
                <dd><bdi>{{ tCount("pipeline.editor.segmentCount", editorRows.length) }}</bdi></dd>
              </div>
              <div class="summary__row">
                <dt>{{ t("pipeline.summary.translationReview") }}</dt>
                <dd>
                  <YmBadge :tone="nodeStateTone('06_translate')">
                    {{ nodeStateLabel("06_translate") }}
                  </YmBadge>
                </dd>
              </div>
              <div class="summary__row">
                <dt>{{ t("pipeline.summary.deliveryReview") }}</dt>
                <dd>
                  <YmBadge :tone="nodeStateTone('08_mix')">{{ nodeStateLabel("08_mix") }}</YmBadge>
                </dd>
              </div>
            </dl>

            <RunOptionsSummary :params="currentRun?.params" />

            <YmBadge :tone="dirty ? 'warning' : 'success'">
              {{ dirty ? t("pipeline.summary.unsaved") : t("pipeline.summary.saved") }}
            </YmBadge>

            <div
              v-if="
                translationCurrent ||
                translationView.projectionUnknown ||
                translationView.historicalGates.length > 0 ||
                deliveryRows.length > 0 ||
                deliveryView.projectionUnknown ||
                deliveryView.historicalGates.length > 0 ||
                reviewBlocked ||
                reviewTerminalBlocked
              "
              class="review-gates"
            >
              <div
                v-if="translationCurrent || translationView.projectionUnknown"
                class="review-gate"
                :data-testid="translationCurrent ? 'review-gate' : 'review-gate-unavailable'"
                :data-review-id="translationCurrent?.gate.id"
                data-source-kind="translation_revision"
                :data-language="translationCurrent?.binding.language ?? candidateLanguage"
              >
                <span class="review-gate__label">{{ t("pipeline.summary.translationReview") }}</span>
                <template v-if="translationCurrent">
                  <YmBadge :tone="translationApproved ? 'success' : 'warning'">
                    {{ t(translationApproved ? "state.approved" : "state.pending") }}
                  </YmBadge>
                  <YmButton
                    v-if="!translationApproved"
                    size="sm"
                    variant="secondary"
                    data-testid="review-approve"
                    :data-review-id="translationCurrent.gate.id"
                    :loading="reviewBusy"
                    :disabled="reviewActionBlocked"
                    :title="reviewActionTitle || undefined"
                    @click="approveGate(translationCurrent.gate, translationCurrent.binding)"
                  >
                    {{ t("pipeline.review.approve") }}
                  </YmButton>
                </template>
                <YmBadge v-else tone="warning">
                  {{ t("pipeline.review.unavailable") }}
                </YmBadge>
              </div>

              <div
                v-for="row in deliveryRows"
                :key="`${row.binding.sourceKind}:${row.binding.sourceId}`"
                class="review-gate"
                data-testid="review-gate"
                :data-review-id="row.gate.id"
                :data-source-kind="row.binding.sourceKind"
                :data-language="row.binding.language"
              >
                <span class="review-gate__label">
                  {{
                    row.binding.sourceKind === "delivery_mix"
                      ? t("pipeline.review.kindMix")
                      : t("pipeline.review.kindSubtitle")
                  }}
                </span>
                <YmBadge :tone="row.status === 'approved' ? 'success' : 'warning'">
                  {{ t(row.status === "approved" ? "state.approved" : "state.pending") }}
                </YmBadge>
                <YmButton
                  v-if="row.status === 'pending'"
                  size="sm"
                  variant="secondary"
                  data-testid="review-approve"
                  :data-review-id="row.gate.id"
                  :loading="reviewBusy"
                  :disabled="reviewActionBlocked"
                  :title="reviewActionTitle || undefined"
                  @click="approveGate(row.gate, row.binding)"
                >
                  {{ t("pipeline.review.approve") }}
                </YmButton>
              </div>

              <div v-if="deliveryView.projectionUnknown" class="review-gate">
                <span class="review-gate__label">{{ t("pipeline.summary.deliveryReview") }}</span>
                <YmBadge tone="warning">{{ t("pipeline.review.unavailable") }}</YmBadge>
              </div>

              <p
                v-if="deliveryRows.length > 1"
                class="review-gate__hint"
                data-testid="delivery-review-aggregate"
                :data-aggregate="deliveryView.aggregate"
                :data-relevant-aggregate="deliveryView.relevantAggregate"
              >
                {{
                  deliveryView.aggregate === "approved"
                    ? t("pipeline.review.allApproved")
                    : deliveryView.relevantAggregate === "approved"
                      ? t("pipeline.review.otherPending")
                      : t("pipeline.review.somePending")
                }}
              </p>
              <p
                v-if="translationView.projectionUnknown || deliveryView.projectionUnknown"
                class="review-gate__hint"
                data-testid="review-projection-unknown"
              >
                {{ t("pipeline.review.projectionUnknown") }}
              </p>
              <p
                v-else-if="
                  translationView.historicalGates.length > 0 ||
                  deliveryView.historicalGates.length > 0
                "
                class="review-gate__hint"
                data-testid="review-history"
                :data-history-count="
                  translationView.historicalGates.length + deliveryView.historicalGates.length
                "
              >
                {{ t("pipeline.review.historyHint") }}
              </p>
              <p
                v-if="deliveryRows.length > 0 && !deliveryView.selectionMatched"
                class="review-gate__hint"
                data-testid="delivery-unmatched-hint"
              >
                {{ t("pipeline.review.deliveryUnmatched") }}
              </p>
              <p v-if="reviewBlocked" class="review-gate__blocked">
                {{ reviewBlockedHint }}
              </p>
              <p v-else-if="reviewTerminalBlocked" class="review-gate__hint">
                {{ t("pipeline.review.blockedTerminal") }}
              </p>
            </div>

            <div class="summary__actions">
              <YmButton block variant="secondary" icon="edit" @click="goStep('config')">
                {{ t("pipeline.summary.editConfig") }}
              </YmButton>
              <YmButton block variant="ghost" icon="subtitles" @click="switchResultTab('editor')">
                {{ t("pipeline.summary.openEditor") }}
              </YmButton>
              <YmButton block variant="ghost" icon="mic" @click="switchResultTab('candidates')">
                {{ t("pipeline.summary.openCandidates") }}
              </YmButton>
              <YmButton block variant="ghost" icon="download" @click="switchResultTab('delivery')">
                {{ t("pipeline.summary.openDelivery") }}
              </YmButton>
            </div>
            <p class="summary__hint">{{ t("pipeline.summary.lockedHint") }}</p>
          </div>
        </template>
      </div>
    </template>

    <template #results>
      <div class="results-col">
        <div v-if="loading" class="state-box state-box--wide">
          <YmSpinner :size="20" />
          <span>{{ t("pipeline.loading") }}</span>
        </div>

        <div v-else-if="loadErrorText" class="state-box state-box--wide state-box--error">
          <YmIcon name="alert" :size="22" />
          <span><bdi>{{ loadErrorText || t("pipeline.loadFailed") }}</bdi></span>
          <YmButton size="sm" variant="secondary" icon="refresh" @click="initProject">
            {{ t("common.retry") }}
          </YmButton>
        </div>

        <div v-else-if="!project" class="guide">
          <YmEmpty
            icon="folder"
            :title="t('pipeline.project.emptyTitle')"
            :description="t('pipeline.project.emptyDesc')"
          >
            <template #action>
              <YmButton variant="primary" size="sm" icon="plus" @click="goProjects">
                {{ t("pipeline.project.openProjects") }}
              </YmButton>
            </template>
          </YmEmpty>
        </div>

        <template v-else>
          <PipelineNodes
            :nodes="nodeStates"
            :run="currentRun ?? null"
            :current-key="currentNodeKey"
            @select="onNodeSelect"
          />

          <div v-if="step !== 'export'" class="guide">
            <YmEmpty icon="film" :title="guideTitle" :description="guideDesc" />
          </div>

          <template v-else>
            <YmTabs
              :model-value="resultTab"
              :tabs="resultTabs"
              @update:model-value="setResultTab"
            />

            <SubtitleEditor
              v-if="resultTab === 'editor'"
              :rows="editorRows"
              :drafts="drafts"
              :errors="rowErrors"
              :selected-id="selectedSegmentId"
              :dirty="dirty"
              :saving="busy"
              :has-track="translationTrack !== undefined"
              :language-label="translationLanguageLabel"
              :duration-label="durationLabel"
              :playing-id="playingSegmentId"
              :auditionable="auditionableSegments"
              :actions="segmentActions"
              @update:text="onSegmentText"
              @update:time="onSegmentTime"
              @select="selectSegment"
              @audition="auditionSegment"
              @merge="onMergeSegment"
              @split="onSplitSegment"
              @save="saveCorrections"
            />

            <CandidatePanel
              v-else-if="resultTab === 'candidates'"
              :rows="editorRows"
              :selected-id="selectedSegmentId"
              :candidates="selectedCandidateList"
              :loading="candidateLoading"
              :selected-candidate-id="selectedCandidateId"
              :language-label="translationLanguageLabel"
              :error="candidateErrorText"
              :playing-candidate-id="playingCandidateId"
              @select-segment="selectSegment"
              @regenerate="regenerateCandidates"
              @choose="chooseCandidate"
              @audition="auditionCandidate"
            />

            <div v-else class="delivery-stack">
              <DeliveryPanel
                :track-choice="trackChoice"
                :confirmed="deliveryConfirmedValue"
                :review-status="nodeState('08_mix').status"
                :preview-available="previewAsset !== null"
                :preview-loading="previewLoading"
                :playing="deliveryPlaying"
                :blocked-reason="deliveryConfirmBlockedHint || undefined"
                :review-hint="deliveryReviewHint || undefined"
                @update:track-choice="setTrackChoice"
                @update:confirmed="setDeliveryConfirmed"
                @preview="playDelivery"
              >
                <template #player>
                  <AssetPlayer
                    v-if="playerSource"
                    :source="playerSource"
                    :playing="playback.playing"
                    visible
                    :aria-label="t('pipeline.delivery.previewAria')"
                    @update:playing="playback.playing = $event"
                    @update:current-ms="playback.currentMs = $event"
                    @error="onPlaybackError"
                  />
                  <template v-else>
                    <YmIcon name="film" :size="40" />
                    <p class="preview-fallback">{{ t("pipeline.delivery.previewHint") }}</p>
                  </template>
                </template>
              </DeliveryPanel>
              <ExportPanel
                :kind="exportKind"
                :language="exportLanguage"
                :language-options="exportLanguageOptions"
                :output-dir="outputDir"
                :naming-template="namingTemplate"
                :naming-preview="namingPreview"
                :format-label="exportFormatLabel"
                :exporting="exporting"
                :record="exportRecord"
                :can-export="canExport"
                :blocked-hint="exportBlockedHint"
                :bilingual="bilingualSubtitleExport"
                :bilingual-available="bilingualAvailable"
                @update:kind="setExportKind"
                @update:bilingual="setBilingualSubtitleExport"
                @update:language="setExportLanguage"
                @update:naming-template="setNamingTemplate"
                @export="startExport"
                @open-folder="openExportDirectory"
              />
            </div>

            <!-- 06/07 只出声不展示控件；交付页签在 DeliveryPanel 内展示可见播放器。 -->
            <AssetPlayer
              v-if="resultTab !== 'delivery' && playerSource"
              :source="playerSource"
              :playing="playback.playing"
              :visible="false"
              @update:playing="playback.playing = $event"
              @update:current-ms="playback.currentMs = $event"
              @error="onPlaybackError"
            />
          </template>
        </template>
      </div>
    </template>
  </WorkbenchPage>
</template>

<style scoped>
.config-col,
.results-col {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  min-width: 0;
}
.results-col {
  padding-bottom: var(--space-4);
}
.notice {
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
  border: 1px solid transparent;
}
.notice--info {
  background: var(--color-info-soft);
  color: var(--color-info-strong);
  border-color: var(--color-info-soft);
}
.notice--success {
  background: var(--color-success-soft);
  color: var(--color-success-strong);
  border-color: var(--color-success-soft);
}
.notice--error {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
  border-color: var(--color-error-soft);
}
.preflight-warnings {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
}
.preflight-warnings__line {
  margin: 0;
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
}
.state-box {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.state-box--wide {
  width: 100%;
}
.state-box--error {
  color: var(--color-error-strong);
}
.guide {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 260px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.delivery-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.preview-fallback {
  font-size: var(--font-size-sm);
  opacity: 0.75;
  text-align: center;
}
.summary {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.summary__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.summary__title {
  font-size: var(--font-size-lg);
}
.summary__list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
}
.summary__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.summary__row dt {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  flex: none;
}
.summary__row dd {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--color-text);
  text-align: end;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.summary__actions {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
}
.summary__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
.project-select {
  width: 180px;
}
.flow-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  flex-wrap: wrap;
}
.flow-bar :deep(.ym-stepper) {
  flex-wrap: wrap;
  row-gap: var(--space-1);
}
.empty-project {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
}
.empty-project__head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.empty-project__title {
  font-size: var(--font-size-lg);
  font-weight: 600;
}
.empty-project__desc {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
  line-height: var(--line-height-normal);
}
.empty-project__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.review-gates {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
}
.review-gate {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.review-gate__label {
  flex: 1;
  color: var(--color-text-muted);
}
.review-gate__blocked {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
</style>

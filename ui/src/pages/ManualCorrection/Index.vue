<script setup lang="ts">
// 人工校正 · 阶段二：三栏工作台（舞台 + 内容/样式/擦除面板）。
//
// 数据与写操作全部沿用现有 Core 契约：GET /projects/{id}、PUT /subtitle-tracks/{id}/revisions、
// /reviews、/reviews/{id}/approve、/segments/{id}/candidates 与 selection。
// 保存仍是唯一显式入口（顶栏 Save / Ctrl+S），成功后权威重读；不做自动保存或离开 flush。
// 擦除块只随 revision 读写，不派发擦除任务；交付审核始终人工放行。
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { onBeforeRouteLeave, useRoute, useRouter } from "vue-router";
import { getLocale, t, formatDuration, formatTimestamp } from "@/i18n";
import { messageNotice, errorNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { api, ApiError } from "@/api";
import type {
  Asset,
  AssetRole,
  AudioCandidate,
  EraseBlock,
  Project,
  ReviewGate,
  Segment,
  SubtitleRevision,
  SubtitleStyle,
  SubtitleTrack,
  WorkflowRun,
} from "@/domain/types";
import { targetLanguageName } from "@/domain/languages";
import type { EditorRow, SegmentActionState } from "@/pages/Pipeline/types";
import { translationReviewView } from "@/pages/Pipeline/reviewBinding";
import type { PlayerSource } from "@/components/media/types";
import ErasePanel from "./ErasePanel.vue";
import McsExportPanel from "./McsExportPanel.vue";
import McsStage from "./McsStage.vue";
import RemixDialog from "./RemixDialog.vue";
import ShortcutHelp from "./ShortcutHelp.vue";
import StylePanel from "./StylePanel.vue";
import SubtitlePanel from "./SubtitlePanel.vue";
import YmBackButton from "@/components/ui/YmBackButton.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";
import { useRuntimeStore } from "@/stores/runtime";
import { createReadVersion } from "@/composables/readGeneration";

const route = useRoute();
const router = useRouter();
const runtime = useRuntimeStore();
/** 项目读取代次/序号：旧 generation 或更早请求的晚回包不得 hydrate 当前草稿。 */
const projectReads = createReadVersion();

type McsTab = "subtitle" | "style" | "erase";
const TAB_ORDER: readonly McsTab[] = ["subtitle", "style", "erase"];

const project = ref<Project | null>(null);
const loading = ref(true);
const loadError = ref<SafeNotice | null>(null);
const notice = ref<SafeNotice | null>(null);
const loadErrorText = computed(() => renderNotice(loadError.value));
const noticeText = computed(() => renderNotice(notice.value));
const activeTab = ref<McsTab>("subtitle");
const selectedLanguage = ref("");
const selectedSegmentId = ref<string | null>(null);
/** 行内候选展开的句段（同一时刻只展开一行）；不保留独立候选 tab。 */
const expandedSegmentId = ref<string | null>(null);
const shortcutsOpen = ref(false);
const saving = ref(false);
/** 自动保存/显式保存共用的最终状态；错误状态优先于 dirty 展示。 */
const saveState = ref<"idle" | "conflict" | "error" | "refresh-pending">("idle");
const lastSavedAt = ref("");
const AUTOSAVE_DELAY_MS = 1500;
let saveTimer: ReturnType<typeof setTimeout> | undefined;
let currentSave: Promise<PersistOutcome | null> | null = null;
/** 宿主重连时存在草稿/保存：保留文本与基准版本，只提示，不自动提交。 */
const recoveryNotice = ref(false);
/** 显式保存 PUT 成功但刷新未应用：提示需重新读取，不自动重放 PUT。 */
const saveRefreshPending = ref(false);
/** 离开守卫：有未保存修改或保存在飞时先确认，不静默丢稿。 */
const leaveDialogOpen = ref(false);
const leaveTarget = ref<string | null>(null);
const leaveError = ref<string | null>(null);
let allowLeave = false;

/** revision 级样式表单；仅 enabled 时随保存提交，否则回退运行样式。 */
const styleForm = ref<SubtitleStyle>({});
const styleOverride = ref(false);
const styleBaseline = ref("");
/** 擦除块只读占位：保存时原样回传，避免编辑字幕时丢块。 */
const eraseBlocks = ref<EraseBlock[]>([]);

// ---- 撤销/重做：本地编辑快照，只覆盖 UI 草稿与样式表单，不伪装 Core 状态 ----
const undoStack = ref<string[]>([]);
const redoStack = ref<string[]>([]);
const undoPending = ref<string | null>(null);
let undoTimer: ReturnType<typeof setTimeout> | undefined;
let hydrating = false;
let applyingSnapshot = false;

const remixOpen = ref(false);
const remixBusy = ref(false);
const remixDone = ref(false);
const remixStatus = ref<SafeNotice | null>(null);
const remixError = ref<SafeNotice | null>(null);
const remixStatusText = computed(() => renderNotice(remixStatus.value));
const remixErrorText = computed(() => renderNotice(remixError.value));
let remixTimer: ReturnType<typeof setTimeout> | undefined;
/** 轮询代次：切换语种/关闭对话框/卸载后旧回包不得写回。 */
let remixSeq = 0;

/** 导出面板：交付审核只读投影与默认输出目录都由面板消费；不提供本机确认。 */
const exportOpen = ref(false);
const reviews = ref<ReviewGate[]>([]);
const reviewsLoading = ref(false);
const reviewReadFailed = ref(false);
/** 审核读取代次：切换语种/重新打开时丢弃旧回包。 */
let reviewsSeq = 0;

const translationSegments = ref<Segment[]>([]);
const baseline = ref<Record<string, string>>({});
/** 源轨编辑副本；与译文轨各自维护 baseline，按轨独立保存。 */
const sourceSegments = ref<Segment[]>([]);
const sourceBaseline = ref<Record<string, string>>({});
const drafts = reactive<Record<string, { start: string; end: string }>>({});
let localSegmentSeq = 0;
// loading 由最近一次前台读取拥有；generation/代次只决定是否应用数据，不决定谁结束 loading。
let loadSeq = 0;
let loadingOwner = 0;

const candidatesBySegment = ref<Record<string, AudioCandidate[]>>({});
const selectedCandidates = ref<Record<string, string>>({});
const candidateLoading = ref(false);
const candidateError = ref<SafeNotice | null>(null);
const candidateErrorText = computed(() => renderNotice(candidateError.value));

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const playbackSupported = typeof desktopApi?.assetUrl === "function";

const playback = reactive({
  key: "",
  assetId: "",
  kind: "video" as "audio" | "video",
  startMs: undefined as number | undefined,
  endMs: undefined as number | undefined,
  playing: false,
});

/** 舞台模式：源素材 / 成品（final/translated/clean 中优先级最高的 ready 资产）。 */
const stageMode = ref<"source" | "final">("source");
const stageCurrentMs = ref(0);

const playerSource = computed<PlayerSource | null>(() => {
  if (!playback.assetId || !playbackSupported) return null;
  const src = desktopApi.assetUrl(playback.assetId);
  if (!src) return null;
  return {
    key: playback.key,
    src,
    kind: playback.kind,
    startMs: playback.startMs,
    endMs: playback.endMs,
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

const projectId = computed(() => String(route.params.id ?? ""));

const translationTracks = computed<SubtitleTrack[]>(() =>
  (project.value?.tracks ?? []).filter((track) => track.type === "translation"),
);
const translationTrack = computed<SubtitleTrack | undefined>(() =>
  translationTracks.value.find((track) => track.language === selectedLanguage.value) ??
  translationTracks.value.at(-1),
);
const translationRevision = computed<SubtitleRevision | undefined>(() => {
  const track = translationTrack.value;
  if (!track) return undefined;
  return track.revisions.find((item) => item.id === track.currentRevisionId) ?? track.revisions.at(-1);
});

const TERMINAL_RUN_STATUS = new Set<WorkflowRun["status"]>([
  "SUCCEEDED",
  "PARTIAL",
  "FAILED",
  "CANCELED",
]);

/** 当前语种的 Runs，按创建时间升序；样式/派发只消费这里的快照。 */
const runsForLanguage = computed<WorkflowRun[]>(() => {
  const language = selectedLanguage.value;
  if (!language) return [];
  return (project.value?.runs ?? [])
    .filter((run) => run.languages.includes(language))
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
});

/** 当前语种最近一个 Run（含终态）；用于终态提示和样式回退。 */
const latestRunForLanguage = computed<WorkflowRun | undefined>(() =>
  runsForLanguage.value.at(-1),
);

/** 可重新合成的活动 Run；终态/暂停在开始前给出可读提示。 */
const runForLanguage = computed<WorkflowRun | undefined>(() =>
  runsForLanguage.value.filter((run) => !TERMINAL_RUN_STATUS.has(run.status)).at(-1),
);

const remixDisabledReason = computed(() => {
  if (!translationTrack.value) return t("mcs.remix.needTrack");
  if (!runForLanguage.value) {
    return latestRunForLanguage.value ? t("mcs.remix.terminal") : t("mcs.remix.needRun");
  }
  return "";
});

/** revision 未设置样式时回退 RunConfig；与 Core effectiveStyle 同口径。 */
const effectiveStyle = computed<SubtitleStyle>(() => {
  const revisionStyle = translationRevision.value?.style;
  if (revisionStyle) return revisionStyle;
  const run = runForLanguage.value ?? latestRunForLanguage.value;
  return run?.params?.subtitleStyle ?? {};
});

const sourceLanguage = computed(() => project.value?.runs.at(-1)?.sourceLanguage ?? "");
const sourceTrack = computed<SubtitleTrack | undefined>(() => {
  const tracks = project.value?.tracks ?? [];
  return (
    tracks.find(
      (track) => track.type === "transcription" && track.language === sourceLanguage.value,
    ) ?? tracks.find((track) => track.type === "transcription")
  );
});
const sourceRevision = computed<SubtitleRevision | undefined>(() => {
  const track = sourceTrack.value;
  if (!track) return undefined;
  return track.revisions.find((item) => item.id === track.currentRevisionId) ?? track.revisions.at(-1);
});

function assetKind(assetId: string): "audio" | "video" {
  const asset = project.value?.assets.find((item) => item.id === assetId);
  return asset?.type === "video" ? "video" : "audio";
}

const sourceAssetId = computed(() => {
  const assets = project.value?.assets ?? [];
  const originalVideo = assets.find(
    (asset) => asset.type === "video" && asset.status === "ready" && asset.role === "original",
  );
  if (originalVideo) return originalVideo.id;
  const video = assets.find((asset) => asset.type === "video" && asset.status === "ready");
  if (video) return video.id;
  const audio = assets.find((asset) => asset.type === "audio" && asset.status === "ready");
  return audio?.id ?? "";
});

const FINAL_ASSET_ROLES: readonly AssetRole[] = ["final_video", "translated_video", "clean_video"];
const finalAsset = computed<Asset | undefined>(() => {
  const assets = (project.value?.assets ?? []).filter((asset) => asset.status === "ready");
  for (const role of FINAL_ASSET_ROLES) {
    const list = assets.filter((asset) => asset.role === role);
    // 成品视频按当前语种优先；无语言标记的 05 纯净视频作为回退。
    const found =
      list.find((asset) => asset.language === selectedLanguage.value) ??
      list.find((asset) => !asset.language) ??
      list[0];
    if (found) return found;
  }
  return undefined;
});
const finalAssetId = computed(() => finalAsset.value?.id ?? "");

const mediaKind = computed<"audio" | "video">(() => assetKind(sourceAssetId.value));

const mediaDurationMs = computed(() => {
  const asset = project.value?.assets.find((item) => item.id === sourceAssetId.value);
  if (asset?.meta?.durationMs) return asset.meta.durationMs;
  return Math.max(0, ...translationSegments.value.map((segment) => segment.endMs));
});
const durationLabel = computed(() => formatDuration(mediaDurationMs.value));

const stageFinalLabel = computed(() => {
  switch (finalAsset.value?.role) {
    case "final_video":
      return t("mcs.stage.final");
    case "translated_video":
      return t("mcs.stage.dubbed");
    case "clean_video":
      return t("mcs.stage.clean");
    default:
      return t("mcs.stage.final");
  }
});
const stageEmptyDescription = computed(() =>
  playbackSupported ? t("mcs.noMedia") : t("mcs.stage.previewDesktop"),
);
const stageFinalUnavailable = computed(() => t("mcs.stage.finalUnavailable"));

const languageOptions = computed(() =>
  translationTracks.value.map((track) => ({
    value: track.language,
    label: targetLanguageName(track.language, getLocale()),
  })),
);
const languageLabel = computed(() =>
  translationTrack.value
    ? targetLanguageName(translationTrack.value.language, getLocale())
    : selectedLanguage.value,
);

/** 译文句段按 lineage 配对原文；缺失时按相同起止、最大重叠回退。 */
const editorRows = computed<EditorRow[]>(() => {
  const originals = sourceSegments.value;
  const byId = new Map(originals.map((segment) => [segment.id, segment]));
  return translationSegments.value.map((translation) => {
    let original: Segment | undefined;
    for (const id of translation.lineage ?? []) {
      const found = byId.get(id);
      if (found) {
        original = found;
        break;
      }
    }
    if (!original) {
      original = originals.find(
        (item) => item.startMs === translation.startMs && item.endMs === translation.endMs,
      );
    }
    if (!original) {
      let bestOverlap = 0;
      for (const item of originals) {
        const overlap =
          Math.min(item.endMs, translation.endMs) - Math.max(item.startMs, translation.startMs);
        if (overlap > bestOverlap) {
          bestOverlap = overlap;
          original = item;
        }
      }
    }
    return { translation, original };
  });
});

// ---- 校验与编辑 ----
function segmentKey(segment: Segment): string {
  return `${segment.startMs}:${segment.endMs}:${segment.text}`;
}

function parseTimestamp(input: string): number | null {
  const value = input.trim();
  if (value === "") return null;
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
const segmentsDirty = computed(() =>
  translationSegments.value.some(
    (segment) => baseline.value[segment.id] !== segmentKey(segment),
  ),
);
const sourceDirty = computed(() =>
  sourceSegments.value.some(
    (segment) => sourceBaseline.value[segment.id] !== segmentKey(segment),
  ),
);
/** 未启用自定义样式时 styleKey 为空串，表示保存时不覆盖运行样式。 */
const styleKey = computed(() => (styleOverride.value ? JSON.stringify(styleForm.value) : ""));
const styleDirty = computed(() => styleKey.value !== styleBaseline.value);
const dirty = computed(() => segmentsDirty.value || styleDirty.value || sourceDirty.value);
/** 译文 revision 记录的源 revision 与当前源轨不一致：可能仍基于旧原文。 */
const sourceStale = computed(() => {
  const currentSourceId = sourceRevision.value?.id ?? "";
  const inputRevisionId = translationRevision.value?.inputRevisionId ?? "";
  return Boolean(currentSourceId && inputRevisionId && inputRevisionId !== currentSourceId);
});
/** 旧 revision 没有 input_revision_id：无法确认，不宣称最新。 */
const sourceUnknown = computed(() => {
  const currentSourceId = sourceRevision.value?.id ?? "";
  return Boolean(currentSourceId && !translationRevision.value?.inputRevisionId);
});
const changedSegmentCount = computed(
  () =>
    translationSegments.value.filter(
      (segment) =>
        baseline.value[segment.id] === undefined ||
        baseline.value[segment.id] !== segmentKey(segment),
    ).length,
);
type SaveStatus = "saving" | "conflict" | "error" | "refresh-pending" | "dirty" | "saved";
const saveStatus = computed<SaveStatus>(() => {
  if (saving.value) return "saving";
  if (saveState.value === "conflict") return "conflict";
  if (saveState.value === "error") return "error";
  if (saveState.value === "refresh-pending") return "refresh-pending";
  if (dirty.value) return "dirty";
  return "saved";
});
const saveStatusText = computed(() => {
  switch (saveStatus.value) {
    case "saving":
      return t("mcs.save.saving");
    case "conflict":
      return t("mcs.save.conflict");
    case "error":
      return t("mcs.save.failed");
    case "refresh-pending":
      return t("mcs.save.refreshRequired");
    case "dirty":
      return t("mcs.save.dirty");
    default:
      return lastSavedAt.value
        ? t("mcs.save.savedAt", { time: lastSavedAt.value })
        : t("mcs.save.saved");
  }
});
/** 顶栏保存按钮与 Ctrl/Cmd+S 共用的可保存条件（同一保存入口）。 */
const canSave = computed(
  () =>
    Boolean(translationTrack.value) &&
    dirty.value &&
    !saving.value &&
    !hasErrors.value &&
    saveState.value !== "refresh-pending",
);
/** 重新合成弹窗确认键的可用性；非法草稿由 startRemix 显示可读原因。 */
const remixDialogEnabled = computed(
  () => Boolean(translationTrack.value) && Boolean(runForLanguage.value),
);

function segmentWindow(segment: Segment): { startMs: number; endMs: number } {
  const draft = drafts[segment.id];
  const start = draft ? parseTimestamp(draft.start) : null;
  const end = draft ? parseTimestamp(draft.end) : null;
  return { startMs: start ?? segment.startMs, endMs: end ?? segment.endMs };
}

function isPersistedSegment(segment: Segment): boolean {
  return baseline.value[segment.id] !== undefined;
}
function lineageSources(segment: Segment): string[] {
  if (isPersistedSegment(segment)) return [segment.id];
  return segment.lineage ?? [];
}
function uniqueIds(ids: string[]): string[] {
  const seen = new Set<string>();
  return ids.filter((id) => {
    if (!id || seen.has(id)) return false;
    seen.add(id);
    return true;
  });
}
function nextLocalSegmentId(): string {
  localSegmentSeq += 1;
  return `new-${localSegmentSeq}`;
}
function digestText(text: string, revision: number): string {
  let hash = 5381;
  for (let i = 0; i < text.length; i += 1) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) >>> 0;
  }
  return `ui-${revision}-${hash.toString(16)}`;
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

function onSegmentText(id: string, value: string): void {
  const segment = translationSegments.value.find((item) => item.id === id);
  if (segment) segment.text = value;
}
function onSourceText(id: string, value: string): void {
  const segment = sourceSegments.value.find((item) => item.id === id);
  if (segment) segment.text = value;
}
function onSegmentTime(id: string, field: "start" | "end", value: string): void {
  const segment = translationSegments.value.find((item) => item.id === id);
  const draft = drafts[id];
  if (!segment || !draft) return;
  const sourceId = editorRows.value.find((row) => row.translation.id === id)?.original?.id;
  const source = sourceId
    ? sourceSegments.value.find((item) => item.id === sourceId)
    : undefined;
  draft[field] = value;
  const parsed = parseTimestamp(value);
  if (parsed === null) return;
  if (field === "start") {
    segment.startMs = parsed;
    if (source) source.startMs = parsed;
  } else {
    segment.endMs = parsed;
    if (source) source.endMs = parsed;
  }
}

function onMergeSegment(id: string): void {
  if (!translationTrack.value) return;
  const index = translationSegments.value.findIndex((segment) => segment.id === id);
  const current = translationSegments.value[index];
  const next = translationSegments.value[index + 1];
  if (!current || !next) return;
  if (
    (rowErrors.value[current.id]?.length ?? 0) > 0 ||
    (rowErrors.value[next.id]?.length ?? 0) > 0
  ) {
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
  expandedSegmentId.value = null;
}

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

function onSplitSegment(id: string): void {
  if (!translationTrack.value) return;
  const index = translationSegments.value.findIndex((segment) => segment.id === id);
  const segment = translationSegments.value[index];
  if (!segment) return;
  if ((rowErrors.value[segment.id]?.length ?? 0) > 0) return;
  const { startMs, endMs } = segmentWindow(segment);
  if (endMs - startMs < 2) return;
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
  expandedSegmentId.value = null;
}

// ---- 撤销/重做快照 ----
const editorSignature = computed(() =>
  JSON.stringify({
    segments: translationSegments.value.map((segment) => ({
      id: segment.id,
      startMs: segment.startMs,
      endMs: segment.endMs,
      text: segment.text,
      speaker: segment.speaker ?? "",
      contentRevision: segment.contentRevision,
      contentDigest: segment.contentDigest,
      lineage: segment.lineage ?? [],
    })),
    source: sourceSegments.value.map((segment) => ({
      id: segment.id,
      startMs: segment.startMs,
      endMs: segment.endMs,
      text: segment.text,
      speaker: segment.speaker ?? "",
      contentRevision: segment.contentRevision,
      contentDigest: segment.contentDigest,
      lineage: segment.lineage ?? [],
    })),
    drafts: { ...drafts },
    style: styleForm.value,
    styleOverride: styleOverride.value,
    erase: eraseBlocks.value,
  }),
);
const canUndo = computed(() => undoPending.value !== null || undoStack.value.length > 0);
const canRedo = computed(() => redoStack.value.length > 0);

function commitPendingUndo(): void {
  if (undoTimer !== undefined) {
    clearTimeout(undoTimer);
    undoTimer = undefined;
  }
  if (undoPending.value === null) return;
  undoStack.value = [...undoStack.value, undoPending.value].slice(-50);
  redoStack.value = [];
  undoPending.value = null;
}

function clearSaveTimer(): void {
  if (saveTimer !== undefined) {
    clearTimeout(saveTimer);
    saveTimer = undefined;
  }
}

/** 输入/样式变化后的 1.5s 防抖；非法草稿和冲突态不自动提交。 */
function scheduleSave(): void {
  clearSaveTimer();
  if (
    hydrating ||
    loading.value ||
    saving.value ||
    !dirty.value ||
    hasErrors.value ||
    saveState.value === "conflict" ||
    saveState.value === "refresh-pending"
  ) {
    return;
  }
  saveTimer = setTimeout(() => {
    saveTimer = undefined;
    void saveNow("auto");
  }, AUTOSAVE_DELAY_MS);
}

watch(hasErrors, (errors) => {
  if (!errors) scheduleSave();
});

watch(
  editorSignature,
  (value, previous) => {
    if (hydrating || previous === undefined || value === previous) return;
    if (!applyingSnapshot) {
      // 连续输入/拖动合并为一个撤销步：保留这段连续修改开始前的状态。
      if (undoPending.value === null) undoPending.value = previous;
      if (undoTimer !== undefined) clearTimeout(undoTimer);
      undoTimer = setTimeout(() => {
        undoTimer = undefined;
        commitPendingUndo();
      }, 600);
    }
    // 自动保存覆盖普通编辑与撤销/重做；hydrate 期间不触发。
    scheduleSave();
  },
  { flush: "sync" },
);

function applyEditorSnapshot(raw: string): void {
  applyingSnapshot = true;
  try {
    const data = JSON.parse(raw) as {
      segments?: Segment[];
      source?: Segment[];
      drafts?: Record<string, { start: string; end: string }>;
      style?: SubtitleStyle;
      styleOverride?: boolean;
      erase?: EraseBlock[];
    };
    if (data.segments) {
      translationSegments.value = data.segments.map((segment) => ({
        ...segment,
        lineage: segment.lineage ? [...segment.lineage] : undefined,
      }));
    }
    if (data.source) {
      sourceSegments.value = data.source.map((segment) => ({
        ...segment,
        lineage: segment.lineage ? [...segment.lineage] : undefined,
      }));
    }
    for (const key of Object.keys(drafts)) delete drafts[key];
    for (const [key, value] of Object.entries(data.drafts ?? {})) {
      drafts[key] = { ...value };
    }
    styleForm.value = { ...(data.style ?? {}) };
    styleOverride.value = Boolean(data.styleOverride);
    eraseBlocks.value = (data.erase ?? []).map((block) => ({
      ...block,
      bbox: [...block.bbox] as [number, number, number, number],
    }));
    if (
      !selectedSegmentId.value ||
      !translationSegments.value.some((segment) => segment.id === selectedSegmentId.value)
    ) {
      selectedSegmentId.value = translationSegments.value[0]?.id ?? null;
    }
    if (
      expandedSegmentId.value &&
      !translationSegments.value.some((segment) => segment.id === expandedSegmentId.value)
    ) {
      expandedSegmentId.value = null;
    }
  } catch {
    // 快照损坏时保持当前状态。
  } finally {
    applyingSnapshot = false;
  }
}

function undo(): void {
  commitPendingUndo();
  const previous = undoStack.value.at(-1);
  if (previous === undefined) return;
  undoStack.value = undoStack.value.slice(0, -1);
  redoStack.value = [...redoStack.value, editorSignature.value].slice(-50);
  applyEditorSnapshot(previous);
}

function redo(): void {
  commitPendingUndo();
  const next = redoStack.value.at(-1);
  if (next === undefined) return;
  redoStack.value = redoStack.value.slice(0, -1);
  undoStack.value = [...undoStack.value, editorSignature.value].slice(-50);
  applyEditorSnapshot(next);
}

function hydrateEditor(): void {
  hydrating = true;
  try {
    const revision = translationRevision.value;
    translationSegments.value = revision ? revision.segments.map((segment) => ({ ...segment })) : [];
    for (const key of Object.keys(drafts)) delete drafts[key];
    const nextBaseline: Record<string, string> = {};
    for (const segment of translationSegments.value) {
      setDraftFor(segment);
      nextBaseline[segment.id] = segmentKey(segment);
    }
    baseline.value = nextBaseline;
    const source = sourceRevision.value;
    sourceSegments.value = source ? source.segments.map((segment) => ({ ...segment })) : [];
    const nextSourceBaseline: Record<string, string> = {};
    for (const segment of sourceSegments.value) {
      nextSourceBaseline[segment.id] = segmentKey(segment);
    }
    sourceBaseline.value = nextSourceBaseline;
    styleOverride.value = Boolean(revision?.style);
    styleForm.value = { ...effectiveStyle.value, ...(revision?.style ?? {}) };
    styleBaseline.value = styleOverride.value ? JSON.stringify(styleForm.value) : "";
    eraseBlocks.value = (revision?.eraseBlocks ?? []).map((block) => ({
      ...block,
      bbox: [...block.bbox] as [number, number, number, number],
    }));
    if (
      !selectedSegmentId.value ||
      !translationSegments.value.some((segment) => segment.id === selectedSegmentId.value)
    ) {
      selectedSegmentId.value = translationSegments.value[0]?.id ?? null;
    }
    expandedSegmentId.value = null;
    candidatesBySegment.value = {};
    selectedCandidates.value = {};
    candidateError.value = null;
  } finally {
    hydrating = false;
    undoPending.value = null;
    undoStack.value = [];
    redoStack.value = [];
    if (undoTimer !== undefined) {
      clearTimeout(undoTimer);
      undoTimer = undefined;
    }
  }
}

type LoadOutcome = "applied" | "stale" | "failed";

async function load(
  options: { force?: boolean; background?: boolean } = {},
): Promise<LoadOutcome> {
  const force = options.force === true;
  const background = options.background === true;
  const requestSeq = (loadSeq += 1);
  const ownsLoading = !background;
  const requestedId = projectId.value;
  // 自动恢复启动前：已有草稿/保存在飞时不发起读取，保留本地文本与基准 revision。
  if (!force && (saving.value || dirty.value || hasErrors.value)) {
    recoveryNotice.value = true;
    return "stale";
  }
  const token = projectReads.begin();
  if (ownsLoading) {
    loadingOwner = requestSeq;
    loading.value = true;
    loadError.value = null;
  }
  try {
    const next = await api.getProject(requestedId);
    // 旧 generation / 更早请求晚回：不写回项目、草稿或错误状态。
    if (!projectReads.current(token) || projectId.value !== requestedId) {
      if (!force) recoveryNotice.value = true;
      return "stale";
    }
    // 读取期间用户开始编辑或保存：自动恢复不得覆盖草稿；显式 save 后的强制刷新除外。
    if (!force && (saving.value || dirty.value || hasErrors.value)) {
      recoveryNotice.value = true;
      return "stale";
    }
    project.value = next;
    loadError.value = null;
    const translations = next.tracks.filter((track) => track.type === "translation");
    if (!translations.length) {
      loadError.value = messageNotice("mcs.noTranslation");
      return "failed";
    }
    const queryLang = typeof route.query.lang === "string" ? route.query.lang : "";
    // 保存/轮询后的重载保留用户当前语种；仅首次进入按 query → 最后一轨回退。
    const preferred = selectedLanguage.value || queryLang;
    selectedLanguage.value =
      translations.find((track) => track.language === preferred)?.language ??
      translations.find((track) => track.language === queryLang)?.language ??
      translations.at(-1)?.language ??
      "";
    hydrateEditor();
    resetStagePlayback();
    recoveryNotice.value = false;
    saveRefreshPending.value = false;
    saveState.value = "idle";
    return "applied";
  } catch (err) {
    if (!projectReads.current(token) || projectId.value !== requestedId) {
      if (!force) recoveryNotice.value = true;
      return "stale";
    }
    // 后台恢复失败保留当前页面与草稿；只有前台初次加载/重试显示错误态。
    if (!background) loadError.value = errorNotice(err);
    return "failed";
  } finally {
    // 本请求拥有 loading 才结束；后发前台请求会接管，旧回包不提前清除。
    if (ownsLoading && loadingOwner === requestSeq) loading.value = false;
  }
}

/** 断开 Vue 响应式代理后再提交；Electron IPC 的结构化克隆不接收 Proxy。 */
function plainStyle(style: SubtitleStyle): SubtitleStyle {
  return {
    ...style,
    outline: style.outline ? { ...style.outline } : undefined,
    shadow: style.shadow ? { ...style.shadow } : undefined,
  };
}

function plainEraseBlockList(blocks: EraseBlock[]): EraseBlock[] {
  return blocks.map((block) => ({
    ...block,
    bbox: [...block.bbox] as [number, number, number, number],
  }));
}

function plainEraseBlocks(): EraseBlock[] {
  return plainEraseBlockList(eraseBlocks.value);
}

/** 按轨构造保存输入；新段规则与现有整轨保存一致。 */
function buildTrackSegments(segments: Segment[], baselineMap: Record<string, string>) {
  return segments.map((segment) => {
    const isNew = baselineMap[segment.id] === undefined;
    const changed = isNew || baselineMap[segment.id] !== segmentKey(segment);
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
}

interface PersistOutcome {
  saved: SubtitleRevision;
  /** PUT 成功后权威重读是否已应用；false 时不得基于旧草稿继续放行审核。 */
  applied: boolean;
}

/**
 * 逐轨保存：原文轨先、译文轨后，各自使用自己的 base revision。
 * 一轨失败即停止；已成功轨更新基线，失败轨保留草稿，不伪造跨轨原子成功。
 */
async function persistCorrections(
  options: { notify?: boolean } = {},
): Promise<PersistOutcome | null> {
  const track = translationTrack.value;
  if (!track || saving.value || hasErrors.value) return null;
  const sourceNeedsSave = sourceDirty.value && Boolean(sourceTrack.value);
  const translationNeedsSave = segmentsDirty.value || styleDirty.value;
  if (!sourceNeedsSave && !translationNeedsSave) return null;
  saving.value = true;
  notice.value = null;
  let savedTranslation: SubtitleRevision | undefined;
  try {
    if (sourceNeedsSave && sourceTrack.value) {
      const source = sourceTrack.value;
      const sourceRev = sourceRevision.value;
      const savedSource = await api.saveSubtitle({
        trackId: source.id,
        baseRevisionId: sourceRev?.id ?? source.currentRevisionId,
        segments: buildTrackSegments(sourceSegments.value, sourceBaseline.value),
        // 源轨不可编辑样式/擦除；原样回传当前 revision，避免保存时丢占位块。
        style: sourceRev?.style ? plainStyle(sourceRev.style) : undefined,
        eraseBlocks: plainEraseBlockList(sourceRev?.eraseBlocks ?? []),
      });
      sourceSegments.value = savedSource.segments.map((segment) => ({ ...segment }));
      sourceBaseline.value = Object.fromEntries(
        savedSource.segments.map((segment) => [segment.id, segmentKey(segment)]),
      );
    }
    if (translationNeedsSave) {
      const revision = translationRevision.value;
      savedTranslation = await api.saveSubtitle({
        trackId: track.id,
        baseRevisionId: revision?.id ?? track.currentRevisionId,
        segments: buildTrackSegments(translationSegments.value, baseline.value),
        // 整轨替换：只在用户显式覆盖时提交 style；擦除块始终回传已读块。
        style: styleOverride.value ? plainStyle(styleForm.value) : undefined,
        eraseBlocks: plainEraseBlocks(),
      });
    }
    // 全部成功后才做权威重读；force 只绕过 dirty 检查，不绕过代次校验。
    const outcome = await load({ force: true });
    if (outcome === "applied") {
      saveRefreshPending.value = false;
      recoveryNotice.value = false;
      saveState.value = "idle";
      if (options.notify !== false) notice.value = messageNotice("mcs.saved");
    } else {
      // PUT 已成功但快照未应用：保留草稿与旧基准，明确提示需重新读取，不自动重放 PUT。
      saveRefreshPending.value = true;
      saveState.value = "refresh-pending";
    }
    const savedRevision = translationRevision.value ?? savedTranslation;
    if (!savedRevision) return null;
    return { saved: savedRevision, applied: outcome === "applied" };
  } catch (err) {
    saveState.value =
      err instanceof ApiError && err.code === "REVISION_CONFLICT" ? "conflict" : "error";
    notice.value = errorNotice(err, "mcs.saveFailed");
    return null;
  } finally {
    saving.value = false;
  }
}

function formatClock(date: Date): string {
  return date.toLocaleTimeString(getLocale(), { hour: "2-digit", minute: "2-digit" });
}

/**
 * 唯一保存入口：自动保存、显式 Save/Ctrl+S、离开守卫共用。
 * 自动保存/离开保存不弹成功提示；显式保存保留现有提示。
 */
async function saveNow(
  reason: "auto" | "manual" | "leave" = "manual",
): Promise<PersistOutcome | null> {
  clearSaveTimer();
  if (hydrating) return null;
  if (saving.value) {
    const pending = currentSave;
    if (pending) await pending;
    if (reason !== "leave" && dirty.value && !hasErrors.value) scheduleSave();
    return null;
  }
  if (
    !dirty.value ||
    hasErrors.value ||
    !translationTrack.value ||
    saveState.value === "refresh-pending"
  ) {
    return null;
  }
  const task = persistCorrections({ notify: reason === "manual" });
  currentSave = task;
  try {
    const outcome = await task;
    if (outcome?.applied) {
      saveState.value = "idle";
      lastSavedAt.value = formatClock(new Date());
    }
    return outcome;
  } finally {
    currentSave = null;
    if (dirty.value && !hasErrors.value) scheduleSave();
  }
}

async function saveCorrections(): Promise<void> {
  await saveNow("manual");
}

// ---- 离开守卫：先尝试保存；失败/冲突才确认，不静默丢稿 ----

onBeforeRouteLeave(async (to) => {
  if (allowLeave) return true;
  if (!dirty.value && !saving.value) return true;
  leaveTarget.value = to.fullPath;
  leaveError.value = null;
  const outcome = await saveNow("leave");
  const saved =
    outcome?.applied === true ||
    (!dirty.value && !saving.value && saveState.value === "idle");
  if (saved) {
    leaveTarget.value = null;
    leaveError.value = null;
    return true;
  }
  leaveDialogOpen.value = true;
  return false;
});

function onLeaveDialogVisibility(open: boolean): void {
  if (open) {
    leaveDialogOpen.value = true;
    return;
  }
  cancelLeave();
}

function cancelLeave(): void {
  leaveDialogOpen.value = false;
  leaveTarget.value = null;
  leaveError.value = null;
}

function proceedLeave(): void {
  const target = leaveTarget.value;
  allowLeave = true;
  leaveDialogOpen.value = false;
  leaveError.value = null;
  leaveTarget.value = null;
  if (!target) {
    allowLeave = false;
    return;
  }
  // 导航被取消/重复时恢复守卫，下一次离开仍会提示。
  void router.push(target).finally(() => {
    allowLeave = false;
  });
}

function discardAndLeave(): void {
  if (saving.value) return;
  proceedLeave();
}

async function saveAndLeave(): Promise<void> {
  if (!canSave.value) return;
  leaveError.value = null;
  const outcome = await saveNow("manual");
  if (!outcome) {
    if (saveState.value === "refresh-pending") {
      leaveDialogOpen.value = false;
      leaveTarget.value = null;
      return;
    }
    leaveError.value = noticeText.value || t("mcs.saveFailed");
    return;
  }
  if (!outcome.applied) {
    // PUT 已成功但权威重读未应用：留在本页并按现有提示重新读取，不带着旧视图离开。
    leaveDialogOpen.value = false;
    leaveTarget.value = null;
    return;
  }
  proceedLeave();
}

/** 窗口关闭/刷新由浏览器原生确认；页面内路由由 onBeforeRouteLeave 接管。 */
function onBeforeUnload(event: BeforeUnloadEvent): void {
  if (!dirty.value && !saving.value) return;
  event.preventDefault();
  event.returnValue = "";
}

// ---- 舞台播放（单一 playback 状态；源/成品资产均由 assetUrl 生成） ----
function setStageMode(mode: "source" | "final"): void {
  if (mode === "final" && !finalAssetId.value) return;
  stageMode.value = mode;
  stageCurrentMs.value = 0;
  const assetId = mode === "source" ? sourceAssetId.value : finalAssetId.value;
  if (!assetId) {
    playback.key = "";
    playback.assetId = "";
    playback.playing = false;
    playback.startMs = undefined;
    playback.endMs = undefined;
    return;
  }
  playback.key = `stage:${mode}`;
  playback.assetId = assetId;
  playback.kind = assetKind(assetId);
  playback.startMs = undefined;
  playback.endMs = undefined;
  playback.playing = false;
}

/** 权威读取后把舞台指回可用资产；成品不存在时回退源素材。 */
function resetStagePlayback(): void {
  if (stageMode.value === "final" && !finalAssetId.value) stageMode.value = "source";
  setStageMode(stageMode.value);
}

const activeStageSegment = computed(() =>
  translationSegments.value.find(
    (segment) => stageCurrentMs.value >= segment.startMs && stageCurrentMs.value <= segment.endMs,
  ),
);
const stageSegments = computed(() =>
  translationSegments.value.map((segment) => {
    const { startMs, endMs } = segmentWindow(segment);
    return { id: segment.id, startMs, endMs, text: segment.text };
  }),
);
const stageSubtitleText = computed(() => activeStageSegment.value?.text ?? "");
const stageAuditionLabel = computed(() =>
  playback.kind === "audio" && playback.key.startsWith("candidate:")
    ? t("mcs.stage.audition")
    : "",
);
/** 样式预览只做映射展示，不写回 revision；无字段时使用默认媒体字幕样式。 */
const stageSubtitleStyle = computed<Record<string, string>>(() => {
  const style = styleOverride.value ? styleForm.value : effectiveStyle.value;
  const result: Record<string, string> = {};
  if (style.align) result.textAlign = style.align;
  if (style.bold) result.fontWeight = "700";
  if (style.fontSize !== undefined && style.fontSize > 0) {
    result.fontSize = `${style.fontSize}px`;
  }
  if (style.lineSpacing !== undefined) {
    result.lineHeight =
      style.lineSpacing >= 1 ? `${style.lineSpacing}px` : String(1 + style.lineSpacing);
  }
  if (style.background) result.background = style.background;
  if (style.outline?.color) {
    const width = Math.max(1, style.outline.width ?? 1);
    result.textShadow = `0 0 ${width}px ${style.outline.color}`;
  } else if (style.shadow?.color) {
    result.textShadow = `${style.shadow.x ?? 0}px ${style.shadow.y ?? 1}px ${
      style.shadow.blur ?? 2
    }px ${style.shadow.color}`;
  }
  if (style.y !== undefined) {
    // y 与 Core 同口径：0-1 相对、≥1 为 1080p 基准像素。
    const fraction = style.y > 1 ? style.y / 1080 : style.y;
    result.bottom = `${Math.max(2, Math.min(92, (1 - fraction) * 100))}%`;
  }
  return result;
});

function onPlaybackError(): void {
  // AssetPlayer 的失败键固定为已注册的播放失败文案；不把运行时字符串当键。
  playback.playing = false;
  notice.value = messageNotice("pipeline.playback.failed");
}

// ---- 导出（默认 outputs；面板按 kind 校验当前交付绑定） ----
const exportRevisionId = computed(() => translationRevision.value?.id);
const exportRunId = computed(() => latestRunForLanguage.value?.id);
const bilingualAvailable = computed(() => Boolean(sourceRevision.value));

/** 打开导出面板时读取一次审核投影；晚回包不得覆盖已切换的语种/运行。 */
async function loadReviews(): Promise<void> {
  const run = latestRunForLanguage.value;
  const requestedId = projectId.value;
  const seq = (reviewsSeq += 1);
  reviewReadFailed.value = false;
  if (!run) {
    reviews.value = [];
    reviewsLoading.value = false;
    return;
  }
  reviewsLoading.value = true;
  try {
    const list = await api.listReviews(requestedId, run.id);
    if (
      seq !== reviewsSeq ||
      projectId.value !== requestedId ||
      latestRunForLanguage.value?.id !== run.id
    ) {
      return;
    }
    reviews.value = list;
  } catch {
    if (seq !== reviewsSeq || projectId.value !== requestedId) return;
    reviews.value = [];
    reviewReadFailed.value = true;
  } finally {
    if (seq === reviewsSeq) reviewsLoading.value = false;
  }
}

function openExport(): void {
  exportOpen.value = true;
  void loadReviews();
}

// ---- 重新合成（当前语种） ----
function openRemix(): void {
  remixOpen.value = true;
  remixBusy.value = false;
  remixDone.value = false;
  remixStatus.value = null;
  remixError.value = null;
}

function stopRemixPolling(): void {
  remixSeq += 1;
  if (remixTimer !== undefined) {
    clearTimeout(remixTimer);
    remixTimer = undefined;
  }
}

function resetRemix(): void {
  stopRemixPolling();
  remixOpen.value = false;
  remixBusy.value = false;
  remixDone.value = false;
  remixStatus.value = null;
  remixError.value = null;
}

function isRemixCurrent(token: number): boolean {
  return token === remixSeq;
}

function mixNode(run: WorkflowRun) {
  return run.nodes.find((node) => node.key === "08_mix");
}

function cloneNodeStatus(run: WorkflowRun) {
  return run.nodes.find((node) => node.key === "07_clone")?.status;
}

async function pollRemix(runId: string, token: number): Promise<void> {
  const deadline = Date.now() + 5 * 60 * 1000;
  const tick = async (): Promise<void> => {
    remixTimer = undefined;
    if (!isRemixCurrent(token)) return;
    try {
      const run = await api.getRun(runId);
      if (!isRemixCurrent(token)) return;
      const mix = mixNode(run);
      const clone = cloneNodeStatus(run);
      if (run.status === "PAUSED" || run.status === "PAUSE_REQUESTED") {
        remixError.value = messageNotice("mcs.remix.paused");
        remixBusy.value = false;
        return;
      }
      if (mix?.status === "failed") {
        remixError.value = messageNotice("mcs.remix.failed");
        remixBusy.value = false;
        return;
      }
      if (
        mix?.status === "waiting_review" ||
        (mix?.status === "done" && TERMINAL_RUN_STATUS.has(run.status))
      ) {
        remixDone.value = true;
        remixBusy.value = false;
        remixStatus.value = messageNotice("mcs.remix.waitingDelivery");
        void load({ background: true });
        return;
      }
      if (mix?.status === "running") {
        remixStatus.value = messageNotice("mcs.remix.mixing");
      } else if (clone === "pending" || clone === "running") {
        remixStatus.value = messageNotice("mcs.remix.running");
      } else {
        remixStatus.value = messageNotice("mcs.remix.started");
      }
      if (TERMINAL_RUN_STATUS.has(run.status)) {
        if (mix?.status === "done") {
          remixDone.value = true;
          remixStatus.value = messageNotice("mcs.remix.done");
        } else {
          remixError.value = messageNotice("mcs.remix.failed");
        }
        remixBusy.value = false;
        return;
      }
    } catch (err) {
      if (!isRemixCurrent(token)) return;
      remixError.value = errorNotice(err, "mcs.remix.failed");
      remixBusy.value = false;
      return;
    }
    if (!isRemixCurrent(token)) return;
    if (Date.now() < deadline) {
      remixTimer = setTimeout(() => void tick(), 1500);
    } else {
      remixError.value = messageNotice("mcs.remix.timeout");
      remixBusy.value = false;
    }
  };
  await tick();
}

/**
 * 确认当前 revision：必要时先保存，然后只放行与当前 (run, language, revision)
 * 精确匹配的 pending translation gate（携带 currentBinding，由 Core 复核）；
 * 找不到当前绑定就 fail closed，绝不按摘要裸批。
 */
async function startRemix(): Promise<void> {
  if (remixBusy.value) return;
  const run = runForLanguage.value;
  if (!run) {
    remixError.value = latestRunForLanguage.value
      ? messageNotice("mcs.remix.terminal")
      : messageNotice("mcs.remix.needRun");
    return;
  }
  if (run.status === "PAUSED" || run.status === "PAUSE_REQUESTED") {
    remixError.value = messageNotice("mcs.remix.paused");
    return;
  }
  if (hasErrors.value) {
    remixError.value = messageNotice("mcs.remix.fixErrors");
    return;
  }
  remixSeq += 1;
  const token = remixSeq;
  remixBusy.value = true;
  remixDone.value = false;
  remixStatus.value = null;
  remixError.value = null;
  try {
    if (dirty.value) {
      const outcome = await persistCorrections();
      if (!isRemixCurrent(token)) return;
      if (!outcome) {
        remixBusy.value = false;
        remixError.value = notice.value ?? messageNotice("mcs.saveFailed");
        return;
      }
      if (!outcome.applied || translationRevision.value?.id !== outcome.saved.id) {
        // 快照未应用或权威重读已指向其他 revision：不能基于旧草稿放行审核。
        remixBusy.value = false;
        remixError.value = messageNotice("mcs.remix.refreshRequired");
        return;
      }
    }
    const revision = translationRevision.value;
    if (!revision) {
      remixBusy.value = false;
      remixError.value = messageNotice("mcs.remix.noPending");
      return;
    }
    const reviews: ReviewGate[] = await api.listReviews(projectId.value, run.id);
    if (!isRemixCurrent(token)) return;
    const view = translationReviewView(reviews, {
      runId: run.id,
      language: selectedLanguage.value,
      revisionId: revision.id,
    });
    if (view.projectionUnknown) {
      // 旧 Core 未提供 current_bindings：不能猜当前门槛，更不能按摘要裸批。
      remixBusy.value = false;
      remixError.value = messageNotice("mcs.remix.unknownBinding");
      return;
    }
    const current = view.current;
    if (!current) {
      remixBusy.value = false;
      remixError.value = messageNotice("mcs.remix.noPending");
      return;
    }
    if (current.gate.status === "pending") {
      await api.approveReview({
        reviewId: current.gate.id,
        revisionDigest: current.gate.revisionDigest,
        type: "translation",
        currentBinding: current.binding,
      });
      if (!isRemixCurrent(token)) return;
    }
    remixStatus.value = messageNotice("mcs.remix.started");
    await pollRemix(run.id, token);
  } catch (err) {
    if (!isRemixCurrent(token)) return;
    remixBusy.value = false;
    remixError.value = errorNotice(err, "mcs.remix.failed");
  }
}

function goDelivery(): void {
  remixOpen.value = false;
  void router.push(`/projects/${projectId.value}/pipeline`);
}

/**
 * 页面级快捷键：Ctrl/Cmd+S 走唯一保存入口；Ctrl/Cmd+Enter 打开重新合成；
 * Ctrl/Cmd+Z（Shift 重做）在非输入控件上执行本地撤销/重做；1/2/3 切换页签；
 * ? 打开快捷键帮助。输入法组合、对话框焦点、输入控件与本页不可用状态都不拦截。
 */
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest('input, textarea, select, [contenteditable="true"]'));
}
function inDialog(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(target.closest('[role="dialog"]'));
}

function onPageKeydown(event: KeyboardEvent): void {
  if (event.defaultPrevented || event.isComposing) return;
  const target = event.target;
  const mod = event.ctrlKey || event.metaKey;
  const key = event.key.toLowerCase();
  if (mod && !event.altKey && !event.shiftKey && key === "s") {
    if (inDialog(target)) return;
    event.preventDefault();
    if (canSave.value) void saveCorrections();
    return;
  }
  if (inDialog(target)) return;
  if (mod && !event.altKey) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      if (
        !remixBusy.value &&
        !saving.value &&
        translationTrack.value &&
        runForLanguage.value
      ) {
        openRemix();
      }
      return;
    }
    if (key === "z" && !isEditableTarget(target)) {
      event.preventDefault();
      if (event.shiftKey) redo();
      else undo();
    }
    return;
  }
  if (!mod && !event.altKey && !isEditableTarget(target)) {
    if (event.key === "?") {
      event.preventDefault();
      shortcutsOpen.value = true;
      return;
    }
    if (!event.shiftKey) {
      const numeric = Number(event.key);
      if (Number.isInteger(numeric) && numeric >= 1 && numeric <= TAB_ORDER.length) {
        const tab = TAB_ORDER[numeric - 1];
        if (tab) {
          event.preventDefault();
          activeTab.value = tab;
        }
      }
    }
  }
}

// ---- 候选 ----
const expandedCandidates = computed(() =>
  expandedSegmentId.value ? candidatesBySegment.value[expandedSegmentId.value] ?? [] : [],
);
const expandedCandidateId = computed(() =>
  expandedSegmentId.value ? selectedCandidates.value[expandedSegmentId.value] : undefined,
);
const candidateLanguage = computed(() => translationTrack.value?.language ?? "");

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

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
  if (list.length === knownCount) notice.value = messageNotice("pipeline.candidates.pending");
  return list;
}

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
    // 保持空选择，用户可以重新选择。
  }
}

/** 展开行时只读回已有候选（不隐式创建 TTS 任务）；生成由显式按钮触发。 */
async function loadCandidatesFor(segmentId: string): Promise<void> {
  if (!segmentId || !candidateLanguage.value) return;
  candidateLoading.value = true;
  candidateError.value = null;
  try {
    if (!selectedCandidates.value[segmentId]) await restoreSelectionFor(segmentId);
    const list = await api.listCandidates(segmentId, candidateLanguage.value);
    candidatesBySegment.value = { ...candidatesBySegment.value, [segmentId]: list };
  } catch (err) {
    candidateError.value = errorNotice(err, "pipeline.candidates.error");
  } finally {
    candidateLoading.value = false;
  }
}

function toggleCandidates(id: string): void {
  if (expandedSegmentId.value === id) {
    expandedSegmentId.value = null;
    return;
  }
  selectedSegmentId.value = id;
  expandedSegmentId.value = id;
  candidateError.value = null;
  if (!candidatesBySegment.value[id]) void loadCandidatesFor(id);
}

async function ensureCandidates(force = false): Promise<void> {
  const segmentId = selectedSegmentId.value;
  if (!segmentId || !candidateLanguage.value) return;
  if (!force && candidatesBySegment.value[segmentId]) return;
  candidateLoading.value = true;
  candidateError.value = null;
  try {
    if (!selectedCandidates.value[segmentId]) await restoreSelectionFor(segmentId);
    let candidates = force
      ? candidatesBySegment.value[segmentId] ?? []
      : await api.listCandidates(segmentId, candidateLanguage.value);
    if (force || candidates.length === 0) {
      const knownCount = force ? candidates.length : 0;
      await api.generateCandidates({ segmentId, language: candidateLanguage.value });
      candidates = await readCandidates(segmentId, candidateLanguage.value, knownCount);
    }
    candidatesBySegment.value = { ...candidatesBySegment.value, [segmentId]: candidates };
  } catch (err) {
    candidateError.value = errorNotice(err, "pipeline.candidates.error");
  } finally {
    candidateLoading.value = false;
  }
}

function regenerateCandidates(): void {
  const segmentId = expandedSegmentId.value;
  if (segmentId) selectedSegmentId.value = segmentId;
  void ensureCandidates(true);
}

async function chooseCandidate(candidateId: string): Promise<void> {
  const segmentId = selectedSegmentId.value;
  if (!segmentId) return;
  try {
    await api.selectCandidate(segmentId, candidateLanguage.value, candidateId);
    selectedCandidates.value = { ...selectedCandidates.value, [segmentId]: candidateId };
    notice.value = messageNotice("pipeline.candidates.selectedNotice");
  } catch (err) {
    notice.value = errorNotice(err);
  }
}

// ---- 播放 ----
function ensurePlaybackSupported(): boolean {
  if (playbackSupported) return true;
  notice.value = messageNotice("pipeline.playback.desktopOnly");
  return false;
}

function startPlayback(target: {
  key: string;
  assetId: string;
  kind: "audio" | "video";
  startMs?: number;
  endMs?: number;
}): void {
  playback.key = target.key;
  playback.assetId = target.assetId;
  playback.kind = target.kind;
  playback.startMs = target.startMs;
  playback.endMs = target.endMs;
  playback.playing = true;
}

function auditionSegment(id: string): void {
  if (!ensurePlaybackSupported()) return;
  if (!sourceAssetId.value) {
    notice.value = messageNotice("mcs.noMedia");
    return;
  }
  const segment = translationSegments.value.find((item) => item.id === id);
  if (!segment) return;
  const { startMs, endMs } = segmentWindow(segment);
  if (startMs < 0 || endMs <= startMs) return;
  const key = `segment:${id}`;
  if (stageMode.value !== "source") stageMode.value = "source";
  if (
    playback.key === key &&
    playback.assetId === sourceAssetId.value &&
    playback.startMs === startMs &&
    playback.endMs === endMs
  ) {
    playback.playing = !playback.playing;
    return;
  }
  startPlayback({
    key,
    assetId: sourceAssetId.value,
    kind: mediaKind.value,
    startMs,
    endMs,
  });
  stageCurrentMs.value = startMs;
}

function auditionCandidate(candidateId: string): void {
  if (!ensurePlaybackSupported()) return;
  const candidate = expandedCandidates.value.find((item) => item.id === candidateId);
  if (!candidate) return;
  const key = `candidate:${candidateId}`;
  if (playback.key === key) {
    playback.playing = !playback.playing;
    return;
  }
  startPlayback({ key, assetId: candidate.audioAssetId, kind: "audio" });
}

const auditionableSegments = computed<Record<string, boolean>>(() => {
  const hasSource = Boolean(sourceAssetId.value);
  const result: Record<string, boolean> = {};
  for (const segment of translationSegments.value) {
    const { startMs, endMs } = segmentWindow(segment);
    result[segment.id] = hasSource && startMs >= 0 && endMs > startMs;
  }
  return result;
});

// 行内候选的生成/选择/试听都以 selectedSegmentId 为准；选中行变化时收起旧展开行，
// 避免把 A 行的候选操作写到 B 行。
watch(selectedSegmentId, (id) => {
  if (expandedSegmentId.value && expandedSegmentId.value !== id) {
    expandedSegmentId.value = null;
  }
});

watch(selectedLanguage, () => {
  resetRemix();
  hydrateEditor();
  // 成品/译文资产按语种选择；切换语种后舞台回到当前语种的可用资产。
  resetStagePlayback();
  // 导出面板在本页打开时，交付审核绑定按语种重新读取。
  if (exportOpen.value) void loadReviews();
});

watch(remixOpen, (open) => {
  if (open) return;
  stopRemixPolling();
  remixBusy.value = false;
});

// 宿主重连：干净页按新连接重读项目；有草稿/保存在飞时保留本地文本与基准 revision，
// 只显示固定恢复提示，不自动保存或放行审核。
let refreshedEpoch = runtime.readyEpoch;
watch(
  () => runtime.readyEpoch,
  (epoch) => {
    if (!runtime.gated || !epoch || epoch === refreshedEpoch) return;
    refreshedEpoch = epoch;
    if (saving.value || dirty.value || hasErrors.value) {
      recoveryNotice.value = true;
      return;
    }
    recoveryNotice.value = false;
    // 后台恢复不闪 loading：编辑器保持可交互，提交前再复核 dirty/saving。
    void load(project.value ? { background: true } : {});
  },
);

onMounted(() => {
  document.addEventListener("keydown", onPageKeydown);
  window.addEventListener("beforeunload", onBeforeUnload);
  void load();
});

onBeforeUnmount(() => {
  document.removeEventListener("keydown", onPageKeydown);
  window.removeEventListener("beforeunload", onBeforeUnload);
  clearSaveTimer();
  stopRemixPolling();
  reviewsSeq += 1;
  if (undoTimer !== undefined) clearTimeout(undoTimer);
});
</script>

<template>
  <div class="mcs-page">
    <header class="mcs-top">
      <YmBackButton
        :label="t('mcs.back')"
        :text="t('mcs.back')"
        @click="router.push(`/projects/${projectId}`)"
      />
      <span class="mcs-top__divider" aria-hidden="true"></span>
      <span v-if="project?.episodeId" class="mcs-top__id mono">
        <bdi dir="ltr">#{{ project.episodeId }}</bdi>
      </span>
      <div class="mcs-top__title">
        <h1 class="mcs-top__name truncate">{{ project?.name ?? t("mcs.title") }}</h1>
        <span class="mcs-status mcs-top__save" :class="`is-${saveStatus}`">
          <span class="mcs-status__dot" aria-hidden="true"></span>
          {{ saveStatusText }}
        </span>
      </div>
      <span v-if="languageLabel" class="mcs-top__lang">
        <bdi>{{ languageLabel }}</bdi>
      </span>
      <div class="mcs-top__controls">
        <YmIconButton
          name="undo"
          :label="t('mcs.undo')"
          :size="15"
          :disabled="!canUndo"
          @click="undo"
        />
        <YmIconButton
          name="redo"
          :label="t('mcs.redo')"
          :size="15"
          :disabled="!canRedo"
          @click="redo"
        />
        <span class="mcs-top__divider" aria-hidden="true"></span>
        <YmIconButton
          name="help"
          :label="t('mcs.shortcuts')"
          :size="15"
          @click="shortcutsOpen = true"
        />
        <YmIconButton
          name="refresh"
          :label="t('mcs.reload')"
          :size="15"
          :disabled="loading || saving || dirty || hasErrors"
          @click="load()"
        />
      </div>
      <div class="mcs-top__actions">
        <YmButton
          size="sm"
          variant="primary"
          :loading="saving"
          :disabled="!canSave"
          @click="saveCorrections"
        >
          {{ t("mcs.save") }}
        </YmButton>
      </div>
      <YmIconButton
        class="mcs-top__export"
        name="download"
        :label="t('pipeline.export.title')"
        :size="15"
        :disabled="loading || !project"
        data-testid="mcs-export-open"
        @click="openExport"
      />
      <YmButton
        class="mcs-top__remix"
        size="sm"
        variant="secondary"
        icon="play"
        :aria-label="t('mcs.remix.button')"
        :disabled="!translationTrack || remixBusy || saving || !runForLanguage"
        :title="remixDisabledReason || undefined"
        @click="openRemix"
      >
        <span class="mcs-top__remix-label">{{ t("mcs.remix.button") }}</span>
      </YmButton>
    </header>

    <p v-if="noticeText" class="mcs-notice" role="status"><bdi>{{ noticeText }}</bdi></p>
    <p
      v-if="sourceStale"
      class="mcs-notice mcs-source-warning"
      role="status"
      data-testid="source-changed-notice"
    >
      <bdi>{{ t("mcs.source.changed") }}</bdi>
    </p>
    <p
      v-else-if="sourceUnknown"
      class="mcs-notice mcs-source-warning"
      role="status"
      data-testid="source-unknown-notice"
    >
      <bdi>{{ t("mcs.source.unknown") }}</bdi>
    </p>
    <p
      v-if="recoveryNotice"
      class="mcs-notice"
      role="status"
      data-testid="runtime-recovery-notice"
    >
      <bdi>{{ t("runtime.recovery.dirty") }}</bdi>
    </p>
    <p v-if="saveRefreshPending" class="mcs-notice" role="status" data-testid="save-refresh-pending">
      <bdi>{{ t("mcs.savedReloadRequired") }}</bdi>
    </p>

    <div v-if="loading" class="mcs-state">
      <YmSpinner :size="22" />
      <span>{{ t("mcs.loading") }}</span>
    </div>
    <YmEmpty
      v-else-if="loadErrorText"
      icon="subtitles"
      :title="t('mcs.loadError')"
      :description="loadErrorText"
    >
      <template #action>
        <YmButton size="sm" icon="refresh" @click="load()">{{ t("common.retry") }}</YmButton>
      </template>
    </YmEmpty>

    <div v-else class="mcs-body">
      <section class="mcs-stage">
        <McsStage
          :source="playerSource"
          :playing="playback.playing"
          :mode="stageMode"
          :final-available="Boolean(finalAssetId)"
          :final-unavailable-label="stageFinalUnavailable"
          :source-label="t('mcs.stage.source')"
          :final-label="stageFinalLabel"
          :empty-title="t('mcs.preview')"
          :empty-description="stageEmptyDescription"
          :segments="stageSegments"
          :selected-id="selectedSegmentId"
          :current-ms="stageCurrentMs"
          :duration-ms="mediaDurationMs"
          :subtitle-text="stageSubtitleText"
          :subtitle-style="stageSubtitleStyle"
          :audition-label="stageAuditionLabel"
          :language-label="languageLabel"
          :duration-label="durationLabel"
          :segment-count="translationSegments.length"
          @update:playing="playback.playing = $event"
          @update:mode="setStageMode"
          @update:current-ms="stageCurrentMs = $event"
          @seek-segment="auditionSegment"
          @error="onPlaybackError"
        />
      </section>

      <aside class="mcs-panel">
        <nav class="mcs-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            :aria-selected="activeTab === 'subtitle'"
            :tabindex="activeTab === 'subtitle' ? 0 : -1"
            :class="{ 'is-active': activeTab === 'subtitle' }"
            @click="activeTab = 'subtitle'"
          >
            {{ t("mcs.tab.subtitle") }}
          </button>
          <button
            type="button"
            role="tab"
            :aria-selected="activeTab === 'style'"
            :tabindex="activeTab === 'style' ? 0 : -1"
            :class="{ 'is-active': activeTab === 'style' }"
            @click="activeTab = 'style'"
          >
            {{ t("mcs.tab.style") }}
          </button>
          <button
            type="button"
            role="tab"
            :aria-selected="activeTab === 'erase'"
            :tabindex="activeTab === 'erase' ? 0 : -1"
            :class="{ 'is-active': activeTab === 'erase' }"
            @click="activeTab = 'erase'"
          >
            {{ t("mcs.tab.erase") }}
          </button>
        </nav>

        <div class="mcs-panel__body">
          <SubtitlePanel
            v-if="activeTab === 'subtitle'"
            :rows="editorRows"
            :drafts="drafts"
            :errors="rowErrors"
            :selected-id="selectedSegmentId"
            :language-label="languageLabel"
            :languages="languageOptions"
            :language="selectedLanguage"
            :source-editable="Boolean(sourceTrack)"
            :playing-id="playingSegmentId"
            :auditionable="auditionableSegments"
            :actions="segmentActions"
            :expanded-id="expandedSegmentId"
            :candidates="expandedCandidates"
            :candidate-loading="candidateLoading"
            :candidate-error="candidateErrorText"
            :selected-candidate-id="expandedCandidateId"
            :playing-candidate-id="playingCandidateId"
            @select="selectedSegmentId = $event"
            @update:text="onSegmentText"
            @update:source-text="onSourceText"
            @update:time="onSegmentTime"
            @audition="auditionSegment"
            @merge="onMergeSegment"
            @split="onSplitSegment"
            @toggle-candidates="toggleCandidates"
            @select-language="selectedLanguage = $event"
            @generate-candidates="regenerateCandidates"
            @choose="chooseCandidate"
            @audition-candidate="auditionCandidate"
          />
          <StylePanel
            v-else-if="activeTab === 'style'"
            v-model="styleForm"
            :enabled="styleOverride"
            @update:enabled="styleOverride = $event"
          />
          <ErasePanel v-else :blocks="eraseBlocks" />
        </div>
      </aside>
    </div>

    <ShortcutHelp v-model="shortcutsOpen" />

    <YmDialog
      :model-value="leaveDialogOpen"
      :title="t('mcs.leave.title')"
      @update:model-value="onLeaveDialogVisibility"
    >
      <p class="mcs-leave__body">{{ t("mcs.leave.body") }}</p>
      <p v-if="hasErrors" class="mcs-leave__error" role="alert">
        {{ t("mcs.leave.errors") }}
      </p>
      <p v-else-if="leaveError" class="mcs-leave__error" role="alert">
        {{ leaveError }}
      </p>
      <template #footer>
        <YmButton variant="secondary" @click="cancelLeave">
          {{ t("mcs.leave.stay") }}
        </YmButton>
        <YmButton variant="danger" :disabled="saving" @click="discardAndLeave">
          {{ t("mcs.leave.discardAndLeave") }}
        </YmButton>
        <YmButton
          variant="primary"
          :loading="saving"
          :disabled="!canSave"
          @click="saveAndLeave"
        >
          {{ t("mcs.leave.saveAndLeave") }}
        </YmButton>
      </template>
    </YmDialog>

    <YmDialog
      :model-value="exportOpen"
      :title="t('pipeline.export.title')"
      @update:model-value="exportOpen = $event"
    >
      <McsExportPanel
        :project-id="projectId"
        :project-name="project?.name ?? ''"
        :run-id="exportRunId"
        :language="selectedLanguage"
        :language-options="languageOptions"
        :current-revision-id="exportRevisionId"
        :reviews="reviews"
        :reviews-loading="reviewsLoading"
        :review-read-failed="reviewReadFailed"
        :bilingual-available="bilingualAvailable"
        :dirty="dirty"
        :has-errors="hasErrors"
        :busy="saving"
        @update:language="selectedLanguage = $event"
      />
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.close") }}</YmButton>
      </template>
    </YmDialog>

    <RemixDialog
      :model-value="remixOpen"
      :remixing="remixBusy"
      :done="remixDone"
      :subtitle-changes="changedSegmentCount"
      :style-changed="styleDirty"
      :source-stale="sourceStale"
      :source-unknown="sourceUnknown"
      :language-label="languageLabel"
      :can-submit="remixDialogEnabled"
      :status-text="remixStatusText"
      :error-text="remixErrorText"
      @update:model-value="remixOpen = $event"
      @confirm="startRemix"
      @go-delivery="goDelivery"
    />
  </div>
</template>

<style scoped>
.mcs-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  background: var(--color-bg);
  /* 界面缩放桌面端用 Chromium 页面缩放、浏览器回退用 html zoom；两栏切换始终按实际可用宽度判断。 */
  container-type: inline-size;
  container-name: mcs;
}
.mcs-top {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-height: 60px;
  padding: 0 var(--page-gutter);
  background: var(--color-surface);
  border-block-end: 1px solid var(--color-border);
  flex: none;
}
.mcs-top__divider {
  width: 1px;
  height: 18px;
  flex: none;
  background: var(--color-border);
}
.mcs-top__id {
  flex: none;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.mcs-top__title {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.mcs-top__name {
  max-width: 360px;
  font-size: var(--font-size-lg);
  font-weight: 600;
  line-height: 20px;
}
.mcs-top__save {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.mcs-top__lang {
  flex: none;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
  font-size: var(--font-size-xs);
}
.mcs-top__controls {
  margin-inline-start: auto;
  display: flex;
  align-items: center;
  gap: var(--space-1);
  flex: none;
}
.mcs-top__actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
}
.mcs-top__remix {
  flex: none;
}
.mcs-top__remix-label {
  white-space: nowrap;
}
.mcs-top__export {
  flex: none;
}
.mcs-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.mcs-status__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-success);
}
.mcs-status.is-dirty .mcs-status__dot {
  background: var(--color-warning);
}
.mcs-status.is-saving .mcs-status__dot {
  background: var(--color-primary);
  animation: mcs-status-pulse var(--motion-duration-pulse) var(--motion-ease-in-out) infinite;
}
@keyframes mcs-status-pulse {
  50% {
    opacity: 0.35;
  }
}
.mcs-status.is-dirty {
  color: var(--color-warning-strong);
}
.mcs-status.is-saving {
  color: var(--color-primary);
}
.mcs-status.is-conflict,
.mcs-status.is-error {
  color: var(--color-error-strong);
}
.mcs-status.is-refresh-pending {
  color: var(--color-warning-strong);
}
.mcs-notice {
  margin: 0;
  padding: var(--space-2) var(--page-gutter, 24px);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.mcs-notice.mcs-source-warning {
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
}
.mcs-leave__body {
  margin: 0;
  color: var(--color-text);
  line-height: 1.6;
}
.mcs-leave__error {
  margin: var(--space-2) 0 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
.mcs-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-8);
  color: var(--color-text-muted);
}
.mcs-body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 440px;
  gap: var(--space-3);
  flex: 1;
  min-height: 0;
  padding: var(--space-4) var(--page-gutter, 24px);
}
.mcs-stage {
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
}
.mcs-panel {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
}
.mcs-tabs {
  display: flex;
  gap: 3px;
  flex: none;
  padding: 3px;
  margin: var(--space-3) var(--space-3) 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface-2);
}
.mcs-tabs button {
  flex: 1;
  min-width: 0;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  font-weight: 500;
  white-space: nowrap;
}
.mcs-tabs button.is-active {
  background: var(--color-surface);
  color: var(--color-primary-ink);
  box-shadow: var(--shadow-sm);
}
.mcs-tabs button:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 1px;
}
.mcs-panel__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  padding: var(--space-3);
}
/*
 * 可用内容宽度不足时（窄窗或 125%/150% 界面缩放）先收起次要的顶栏信息，
 * 保证保存栏、标题与操作仍可见且页面没有横向溢出。
 */
@container mcs (max-width: 900px) {
  .mcs-top {
    gap: var(--space-2);
  }
  .mcs-top__id,
  .mcs-top__lang {
    display: none;
  }
  .mcs-top__name {
    max-width: 200px;
  }
  .mcs-top__remix-label {
    display: none;
  }
}
/*
 * 可用内容宽度不足时（窄窗或 125%/150% 界面缩放）改为单列堆叠：
 * 舞台在上、面板在下，整体在内容区内滚动；保存栏仍在页头。
 * 保持 DOM 顺序，避免视觉顺序与阅读/焦点顺序不一致。
 * @media 在浏览器回退的 html zoom 下感知不到真实宽度，因此用容器查询；
 * 桌面 Chromium 页面缩放同样按容器实际宽度生效。
 */
@container mcs (max-width: 700px) {
  .mcs-body {
    grid-template-columns: minmax(0, 1fr);
    grid-auto-rows: max-content;
    align-content: start;
    overflow-y: auto;
  }
  .mcs-panel {
    overflow: visible;
  }
  .mcs-panel__body {
    overflow: visible;
  }
}
@media (min-width: 1600px) {
  .mcs-body {
    grid-template-columns: minmax(0, 1fr) 480px;
  }
}
</style>

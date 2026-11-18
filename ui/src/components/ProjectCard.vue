<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api } from "@/api";
import { formatDate, formatDuration, getLocale, t, tCount } from "@/i18n";
import { messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { ASR_LANGUAGES, TARGET_LANGUAGES, UI_LANGUAGES } from "@/domain/languages";
import { projectStatusBucket, type ProjectStatusBucket } from "@/domain/projects";
import type { Tone } from "@/domain/status";
import type {
  Asset,
  Project,
  ProjectSummary,
  RunStatus,
  WorkflowRun,
} from "@/domain/types";
import { targetLanguageName } from "@/domain/languages";
import { runOptionValueText } from "@/domain/runOptions";
import { projectTypeKeyFromMode } from "@/domain/projects";
import {
  buildProjectDownloadItems,
  type CardSimpleType,
  type DownloadGroup,
  type ProjectDownloadItem,
} from "./projectCardFiles";
import ProjectCover from "@/components/ProjectCover.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmIcon from "@/components/ui/YmIcon.vue";

const props = withDefaults(
  defineProps<{ project: ProjectSummary; correctionAvailable?: boolean }>(),
  { correctionAvailable: false },
);

const emit = defineEmits<{
  (e: "open"): void;
  (e: "correct"): void;
  (e: "delete"): void;
  (e: "rename"): void;
}>();

const TYPE_ICONS: Record<string, string> = {
  pipeline: "film",
  erase: "eraser",
  separation: "layers",
  extract: "subtitles",
  translate: "globe",
  clone: "mic",
  custom: "link",
  project: "folder",
};

const LANGUAGE_NAMES = new Map<string, string>();
for (const language of [...UI_LANGUAGES, ...ASR_LANGUAGES, ...TARGET_LANGUAGES]) {
  const name = language.nativeName ?? language.name;
  if (!LANGUAGE_NAMES.has(language.code)) LANGUAGE_NAMES.set(language.code, name);
}

const detail = ref<Project | undefined>();
const nowTick = ref(Date.now());
let tickTimer: number | undefined;

const ACTIVE_RUN_STATUSES: RunStatus[] = ["QUEUED", "RUNNING", "PAUSE_REQUESTED"];

onMounted(() => {
  tickTimer = window.setInterval(() => {
    nowTick.value = Date.now();
  }, 30000);
});
onBeforeUnmount(() => {
  if (tickTimer !== undefined) window.clearInterval(tickTimer);
  if (fileNoticeTimer !== undefined) window.clearTimeout(fileNoticeTimer);
});

const run = computed(() => latestRun(detail.value));

/** 摘要缺失最近运行状态时回退到详情里的最新 Run，旧 Core 也能显示右上角状态。 */
const statusValue = computed(() => props.project.latestRunStatus ?? run.value?.status);

/** 项目状态固定四态：待制作 / 制作中 / 已完成 / 制作失败。 */
const STATUS_BUCKET_TONES: Record<ProjectStatusBucket, Tone> = {
  pending: "neutral",
  running: "info",
  completed: "success",
  failed: "error",
};
const statusBucket = computed(() => projectStatusBucket(statusValue.value));
const statusTone = computed(() => STATUS_BUCKET_TONES[statusBucket.value]);
const statusLabel = computed(() => t(`projects.filter.status.${statusBucket.value}`));

/** 无 Run 且无 modeType 时类型未知：类型位留空，未配置放到右上角状态。 */
const hasKnownType = computed(() => Boolean(primaryRun.value || props.project.modeType));

/** 与网页端一致：封面右下展示源素材视频总时长，摘要缺失/偏大时从详情降级取。 */
const sourceDurationMs = computed(() => {
  const ready = (detail.value?.assets ?? []).filter((asset) => asset.status === "ready");
  const imported = ready.filter((asset) => !asset.runId);
  const pick =
    imported.find((asset) => asset.type === "video") ??
    imported.find((asset) => asset.type === "audio") ??
    imported.find((asset) => asset.type === "audio_track") ??
    ready.find((asset) => asset.type === "video") ??
    ready.find((asset) => asset.type === "audio") ??
    ready.find((asset) => asset.type === "audio_track");
  return pick?.meta?.durationMs ?? 0;
});
const durationMs = computed(
  () => sourceDurationMs.value || props.project.totalDurationMs || 0,
);

/** 全部 Run 的节点并集与语言并集，用于兼容同一项目的多次运行。 */
const allNodeKeys = computed(() => {
  const keys = new Set<string>();
  for (const r of detail.value?.runs ?? []) {
    for (const node of r.nodes) {
      if (node.status !== "skipped") keys.add(node.key);
    }
  }
  return keys;
});
const mergedLanguages = computed(() => {
  const langs: string[] = [];
  for (const r of detail.value?.runs ?? []) {
    for (const code of r.languages) if (!langs.includes(code)) langs.push(code);
  }
  return langs;
});
const hasCap = (key: string) => allNodeKeys.value.has(key);
const capTranslate = computed(() => hasCap("06_translate"));
const capClone = computed(() => hasCap("07_clone"));
const isProducing = computed(
  () => run.value !== undefined && ACTIVE_RUN_STATUSES.includes(run.value.status),
);

/** 粗粒度等待估算（分钟），对齐网页端口径：全链路 30 / 擦除 20 / 克隆 5 / 其他 3。 */
const waitMinutes = computed(() => {
  if (!run.value || !isProducing.value) return 0;
  const keys = new Set(run.value.nodes.map((node) => node.key));
  let total = 0;
  if (keys.has("06_translate") && keys.has("07_clone") && keys.has("08_mix")) total += 30;
  else {
    if (keys.has("05_erase")) total += 20;
    if (keys.has("07_clone")) total += 5;
    if (keys.has("03_separate") || keys.has("04_extract") || keys.has("06_translate")) total += 3;
  }
  if (total === 0) total = 3;
  const started = Date.parse(run.value.createdAt);
  const elapsedMin = Number.isFinite(started) ? (nowTick.value - started) / 60000 : 0;
  return Math.max(0, Math.ceil(total - elapsedMin));
});

/** 目标语言 chips：只用 Run 语言/多次 Run 语言并集，不退回项目轨道。 */
const targetLangChips = computed(() => {
  const langs = run.value?.languages.length ? run.value.languages : mergedLanguages.value;
  return langs.map((code) => ({ code, label: targetLanguageName(code, getLocale()) }));
});

/** und/auto 等是“未判定”占位，不是可展示语言；分离、擦除等无语言任务不应出现语言行。 */
const UNKNOWN_LANGUAGE_CODES = new Set(["", "und", "undetermined", "auto", "unknown", "zxx"]);

function meaningfulLanguage(code?: string): string {
  const value = (code ?? "").trim();
  return UNKNOWN_LANGUAGE_CODES.has(value.toLowerCase()) ? "" : value;
}

const sourceLanguageCode = computed(() => {
  const runSource = meaningfulLanguage(primaryRun.value?.sourceLanguage);
  if (runSource) return runSource;
  const tracks = detail.value?.tracks ?? [];
  return meaningfulLanguage(
    tracks.find((track) => track.type === "transcription")?.language ??
      tracks.find((track) => track.type !== "translation")?.language,
  );
});

/**
 * 只展示目标语言，且只展示一次；源语言在提取任务的工具参数里展示。
 * 标签按能力取“配音语言/译制语言/目标语言”，与网页端口径一致。
 */
const targetRowLabelKey = computed(() => {
  const cloneType = primaryTypeKey.value === "clone";
  if (capTranslate.value && (capClone.value || cloneType)) return "workbench.card.langBoth";
  if (capClone.value || cloneType) return "workbench.card.langDub";
  if (capTranslate.value) return "workbench.card.langTranslate";
  return "workbench.card.targets";
});

/** 语言数量行对齐网页端：只在有目标语言的翻译/克隆类卡片出现。 */
const languageCountRow = computed(() => {
  if (!targetLangChips.value.length) return null;
  const roleKey = targetRowLabelKey.value;
  return {
    label: `${t(roleKey)}${t("workbench.card.langCountSuffix")}`,
    value: tCount("workbench.card.langCountShort", targetLangChips.value.length),
  };
});

function paramOf<K extends keyof NonNullable<WorkflowRun["params"]>>(
  key: K,
): NonNullable<WorkflowRun["params"]>[K] | undefined {
  return run.value?.params?.[key];
}

const capErase = computed(
  () => allNodeKeys.value.has("05_erase") || Boolean(run.value?.modeType?.includes("erase")),
);

const detailRows = computed<
  { key: string; labelKey: string; value: string; warning?: boolean }[]
>(() => {
  const rows: {
    key: string;
    labelKey: string;
    value: string;
    warning?: boolean;
  }[] = [];
  const eraseMode = paramOf("eraseMode");
  if (primaryTypeKey.value === "pipeline") {
    rows.push({
      key: "adjust",
      labelKey: "workbench.card.adjustDuration",
      value: runOptionValueText("adjustDuration", paramOf("adjustDuration")),
      warning: paramOf("adjustDuration") === true,
    });
    if (capErase.value && eraseMode) {
      rows.push({
        key: "erase",
        labelKey: "workbench.card.eraseMode",
        value: t(
          eraseMode === "dialog_and_art"
            ? "workbench.card.eraseArt"
            : "workbench.card.eraseDialog",
        ),
      });
    }
  } else if (capErase.value && eraseMode) {
    rows.push({
      key: "erase",
      labelKey: "workbench.card.eraseMode",
      value: t(
        eraseMode === "dialog_and_art"
          ? "workbench.card.eraseArt"
          : "workbench.card.eraseDialog",
      ),
    });
  }
  if ((capClone.value || primaryTypeKey.value === "clone") && primaryTypeKey.value !== "pipeline") {
    rows.push({
      key: "timestamp",
      labelKey: "workbench.card.timestampCheck",
      value: runOptionValueText(
        "validateTimestamps",
        paramOf("validateTimestamps") ?? (run.value ? undefined : false),
      ),
    });
    rows.push({
      key: "clone-adjust",
      labelKey: "workbench.card.adjustDuration",
      value: runOptionValueText(
        "adjustDuration",
        paramOf("adjustDuration") ?? (run.value ? undefined : false),
      ),
      warning: paramOf("adjustDuration") === true,
    });
  }
  if (primaryTypeKey.value === "extract") {
    const extractMode =
      paramOf("extractMode") ??
      (props.project.modeType?.includes("ocr") ? "ocr" : "asr");
    rows.push({
      key: "extract-mode",
      labelKey: "workbench.card.extractMode",
      value: t(
        extractMode === "ocr"
          ? "workbench.card.extractOcr"
          : extractMode === "asr_ocr"
            ? "workbench.card.extractBoth"
            : "workbench.card.extractAsr",
      ),
    });
    const source = paramOf("asrLanguage") ?? sourceLanguageCode.value;
    if (source) {
      rows.push({
        key: "extract-source",
        labelKey: "workbench.card.asrLanguage",
        value: langName(source),
      });
    }
  }
  return rows;
});

const packedDetails = computed<{
  key: string;
  labelKey: string;
  value: string;
  warning?: boolean;
  full: boolean;
}[]>(() => detailRows.value.map((row) => ({ ...row, full: false })));

/** 仅真正制作中的状态禁止删除；已完成项目仍可删除。 */
const isDeleteBlocked = computed(
  () =>
    !!run.value &&
    ["QUEUED", "RUNNING", "PAUSE_REQUESTED", "PAUSED"].includes(run.value.status),
);

/** 克隆类不显示重试（对齐文档第六节）。 */
const hasTranslationTrack = computed(() =>
  (detail.value?.tracks ?? []).some((track) => track.type === "translation"),
);

const showCorrect = computed(
  () =>
    props.correctionAvailable &&
    (props.project.latestRunStatus === "WAITING_REVIEW" ||
      props.project.latestRunStatus === "SUCCEEDED") &&
    hasTranslationTrack.value,
);

/** 单点能力判定（用于下载门控，对齐文档第五节）。capErase 复用上方 modeType 增强版。 */
const capSep = computed(() => hasCap("03_separate"));
const capMix = computed(() => hasCap("08_mix"));
const hasAnyCap = computed(() => allNodeKeys.value.size > 0);
const capIsFullPipeline = computed(
  () => hasCap("06_translate") && hasCap("07_clone") && hasCap("08_mix"),
);
const pureTranslate = computed(
  () => capTranslate.value && !capErase.value && !capSep.value && !capMix.value && !capClone.value,
);
const pureClone = computed(
  () => capClone.value && !capErase.value && !capSep.value && !capTranslate.value && !capMix.value,
);
const pureExtract = computed(
  () =>
    hasCap("04_extract") &&
    !capTranslate.value &&
    !capClone.value &&
    !capErase.value &&
    !capSep.value &&
    !capMix.value,
);
/** 原片：字幕翻译/克隆配音不提供（文档：克隆无原片入口；翻译仅字幕）。 */
const allowVideoDownload = computed(() => !pureTranslate.value && !pureClone.value);
/** 分离音频：音分/克隆/全链路提供。 */
const allowAudioDownload = computed(
  () => !hasAnyCap.value || capSep.value || capClone.value || capIsFullPipeline.value,
);

/** 下载/预览的能力分组门控；条目身份与文件名统一由 projectCardFiles 构造。 */
const allowedDownloadGroups = computed<DownloadGroup[]>(() => {
  if (!hasAnyCap.value) return ["video", "audio", "subtitle"];
  const groups: DownloadGroup[] = ["subtitle"];
  if (allowVideoDownload.value) groups.push("video");
  if (allowAudioDownload.value) groups.push("audio");
  return groups;
});

/** 编号优先用本地项目序号（1、2、3…）；旧 Core 无序号时回退尾 4 位短 ID。 */
const displayId = computed(() => {
  if (props.project.seq) return String(props.project.seq);
  return (
    (props.project.episodeId || props.project.id)
      .replace(/[^a-zA-Z0-9]/g, "")
      .slice(-4)
      .toUpperCase() || "——"
  );
});

function requestDelete(): void {
  if (isDeleteBlocked.value) return;
  emit("delete");
}

/**
 * 素材只统计用户导入的源文件，不含 Core 生成的产物。
 * 新 Core 用 Asset.run_id 区分：导入源文件没有 run_id；旧 Core 缺省时退回全部资产。
 */
const sourceAssets = computed(() => {
  const list = (detail.value?.assets ?? []).filter((asset) =>
    ["video", "audio", "audio_track", "subtitle"].includes(asset.type),
  );
  const imported = list.filter((asset) => !asset.runId);
  return imported.length ? imported : list;
});

/** 素材行文案：「N个MP4 / N个WAV / N个SRT」，只含源文件。 */
const materialText = computed(() => {
  const list = sourceAssets.value;
  if (!list.length) return "—";
  const byExt = new Map<string, number>();
  for (const asset of list) {
    const i = asset.name.lastIndexOf(".");
    const ext = i > 0 ? asset.name.slice(i + 1).toUpperCase() : "FILE";
    byExt.set(ext, (byExt.get(ext) ?? 0) + 1);
  }
  return [...byExt.entries()].map(([ext, n]) => `${n}${t("workbench.card.mat.unit")}${ext}`).join(" / ");
});
const mediaId = ref<string | undefined>();
const fileNotice = ref<SafeNotice | null>(null);
const fileNoticeText = computed(() => renderNotice(fileNotice.value));
let fileNoticeTimer: number | undefined;

function showFileNotice(message: SafeNotice): void {
  fileNotice.value = message;
  if (fileNoticeTimer !== undefined) window.clearTimeout(fileNoticeTimer);
  fileNoticeTimer = window.setTimeout(() => {
    fileNotice.value = null;
  }, 4000);
}


async function loadDetail(): Promise<void> {
  try {
    const next = await api.getProject(props.project.id);
    detail.value = next;
    mediaId.value = next.assets.find(
      (asset) => asset.type === "video" && asset.status === "ready",
    )?.id;
  } catch {
    detail.value = undefined;
    mediaId.value = undefined;
  }
}

watch(
  () => [props.project.id, props.project.updatedAt],
  () => {
    void loadDetail();
  },
  { immediate: true },
);

function langName(code: string): string {
  return LANGUAGE_NAMES.get(code) ?? code;
}

function latestRun(project?: Project): WorkflowRun | undefined {
  return project?.runs.slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0];
}

function runTypeKey(run?: WorkflowRun): string {
  if (!run) return projectTypeKeyFromMode(props.project.modeType) ?? "project";
  const fromMode = projectTypeKeyFromMode(run.modeType);
  if (fromMode) return fromMode;
  const nodes = new Set(run.nodes.map((node) => node.key));
  if (nodes.has("06_translate") && nodes.has("07_clone") && nodes.has("08_mix")) {
    return "pipeline";
  }
  const types: string[] = [];
  if (nodes.has("03_separate")) types.push("separation");
  if (nodes.has("04_extract")) types.push("extract");
  if (nodes.has("05_erase")) types.push("erase");
  if (nodes.has("06_translate")) types.push("translate");
  if (nodes.has("07_clone")) types.push("clone");
  if (nodes.has("08_mix")) types.push("mix");
  if (types.length === 0) return "project";
  if (types.length > 1) return "custom";
  return types[0] === "mix" ? "custom" : types[0];
}

const primaryRun = computed(() => latestRun(detail.value));
const primaryTypeKey = computed(() => runTypeKey(primaryRun.value));
const primaryTypeLabel = computed(() => t(`workbench.type.${primaryTypeKey.value}`));
const primaryTypeIcon = computed(() => TYPE_ICONS[primaryTypeKey.value] ?? "folder");

function canPreview(asset: Asset): boolean {
  return ["video", "audio", "audio_track", "subtitle", "image"].includes(asset.type);
}

function assetContentUrl(id: string): string {
  return `yaomu-asset://asset/${encodeURIComponent(id)}`;
}

/** 单点工具按清单限制可见产物。 */
const simpleType = computed<CardSimpleType>(() => {
  const key = primaryTypeKey.value;
  return (["erase", "separation", "translate", "clone", "extract"].includes(key)
    ? key
    : "") as CardSimpleType;
});

/** 下载与预览共用同一批条目身份；文件名由纯 helper 统一去重。 */
const downloadItems = computed(() =>
  buildProjectDownloadItems({
    projectName: props.project.name,
    assets: detail.value?.assets ?? [],
    simpleType: simpleType.value,
    allowedGroups: allowedDownloadGroups.value,
  }),
);

const productPreviewOpen = ref(false);
const activeProductId = ref("");
const productText = ref("");
const productError = ref<SafeNotice | null>(null);
const productErrorText = computed(() => renderNotice(productError.value));
const previewTabs = computed(() =>
  downloadItems.value.filter((item) => canPreview(item.asset)),
);
const activeProduct = computed(
  () => previewTabs.value.find((item) => item.asset.id === activeProductId.value),
);
const productPreviewTitle = computed(() => {
  if (primaryTypeKey.value === "clone") return t("workbench.card.previewAudio");
  if (primaryTypeKey.value === "translate") return t("workbench.card.previewSubtitle");
  return t("workbench.card.previewVideo");
});

function openProductPreview(): void {
  const first = previewTabs.value[0];
  if (!first) return;
  productPreviewOpen.value = true;
  void selectProduct(first);
}

async function selectProduct(item: ProjectDownloadItem): Promise<void> {
  activeProductId.value = item.asset.id;
  productText.value = "";
  productError.value = null;
  if (item.asset.type !== "subtitle") return;
  try {
    const response = await fetch(assetContentUrl(item.asset.id));
    if (!response.ok) throw new Error(String(response.status));
    productText.value = await response.text();
  } catch {
    productError.value = messageNotice("workbench.card.previewFailed");
  }
}

function closeProductPreview(): void {
  productPreviewOpen.value = false;
  activeProductId.value = "";
  productText.value = "";
  productError.value = null;
}

function productPreviewFailed(): void {
  productError.value = messageNotice("workbench.card.previewFailed");
}

const downloadDialogOpen = ref(false);

function openDownload(): void {
  if (!downloadItems.value.length) return;
  if (downloadItems.value.length === 1) {
    void downloadOne(downloadItems.value[0]);
    return;
  }
  downloadDialogOpen.value = true;
}

function closeDownloadDialog(): void {
  downloadDialogOpen.value = false;
}

async function downloadOne(item: ProjectDownloadItem): Promise<void> {
  downloadDialogOpen.value = false;
  await downloadAsset(item.asset, item.filename);
}

async function downloadAsset(asset: Asset, filename?: string): Promise<void> {
  fileNotice.value = null;
  const name = filename ?? asset.name;
  try {
    // 复用 api.saveAsset：内部处理桌面桥取消 null 与失败安全错误。
    const saved = await api.saveAsset({ assetId: asset.id, suggestedName: name });
    if (saved) showFileNotice(messageNotice("workbench.card.downloaded", { name }));
  } catch {
    showFileNotice(messageNotice("workbench.card.downloadFailed"));
  }
}
</script>

<template>
  <article class="recent-card">
    <ProjectCover
      :asset-id="mediaId"
      @click="emit('open')"
    >
      <span v-if="hasKnownType" class="recent-card__type">
        <YmIcon :name="primaryTypeIcon" :size="13" />
        {{ primaryTypeLabel }}
      </span>
      <YmBadge class="recent-card__status" solid :tone="statusTone">
        {{ statusLabel }}
      </YmBadge>

      <span
        class="recent-card__pill recent-card__pill--date"
        :title="`${t('workbench.card.updated')} ${formatDate(project.updatedAt)}`"
      >
        <bdi dir="ltr">{{ formatDate(project.createdAt) }}</bdi>
      </span>
      <span v-if="durationMs" class="recent-card__pill recent-card__pill--duration">
        <YmIcon name="play-round" :size="11" />
        <bdi dir="ltr">{{ formatDuration(durationMs) }}</bdi>
      </span>

      <button
        v-if="!isProducing && previewTabs.length"
        type="button"
        class="recent-card__play"
        :class="{ 'is-always': statusBucket === 'completed' }"
        :aria-label="productPreviewTitle"
        @click.stop="openProductPreview"
      >
        <YmIcon name="play-round" :size="22" />
      </button>

      <div v-if="isProducing" class="recent-card__producing">
        <span class="recent-card__producing-ring" aria-hidden="true"></span>
        <p class="recent-card__producing-text">
          {{
            project.waitingResource
              ? t("state.waitingResource")
              : waitMinutes > 0
                ? t("workbench.card.producing", { minutes: waitMinutes })
                : t("workbench.card.soonDone")
          }}
        </p>
      </div>
    </ProjectCover>

    <div class="recent-card__body" @click="emit('open')">
      <div class="recent-card__title-row">
        <span class="recent-card__id-pill" :title="project.episodeId || project.id">
          {{ t("workbench.card.id") }}:{{ displayId }}
        </span>
        <button
          type="button"
          class="recent-card__name"
          :title="project.name"
          @click="emit('open')"
        >{{ project.name }}</button>
      </div>

      <div class="recent-card__info">
        <div class="recent-card__detail recent-card__detail--full">
          <span class="recent-card__detail-label">{{ t("workbench.card.assets") }}：</span>
          <span class="recent-card__detail-value" :title="materialText"><bdi dir="ltr">{{ materialText }}</bdi></span>
        </div>
        <div
          v-if="languageCountRow"
          class="recent-card__detail recent-card__detail--full"
          data-testid="recent-card-lang-count"
        >
          <span class="recent-card__detail-label">{{ languageCountRow.label }}：</span>
          <span class="recent-card__detail-value">{{ languageCountRow.value }}</span>
        </div>
        <div
          v-for="row in packedDetails"
          :key="row.key"
          class="recent-card__detail"
          :class="{ 'recent-card__detail--full': row.full }"
        >
          <span class="recent-card__detail-label">{{ t(row.labelKey) }}：</span>
          <span
            class="recent-card__detail-value"
            :class="{ 'recent-card__detail-value--warning': row.warning }"
            :title="row.value"
          ><bdi>{{ row.value }}</bdi></span>
        </div>
        <div
          v-if="targetLangChips.length"
          class="recent-card__detail recent-card__detail--full recent-card__detail--languages"
        >
          <span class="recent-card__detail-label">{{ t(targetRowLabelKey) }}：</span>
          <span class="recent-card__lang-list">
            <span v-for="lang in targetLangChips" :key="lang.code" class="recent-card__lang-chip">
              {{ lang.label }}
            </span>
          </span>
        </div>
      </div>

      <p v-if="fileNoticeText" class="file-notice" role="status"><bdi>{{ fileNoticeText }}</bdi></p>
      <div class="recent-card__foot" @click.stop @keydown.stop>
        <YmButton
          v-if="showCorrect"
          size="sm"
          variant="ghost"
          class="recent-card__enter"
          :title="t('projects.correct')"
          @click="emit('correct')"
        >
          {{ t("projects.correct") }}
        </YmButton>
        <YmButton
          v-if="downloadItems.length"
          size="sm"
          variant="ghost"
          class="recent-card__download"
          :title="t('workbench.card.download')"
          :aria-label="t('workbench.card.download')"
          @click="openDownload"
        >
          {{ t("workbench.card.download") }}
        </YmButton>
        <YmButton
          size="sm"
          variant="ghost"
          class="recent-card__rename"
          :title="t('projects.rename.action')"
          @click="emit('rename')"
        >
          {{ t("common.edit") }}
        </YmButton>
        <YmButton
          size="sm"
          variant="ghost"
          class="recent-card__delete"
          :title="t('common.delete')"
          :disabled="isDeleteBlocked"
          @click="requestDelete"
        >
          {{ t("common.delete") }}
        </YmButton>
      </div>
    </div>

    <YmDialog
      :model-value="downloadDialogOpen"
      :title="t('workbench.card.downloadTitle')"
      @update:model-value="closeDownloadDialog"
    >
      <div class="download-list">
        <button
          v-for="item in downloadItems"
          :key="item.asset.id"
          type="button"
          class="download-row"
          @click="downloadOne(item)"
        >
          <YmIcon
            :name="item.group === 'video' ? 'film' : item.group === 'audio' ? 'volume' : 'subtitles'"
            :size="16"
          />
          <span class="download-row__main">
            <span class="download-row__label"><bdi dir="auto">{{ item.label }}</bdi></span>
            <span class="download-row__name" :title="item.filename"><bdi dir="auto">{{ item.filename }}</bdi></span>
          </span>
          <span class="download-row__ext mono"><bdi dir="ltr">{{ item.extension }}</bdi></span>
        </button>
      </div>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.close") }}</YmButton>
      </template>
    </YmDialog>

    <YmDialog
      :model-value="productPreviewOpen"
      :title="productPreviewTitle"
      @update:model-value="closeProductPreview"
    >
      <div class="product-preview">
        <div class="product-preview__tabs">
          <button
            v-for="item in previewTabs"
            :key="item.asset.id"
            type="button"
            class="product-preview__tab"
            :class="{ 'is-active': activeProductId === item.asset.id }"
            @click="selectProduct(item)"
          >
            <bdi dir="auto">{{ item.previewLabel }}</bdi>
          </button>
        </div>
        <div v-if="activeProduct" class="asset-preview">
          <video
            v-if="activeProduct.asset.type === 'video'"
            :src="assetContentUrl(activeProduct.asset.id)"
            controls
            autoplay
            @error="productPreviewFailed"
          ></video>
          <audio
            v-else-if="
              activeProduct.asset.type === 'audio' ||
              activeProduct.asset.type === 'audio_track'
            "
            :src="assetContentUrl(activeProduct.asset.id)"
            controls
            autoplay
            @error="productPreviewFailed"
          ></audio>
          <img
            v-else-if="activeProduct.asset.type === 'image'"
            :src="assetContentUrl(activeProduct.asset.id)"
            alt=""
            @error="productPreviewFailed"
          />
          <pre v-else-if="activeProduct.asset.type === 'subtitle'">
            {{ productText || t("common.loading") }}
          </pre>
          <p v-if="productErrorText" class="asset-preview__error"><bdi>{{ productErrorText }}</bdi></p>
        </div>
        <p v-else class="file-empty">{{ t("workbench.card.previewEmpty") }}</p>
      </div>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.close") }}</YmButton>
      </template>
    </YmDialog>

  </article>
</template>

<style scoped>
.recent-card {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border-radius: 14px;
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  box-shadow: var(--shadow-sm);
  cursor: pointer;
  transform-origin: center;
  transition:
    transform var(--motion-duration-base) var(--motion-ease-out),
    border-color var(--motion-duration-base) var(--motion-ease-out),
    box-shadow var(--motion-duration-base) var(--motion-ease-out);
}
.recent-card:hover {
  transform: translateY(-1px);
  border-color: var(--color-border-strong);
  box-shadow: var(--shadow-md);
}
.recent-card:active {
  transform: translateY(0) scale(0.995);
}
/* 卡片宿主限制封面高度：ProjectCover 仍按 16:9 取帧并以 cover 裁切，
   这里只压缩卡片内展示高度，避免宽卡把底部操作挤出首屏。 */
.recent-card :deep(.project-cover) {
  height: 160px;
}
.recent-card__type {
  position: absolute;
  top: 10px;
  left: 10px;
  z-index: 5;
  pointer-events: none;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: calc(100% - 90px);
  padding: 6px 10px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: var(--radius-pill);
  background: color-mix(in srgb, var(--color-primary) 82%, transparent);
  color: var(--color-on-primary);
  font-size: 11px;
  font-weight: 500;
  line-height: 1.25;
  backdrop-filter: blur(6px);
}
.recent-card__status {
  position: absolute;
  top: 10px;
  right: 10px;
  /* 制作中遮罩 z-index: 3；状态标签必须始终置顶。 */
  z-index: 6;
}
.recent-card__body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  padding: 14px;
}
.recent-card__details {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 12px;
  margin: 0;
}
.recent-card__details > div {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}
.recent-card__details dt {
  flex: none;
  color: var(--color-text-faint);
  font-size: 10px;
  line-height: 1.3;
  white-space: nowrap;
}
.recent-card__details dd {
  min-width: 0;
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.4;
}
.recent-card__detail-wide {
  grid-column: 1 / -1;
}
.recent-card__foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 4px 8px;
  margin-top: auto;
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
  min-width: 0;
}
.recent-card__foot :deep(.ym-btn--sm) {
  padding: 0 4px;
}
.recent-card__download {
  padding: 0 6px;
}
.recent-card__rename {
  color: var(--color-text-muted);
}
.recent-card__rename:hover:not(:disabled) {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.recent-card__delete {
  color: var(--color-error-strong);
}
.recent-card__delete:hover:not(:disabled) {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.recent-card__enter {
  color: var(--color-primary-ink);
}
.recent-card__enter:hover:not(:disabled) {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}
.download-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 52vh;
  overflow-y: auto;
}
.download-row {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 10px;
  border-radius: var(--radius-md);
  text-align: start;
}
.download-row:hover {
  background: var(--color-surface-2);
}
.download-row__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.download-row__label {
  min-width: 0;
  color: var(--color-text);
  font-size: var(--font-size-sm);
  /* 长无空格文件名（导入原名）在名称列内换行，不能横向盖住扩展名列。 */
  overflow-wrap: anywhere;
  word-break: break-word;
}
.download-row__name {
  overflow: hidden;
  color: var(--color-text-faint);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.download-row__ext {
  flex: none;
  color: var(--color-text-faint);
  font-size: 10px;
}
.file-empty {
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
}
.file-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 52vh;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}
.file-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 0;
  border-bottom: 1px solid var(--color-border);
}
.file-row:last-child {
  border-bottom: none;
}
.file-row__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  border-radius: 8px;
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.file-row__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.file-row__name {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.file-row__meta {
  color: var(--color-text-faint);
  font-size: 10px;
}
.file-row__actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 2px;
}
.file-notice {
  margin: var(--space-2) 0 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.product-preview {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.product-preview__tabs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1);
}
.product-preview__tab {
  padding: 4px 10px;
  border-radius: var(--radius-pill);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.product-preview__tab:hover {
  color: var(--color-primary);
}
.product-preview__tab.is-active {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.asset-preview {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.asset-preview video {
  width: 100%;
  aspect-ratio: 16 / 9;
  border-radius: var(--radius-md);
  background: var(--color-media);
}
.asset-preview audio {
  width: 100%;
}
.asset-preview img {
  width: 100%;
  border-radius: var(--radius-md);
}
.asset-preview pre {
  max-height: 52vh;
  margin: 0;
  overflow: auto;
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text);
  font-size: var(--font-size-xs);
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}
.asset-preview__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
.recent-card__pill {
  position: absolute;
  bottom: 8px;
  z-index: 5;
  pointer-events: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  gap: 4px;
  border-radius: var(--radius-pill);
  background: rgba(0, 0, 0, 0.35);
  color: var(--color-on-primary);
  font-size: var(--font-size-xs);
  line-height: 1.5;
  backdrop-filter: blur(4px);
}
.recent-card__pill--date {
  left: 8px;
}
.recent-card__pill--duration {
  right: 8px;
}
.recent-card__play {
  position: absolute;
  top: 50%;
  left: 50%;
  z-index: 3;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 52px;
  height: 52px;
  border-radius: 50%;
  background: rgba(0, 0, 0, 0.55);
  color: #fff;
  backdrop-filter: blur(4px);
  opacity: 0;
  transform: translate(-50%, -50%) scale(0.9);
  transition: opacity var(--motion-duration-base) var(--motion-ease-out), transform var(--motion-duration-base) var(--motion-ease-out);
}
.recent-card:hover .recent-card__play,
.recent-card__play.is-always {
  opacity: 1;
  transform: translate(-50%, -50%) scale(1);
}
.recent-card__producing {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  background: linear-gradient(
    160deg,
    rgba(0, 22, 56, 0.55) 0%,
    rgba(0, 30, 80, 0.65) 50%,
    rgba(0, 18, 50, 0.75) 100%
  );
  backdrop-filter: blur(2px);
}
.recent-card__producing::before {
  content: "";
  position: absolute;
  inset: -20%;
  background: radial-gradient(
    circle at 50% 45%,
    color-mix(in srgb, var(--color-primary) 45%, transparent) 0%,
    transparent 62%
  );
  opacity: 0.35;
  pointer-events: none;
  z-index: -1;
  animation: card-ambient var(--motion-duration-ambient) var(--motion-ease-out) infinite;
}
.recent-card__producing-ring {
  position: relative;
  width: 44px;
  height: 44px;
  border-radius: 50%;
  border: 3px solid rgba(255, 255, 255, 0.25);
  border-top-color: #fff;
  animation: card-spin var(--motion-duration-spin) linear infinite;
}
@keyframes card-spin {
  to {
    transform: rotate(360deg);
  }
}
@keyframes card-ambient {
  0%,
  100% {
    opacity: 0.25;
  }
  50% {
    opacity: 0.65;
  }
}
@media (prefers-reduced-motion: reduce) {
  .recent-card__producing::before {
    animation: none;
    opacity: 0.45;
  }
}
.recent-card__producing-text {
  color: #fff;
  font-size: var(--font-size-xs);
  font-weight: 500;
}
.recent-card__title-row {
  display: flex;
  /* ID 胶囊对齐标题第一行，而不是在两行标题中垂直居中。 */
  align-items: flex-start;
  gap: var(--space-2);
  min-width: 0;
}
.recent-card__id-pill {
  flex: none;
  margin-top: 1px;
  padding: 1px 6px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-sm);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.5;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.recent-card__name {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  padding: 0;
  border: none;
  background: transparent;
  font-size: var(--font-size-md);
  font-weight: 600;
  color: var(--color-text);
  line-height: 1.4;
  text-align: start;
  /* 名称含长无空格词时在卡片列内换行，不能被标题行撑出横向溢出。 */
  overflow-wrap: anywhere;
  word-break: break-word;
}
.recent-card__name:hover {
  color: var(--color-primary);
}
.recent-card__body {
  cursor: pointer;
}
.recent-card__detail--id {
  flex-basis: 100%;
}
.recent-card__detail--id .recent-card__detail-label,
.recent-card__detail--id .recent-card__detail-value {
  font-size: 10px;
}
.recent-card__info {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
}
.recent-card__detail {
  display: inline-flex;
  align-items: baseline;
  flex: 0 0 auto;
  gap: 2px;
  min-width: 0;
  max-width: 100%;
  white-space: nowrap;
}
.recent-card__detail--full {
  flex-basis: 100%;
}
.recent-card__detail-label {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.55;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.recent-card__detail-value {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.55;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.recent-card__detail-value--warning {
  color: var(--color-warning-strong);
}
.recent-card__detail--languages {
  align-items: center;
  white-space: normal;
}
.recent-card__lang-list {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 6px;
}
.recent-card__lang-chip {
  display: inline-flex;
  align-items: center;
  min-height: 20px;
  max-width: 100%;
  padding: 0 6px;
  border: 1px solid color-mix(in srgb, var(--color-primary) 20%, transparent);
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-primary) 6%, var(--color-surface));
  color: var(--color-primary-ink);
  font-size: var(--font-size-xs);
  font-weight: 500;
  line-height: 18px;
  white-space: nowrap;
}
.dl-menu {
  position: relative;
}
.dl-menu__trigger {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 26px;
  padding: 0 8px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  transition: color var(--motion-duration-snap) var(--motion-ease-out), background var(--motion-duration-snap) var(--motion-ease-out);
}
.dl-menu__trigger:hover:not(:disabled) {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.dl-menu__trigger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.dl-menu__panel {
  position: absolute;
  bottom: calc(100% + 6px);
  right: 0;
  z-index: 40;
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 180px;
  max-height: 280px;
  overflow-y: auto;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  box-shadow: var(--shadow-lg);
}
.dl-menu--download .dl-menu__panel {
  right: auto;
  left: 0;
  width: 200px;
}
.dl-menu__item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 7px 10px;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  text-align: start;
}
.dl-menu__item:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.dl-menu__item--all {
  margin-bottom: 2px;
  border-bottom: 1px solid var(--color-border);
  border-radius: var(--radius-md) var(--radius-md) 0 0;
  color: var(--color-primary);
  font-weight: 500;
}
.dl-menu__ext {
  margin-inline-start: auto;
  color: var(--color-text-faint);
  font-size: 10px;
}
.dl-menu__item--danger {
  color: var(--color-error-strong);
}
.dl-menu__item--danger:hover {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.dl-menu__item:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.dl-menu__item:disabled:hover {
  background: transparent;
  color: var(--color-error-strong);
}
.dl-menu__empty {
  margin: 0;
  padding: var(--space-2);
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
</style>

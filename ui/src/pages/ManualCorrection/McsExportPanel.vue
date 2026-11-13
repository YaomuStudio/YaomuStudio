<script setup lang="ts">
// 人工校正 · 导出面板（UI 分支多选交互 + main 的逐类型交付门禁）。
//
// 输出目录固定为桌面主进程授权的 <程序目录>/outputs；命名模板只认 Core 的
// {project}/{language}/{timestamp}；字幕冻结当前已保存 revision（双语仅 SRT），
// 视频/音频绑定当前 revision 的成功 08 交付门槛。多选后逐项导出、逐项轮询；
// 一项失败不回滚已成功项，也不宣称原子。
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { t } from "@/i18n";
import { messageNotice, errorNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { api } from "@/api";
import { deliveryReviewView } from "@/pages/Pipeline/reviewBinding";
import type { SelectOption } from "@/pages/Pipeline/types";
import { taskStatusLabelKey, taskStatusTone, type Tone } from "@/domain/status";
import { useDefaultOutputDir } from "@/composables/useDefaultOutputDir";
import type {
  ExportFormat,
  ExportKind,
  ExportRecord,
  ReviewGate,
} from "@/domain/types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmField from "@/components/ui/YmField.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";

const props = defineProps<{
  projectId: string;
  projectName: string;
  /** 当前语种最近一次 Run（含终态）；已审核内容的终态导出仍可用。 */
  runId?: string;
  language: string;
  languageOptions: SelectOption[];
  /** 当前已保存的译文 revision；字幕导出必须冻结它。 */
  currentRevisionId?: string;
  reviews: ReviewGate[];
  reviewsLoading: boolean;
  reviewReadFailed: boolean;
  bilingualAvailable: boolean;
  dirty: boolean;
  hasErrors: boolean;
  /** 保存等写入进行中：与 Pipeline 一样先等待。 */
  busy: boolean;
}>();

const emit = defineEmits<{
  (e: "update:language", v: string): void;
}>();

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const TERMINAL_EXPORT_STATUS = new Set<ExportRecord["status"]>([
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
  "INTERRUPTED",
]);
const DEFAULT_NAMING = "{project}_{language}_{timestamp}";
const TEMPLATE_TOKENS = ["{project}", "{language}", "{timestamp}"] as const;

const { outputDir, resolve: resolveOutputDir } = useDefaultOutputDir();
const selectedKinds = ref<ExportKind[]>([]);
const bilingual = ref(false);
const naming = ref(DEFAULT_NAMING);
const exporting = ref(false);
const notice = ref<SafeNotice | null>(null);
const noticeTone = ref<"info" | "error">("info");
const noticeText = computed(() => renderNotice(notice.value));
const jobs = ref<ExportJob[]>([]);
const jobDirectory = ref("");

type JobStatus = "queued" | "running" | "succeeded" | "failed";
interface ExportJob {
  kind: ExportKind;
  status: JobStatus;
  record?: ExportRecord;
  error?: SafeNotice;
  /** 本地已翻译的阻断原因；不是 SafeNotice 键，按原样显示。 */
  errorText?: string;
}

let exportGeneration = 0;
let disposed = false;

const kindOptions = computed<SelectOption[]>(() => [
  { value: "video", label: t("pipeline.export.kindVideo") },
  { value: "audio", label: t("pipeline.export.kindAudio") },
  { value: "subtitle", label: t("pipeline.export.kindSubtitle") },
]);

function kindLabel(kind: ExportKind): string {
  if (kind === "subtitle") return t("pipeline.export.kindSubtitle");
  if (kind === "audio") return t("pipeline.export.kindAudio");
  return t("pipeline.export.kindVideo");
}

function formatFor(kind: ExportKind): ExportFormat {
  if (kind === "audio") return "wav";
  if (kind === "subtitle") return "srt";
  return "mp4_h264";
}

function formatLabelFor(kind: ExportKind): string {
  if (kind === "audio") return t("pipeline.export.formatAudio");
  if (kind === "subtitle") return t("pipeline.export.formatSubtitle");
  return t("pipeline.export.formatVideo");
}

const formatLabel = computed(() => selectedKinds.value.map(formatLabelFor).join(" · "));

function extensionFor(kind: ExportKind): string {
  if (kind === "audio") return "wav";
  if (kind === "subtitle") return "srt";
  return "mp4";
}

/** 逐类型当前交付绑定；与 Core currentDeliveryDigests 同口径。 */
function deliveryViewFor(kind: ExportKind) {
  return deliveryReviewView(props.reviews, {
    runId: props.runId,
    language: props.language,
    exportKind: kind,
    subtitleRevisionId: props.currentRevisionId,
  });
}

/**
 * 本地阻断原因（只防误操作，Core 仍是权威）。顺序与 Pipeline 一致：
 * 写入中 → 非法草稿 → 未保存草稿 → 审核投影未知/缺失/未放行。
 */
function blockedReasonFor(kind: ExportKind): string {
  if (props.busy) return t("pipeline.review.blockedSaving");
  if (props.hasErrors) return t("pipeline.review.blockedInvalid");
  if (props.dirty) return t("pipeline.review.blockedDirty");
  if (props.reviewsLoading) return t("mcs.export.reviewLoading");
  if (props.reviewReadFailed) return t("pipeline.review.readFailed");
  if (!props.runId) return t("pipeline.review.gateMissing");
  const view = deliveryViewFor(kind);
  if (view.projectionUnknown) return t("pipeline.review.projectionUnknown");
  if (!view.selectionMatched) return t("pipeline.review.deliveryUnmatched");
  if (!view.hasGates) return t("pipeline.review.gateMissing");
  if (view.relevantRows.length === 0) return t("pipeline.review.deliveryUnmatched");
  if (view.relevantAggregate !== "approved") return t("pipeline.review.somePending");
  if (kind === "subtitle" && !props.currentRevisionId) return t("mcs.noTranslation");
  return "";
}

const blockedReason = computed(() => {
  if (!selectedKinds.value.length) return t("mcs.export.selectOne");
  for (const kind of selectedKinds.value) {
    const reason = blockedReasonFor(kind);
    if (reason) return reason;
  }
  return "";
});

const namingError = computed(() => {
  const value = naming.value.trim();
  if (!value) return t("pipeline.export.emptyNaming");
  const tokens = value.match(/\{[^}]*\}/g) ?? [];
  if (tokens.some((token) => !TEMPLATE_TOKENS.includes(token as (typeof TEMPLATE_TOKENS)[number]))) {
    return t("mcs.export.namingInvalid");
  }
  // 未闭合/多余的括号同样不能用 Core 未定义的规则提交。
  const rest = value.replace(/\{(?:project|language|timestamp)\}/g, "");
  if (/[{}]/.test(rest)) return t("mcs.export.namingInvalid");
  return "";
});

function formatStamp(date: Date): string {
  const pad = (value: number): string => String(value).padStart(2, "0");
  return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}-${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`;
}

const namingPreview = computed(() => {
  const extension = selectedKinds.value[0] ? extensionFor(selectedKinds.value[0]) : "srt";
  const base = naming.value
    .replaceAll("{project}", props.projectName || "YaomuStudio")
    .replaceAll("{language}", props.language || "lang")
    .replaceAll("{timestamp}", formatStamp(new Date()))
    .replace(/[\\/:*?"<>|]/g, "_");
  return `${base}.${extension}`;
});

const canStart = computed(
  () =>
    !exporting.value &&
    selectedKinds.value.length > 0 &&
    !blockedReason.value &&
    !namingError.value &&
    Boolean(outputDir.value.trim()),
);

function toggleKind(kind: ExportKind, on: boolean): void {
  selectedKinds.value = on
    ? [...new Set([...selectedKinds.value, kind])]
    : selectedKinds.value.filter((item) => item !== kind);
  if (!selectedKinds.value.includes("subtitle")) bilingual.value = false;
}

function insertToken(token: string): void {
  naming.value = `${naming.value}${token}`;
}

function jobLabel(job: ExportJob): string {
  if (job.record) return t(taskStatusLabelKey(job.record.status));
  if (job.status === "failed") return t("pipeline.export.failed");
  return t("pipeline.export.started");
}

function jobTone(job: ExportJob): Tone {
  if (job.record) return taskStatusTone(job.record.status);
  return job.status === "failed" ? "error" : "info";
}

function folderReadyFor(job: ExportJob): boolean {
  return Boolean(desktopApi) && job.record?.status === "SUCCEEDED" && !!jobDirectory.value;
}

function isCurrent(generation: number): boolean {
  return !disposed && generation === exportGeneration;
}

/** 逐项轮询 Core 终态；代次/卸载守卫阻止旧回包写回。 */
async function waitForJob(job: ExportJob, generation: number): Promise<void> {
  while (isCurrent(generation) && job.record && !TERMINAL_EXPORT_STATUS.has(job.record.status)) {
    const id = job.record.id;
    await new Promise((resolve) => setTimeout(resolve, 1_000));
    if (!isCurrent(generation) || !job.record) return;
    try {
      const records = await api.listExports(props.projectId);
      if (!isCurrent(generation) || !job.record) return;
      const updated = records.find((item) => item.id === id);
      if (updated) job.record = updated;
    } catch {
      // 暂时断连保留当前记录，继续读回；不把受理当作成功。
    }
  }
  if (!isCurrent(generation) || !job.record) return;
  job.status = job.record.status === "SUCCEEDED" ? "succeeded" : "failed";
  if (job.status === "failed") job.error = messageNotice("pipeline.export.failed");
}

async function openFolder(job: ExportJob): Promise<void> {
  if (!desktopApi || !folderReadyFor(job)) return;
  try {
    await desktopApi.openPath(jobDirectory.value);
  } catch (err) {
    notice.value = errorNotice(err, "pipeline.export.failed");
    noticeTone.value = "error";
  }
}

async function exportNow(): Promise<void> {
  if (!canStart.value) return;
  exporting.value = true;
  notice.value = null;
  noticeTone.value = "info";
  const generation = (exportGeneration += 1);
  try {
    let directory = outputDir.value.trim();
    if (desktopApi) {
      try {
        directory = (await resolveOutputDir()).trim();
      } catch {
        if (isCurrent(generation)) {
          notice.value = messageNotice("pipeline.export.dirUnavailable");
          noticeTone.value = "error";
        }
        return;
      }
    }
    if (!isCurrent(generation)) return;
    if (!directory) {
      notice.value = messageNotice("pipeline.export.dirUnavailable");
      noticeTone.value = "error";
      return;
    }
    jobDirectory.value = directory;
    jobs.value = selectedKinds.value.map((kind) => ({ kind, status: "queued" }));

    for (const job of jobs.value) {
      if (!isCurrent(generation)) return;
      const reason = blockedReasonFor(job.kind);
      if (reason) {
        job.status = "failed";
        job.errorText = reason;
        continue;
      }
      job.status = "running";
      try {
        const queued = await api.export({
          projectId: props.projectId,
          runId: props.runId,
          language: props.language || undefined,
          kind: job.kind,
          format: formatFor(job.kind),
          inputRevisionId: job.kind === "subtitle" ? props.currentRevisionId : undefined,
          bilingual: job.kind === "subtitle" && bilingual.value,
          outputDir: directory,
          namingTemplate: naming.value.trim(),
        });
        if (!isCurrent(generation)) return;
        job.record = queued;
        await waitForJob(job, generation);
      } catch (err) {
        if (!isCurrent(generation)) return;
        job.status = "failed";
        job.error = errorNotice(err, "pipeline.export.failed");
      }
    }

    if (!isCurrent(generation)) return;
    const failed = jobs.value.filter((job) => job.status === "failed").length;
    if (failed === jobs.value.length) {
      notice.value = messageNotice("pipeline.export.failed");
      noticeTone.value = "error";
    } else if (failed > 0) {
      notice.value = messageNotice("mcs.export.partialFailed", { count: failed });
      noticeTone.value = "error";
    } else {
      notice.value = messageNotice("pipeline.export.started");
      noticeTone.value = "info";
    }
  } finally {
    if (isCurrent(generation)) exporting.value = false;
  }
}

onMounted(() => {
  selectedKinds.value = [props.currentRevisionId ? "subtitle" : "video"];
  void resolveOutputDir().catch(() => {
    if (disposed) return;
    notice.value = messageNotice("pipeline.export.dirUnavailable");
    noticeTone.value = "error";
  });
});

onBeforeUnmount(() => {
  disposed = true;
  exportGeneration += 1;
});
</script>

<template>
  <div class="export-panel" data-testid="mcs-export-panel">
    <YmField :label="t('pipeline.export.kind')">
      <div class="export-panel__kinds">
        <YmCheckbox
          v-for="option in kindOptions"
          :key="option.value"
          :data-testid="`mcs-export-kind-${option.value}`"
          :model-value="selectedKinds.includes(option.value as ExportKind)"
          :disabled="
            Boolean(blockedReasonFor(option.value as ExportKind)) &&
            !selectedKinds.includes(option.value as ExportKind)
          "
          :label="option.label"
          @update:model-value="(on: boolean) => toggleKind(option.value as ExportKind, on)"
        />
      </div>
    </YmField>

    <div v-if="formatLabel" class="export-panel__format">
      <span class="export-panel__format-label">{{ t("pipeline.export.format") }}</span>
      <span class="export-panel__format-value">{{ formatLabel }}</span>
    </div>

    <YmField v-if="languageOptions.length > 1" :label="t('pipeline.export.language')">
      <YmSelect
        :model-value="language"
        :options="languageOptions"
        @update:model-value="emit('update:language', $event)"
      />
    </YmField>

    <YmField
      v-if="selectedKinds.includes('subtitle')"
      :label="t('pipeline.export.bilingual')"
    >
      <YmCheckbox
        :model-value="bilingual"
        :disabled="!bilingualAvailable"
        :label="t('pipeline.export.bilingualLabel')"
        @update:model-value="bilingual = $event"
      />
      <p v-if="!bilingualAvailable" class="export-panel__hint">
        {{ t("pipeline.export.bilingualUnavailable") }}
      </p>
    </YmField>

    <YmField
      :label="t('pipeline.export.outputDir')"
      :hint="t('pipeline.export.outputDirHint')"
    >
      <YmTextInput
        data-testid="mcs-export-dir"
        :model-value="outputDir"
        disabled
      />
    </YmField>

    <YmField
      :label="t('pipeline.export.naming')"
      :hint="t('pipeline.export.namingHint')"
      :error="namingError"
    >
      <YmTextInput
        data-testid="mcs-export-naming"
        :model-value="naming"
        @update:model-value="naming = $event"
      />
    </YmField>
    <div class="export-panel__tokens">
      <button
        v-for="token in TEMPLATE_TOKENS"
        :key="token"
        type="button"
        class="export-panel__token"
        @click="insertToken(token)"
      >
        {{ token }}
      </button>
    </div>

    <div class="export-panel__preview">
      <span class="export-panel__preview-label">{{ t("pipeline.export.preview") }}</span>
      <code class="mono" data-testid="mcs-export-preview">{{ namingPreview }}</code>
    </div>

    <p
      v-if="blockedReason"
      class="export-panel__blocked"
      data-testid="mcs-export-blocked"
    >
      {{ blockedReason }}
    </p>

    <p v-if="noticeText" class="export-panel__notice" :class="`is-${noticeTone}`" role="status">
      <bdi>{{ noticeText }}</bdi>
    </p>

    <YmButton
      variant="primary"
      size="md"
      icon="download"
      data-testid="mcs-export-start"
      :loading="exporting"
      :disabled="!canStart"
      @click="exportNow"
    >
      {{ t("pipeline.export.start") }}
    </YmButton>

    <div v-if="jobs.length" class="export-panel__records" data-testid="mcs-export-records">
      <div
        v-for="job in jobs"
        :key="job.kind"
        class="export-panel__record"
        :data-testid="`mcs-export-record-${job.kind}`"
      >
        <div class="export-panel__record-head">
          <span class="export-panel__record-title">{{ kindLabel(job.kind) }}</span>
          <YmBadge :tone="jobTone(job)">{{ jobLabel(job) }}</YmBadge>
        </div>
        <p class="export-panel__record-line">
          <span>{{ t("pipeline.export.outputPath") }}</span>
          <code class="mono">{{ job.record?.outputPath ?? "—" }}</code>
        </p>
        <p v-if="job.error" class="export-panel__record-error" role="alert">
          <bdi>{{ renderNotice(job.error) }}</bdi>
        </p>
        <p v-else-if="job.errorText" class="export-panel__record-error" role="alert">
          <bdi>{{ job.errorText }}</bdi>
        </p>
        <div class="export-panel__record-actions">
          <span class="export-panel__record-id mono">{{ job.record?.id ?? "—" }}</span>
          <YmButton
            variant="secondary"
            size="sm"
            icon="folder-open"
            :disabled="!folderReadyFor(job)"
            :title="folderReadyFor(job) ? undefined : t('pipeline.export.openFolderPending')"
            @click="openFolder(job)"
          >
            {{ t("pipeline.export.openFolder") }}
          </YmButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.export-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  max-height: min(68vh, 560px);
  padding-inline-end: 2px;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.export-panel__format {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.export-panel__format-label {
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
}
.export-panel__format-value {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.export-panel__hint {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.export-panel__tokens {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: calc(-1 * var(--space-2));
}
.export-panel__token {
  padding: 2px 8px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-pill);
  background: var(--color-surface);
  color: var(--color-primary-ink);
  font-size: var(--font-size-2xs);
}
.export-panel__preview {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-2);
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.export-panel__preview code {
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.export-panel__blocked {
  margin: 0;
  color: var(--color-warning-strong);
  font-size: var(--font-size-sm);
}
.export-panel__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
}
.export-panel__notice.is-info {
  background: var(--color-primary-soft);
  color: var(--color-primary-ink);
}
.export-panel__notice.is-error {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.export-panel__record {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.export-panel__record-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.export-panel__record-title {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.export-panel__record-line {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.export-panel__record-line code {
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.export-panel__record-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.export-panel__record-id {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}

.export-panel__kinds {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
}
.export-panel__records {
  display: flex;
  flex-direction: column;
}
.export-panel__record-error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
}
</style>

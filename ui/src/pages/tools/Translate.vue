<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "@/api";
import { formatBytes, formatTimestamp, getLocale, t } from "@/i18n";
import { errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import { TARGET_LANGUAGES, targetLanguageLabel } from "@/domain/languages";
import { detectFileLanguage } from "@/domain/subtitleLanguage";
import type {
  Capability,
  CapabilitySlotKey,
  ExportRecord,
  Project,
  SubtitleRevision,
  SubtitleTrack,
} from "@/domain/types";
import {
  nodeStatusLabelKey,
  nodeStatusTone,
  runStatusLabelKey,
  runStatusTone,
  slotStateLabelKey,
  slotStateTone,
} from "@/domain/status";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useCreateGuard } from "@/composables/useCreateGuard";
import { useCreationFlow } from "@/composables/useCreationFlow";
import {
  loadRecentTargetLanguages,
  rememberTargetLanguages,
} from "@/composables/useRecentTargetLanguages";
import { evaluateAccess } from "@/domain/capabilityAccess";
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CapabilityAccessNotice from "@/components/layout/CapabilityAccessNotice.vue";
import LanguageMultiSelect from "@/components/LanguageMultiSelect.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import ExecutionLogPanel from "@/components/tools/ExecutionLogPanel.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import { useDefaultOutputDir } from "@/composables/useDefaultOutputDir";
import { useToolRun } from "./useToolRun";
import { useRunTask } from "./useRunTask";

interface PickedFile {
  name: string;
  sizeBytes: number;
}

interface TrackResult {
  code: string;
  label: string;
  track: SubtitleTrack;
  revision: SubtitleRevision;
}

interface ExportState {
  busy: boolean;
  record?: ExportRecord;
  error?: string;
}

const TERMINAL_EXPORT = new Set(["SUCCEEDED", "FAILED", "CANCELED"]);

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const caps = useCapabilitiesStore();

/** 所需引擎未就绪时禁用创建，并在页脚说明缺少的能力。 */
const { blocked: createBlocked, blockedText: createBlockedText } = useCreateGuard("tool-translate");

/** 连续创建：清空表单、结果与当前 Run 视图，保留本工具的配置项。 */
function resetForm(): void {
  projectName.value = "";
  sourceFile.value = null;
  sourceSubtitle.value = null;
  showErrors.value = false;
  mediaPathError.value = "";
  resultError.value = "";
  resultProject.value = null;
  exporting.value = {};
  resetRun();
}

const { hasProject, canOpenProject, primaryLabel, onPrimary, openProject, startNew } = useCreationFlow({
  projectId: () => currentRun.value?.projectId,
  busy: () => runBusy.value,
  create,
  reset: resetForm,
});

const projectName = ref("");
const sourceFile = ref<File | null>(null);
const sourceSubtitle = ref<PickedFile | null>(null);
const sourceLanguage = ref("und");
const targetLanguages = ref<string[]>(loadRecentTargetLanguages("translate"));
const showErrors = ref(false);
const mediaPathError = ref("");
const resultError = ref("");
const resultProject = ref<Project | null>(null);
const exporting = ref<Record<string, ExportState>>({});
const { outputDir: exportDir, resolve: resolveOutputDir } = useDefaultOutputDir();
const namingTemplate = ref("{project}_{language}_{timestamp}");

const relatedSlotKeys: CapabilitySlotKey[] = ["translate"];
const relatedCapabilities = computed<Capability[]>(() =>
  relatedSlotKeys
    .map((slot) => caps.slotState(slot))
    .filter((cap): cap is Capability => cap !== undefined),
);

const {
  busy: runBusy,
  phase: runPhase,
  run: currentRun,
  issues: runIssues,
  error: runError,
  partial: runPartial,
  start: startRun,
  restore: restoreRun,
  reset: resetRun,
} = useToolRun();

const targetOptions = computed(() => TARGET_LANGUAGES);

const footerStatus = computed(() => {
  if (translateTask.value?.status === "WAITING_RESOURCE") return t("state.waitingResource");
  if (runBusy.value) return t("creation.footer.running");
  if (createBlocked.value) return createBlockedText.value;
  const missing: string[] = [];
  if (!projectName.value.trim()) missing.push(t("tools.common.projectName"));
  if (!sourceSubtitle.value) missing.push(t("tools.translate.source.label"));
  if (targetLanguages.value.length === 0) missing.push(t("tools.translate.languages.label"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});
const projectError = computed(() =>
  showErrors.value && !projectName.value.trim() ? t("tools.common.projectNameErr") : "",
);
const sourceError = computed(() =>
  showErrors.value && !sourceSubtitle.value ? t("tools.translate.source.err") : "",
);
const languagesError = computed(() =>
  showErrors.value && targetLanguages.value.length === 0
    ? t("tools.translate.languages.err")
    : "",
);
/** 只展示实际执行的节点；任务在等执行器时节点也显示等待资源，不能谎称运行中。 */
const runNodeRows = computed(() =>
  (currentRun.value?.nodes ?? [])
    .filter((node) => node.status !== "skipped")
    .map((node) => {
      const waiting = node.status === "running" && translateTask.value?.status === "WAITING_RESOURCE";
      return {
        key: node.key,
        label: t(`pipeline.node.${node.key}`),
        status: node.status,
        waiting,
        progressText: waiting
          ? "0%"
          : node.status === "running" && node.progress === 0
            ? t("tools.common.nodeWorking")
            : `${node.progress}%`,
      };
    }),
);
const runPhaseLabel = computed(() => t(`tools.flow.${runPhase.value}`));
const translateTask = useRunTask(
  () => currentRun.value?.id,
  "06_translate",
  () => runBusy.value,
);

// 翻译等云端步骤没有逐句进度，06 节点会长时间停在 0%。展示已运行时长，
// 让用户能区分“仍在处理”和“真卡住”，不伪造进度百分比。
const runActive = computed(() => {
  const status = currentRun.value?.status;
  return status === "QUEUED" || status === "RUNNING" || status === "WAITING_REVIEW";
});
const nowTick = ref(Date.now());
let elapsedTimer: ReturnType<typeof setInterval> | null = null;

function stopElapsedTimer(): void {
  if (elapsedTimer !== null) {
    clearInterval(elapsedTimer);
    elapsedTimer = null;
  }
}

watch(
  runActive,
  (active) => {
    stopElapsedTimer();
    if (!active) return;
    nowTick.value = Date.now();
    elapsedTimer = setInterval(() => {
      nowTick.value = Date.now();
    }, 1000);
  },
  { immediate: true },
);

onUnmounted(stopElapsedTimer);

const runElapsedLabel = computed(() => {
  if (!runActive.value || !currentRun.value) return "";
  const started = Date.parse(currentRun.value.createdAt);
  if (!Number.isFinite(started)) return "";
  const total = Math.max(0, Math.floor((nowTick.value - started) / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const clock =
    hours > 0
      ? `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`
      : `${minutes}:${String(seconds).padStart(2, "0")}`;
  return t("tools.common.runElapsed", { duration: clock });
});
// 结果按 Run 冻结的目标语言展示，避免表单后续改动让结果区漂移。
const resultLanguages = computed<string[]>(() => {
  const frozen = currentRun.value?.languages ?? [];
  return frozen.length > 0 ? frozen : targetLanguages.value;
});
const resultTracks = computed<TrackResult[]>(() => {
  const project = resultProject.value;
  if (!project) return [];
  return resultLanguages.value
    .map((code) => {
      const track = project.tracks.find(
        (item) => item.type === "translation" && item.language === code,
      );
      if (!track) return null;
      const revision =
        track.revisions.find((item) => item.id === track.currentRevisionId) ??
        track.revisions.at(-1);
      // 无 revision 或空句段都按缺失处理，不能显示成成功译文。
      if (!revision || revision.segments.length === 0) return null;
      return { code, label: languageLabel(code), track, revision };
    })
    .filter((item): item is TrackResult => item !== null);
});

function languageLabel(code: string): string {
  const language = TARGET_LANGUAGES.find((item) => item.code === code);
  return language ? targetLanguageLabel(language, getLocale()) : code;
}

function resolveFilePath(file: File): string {
  if (desktopApi) {
    return desktopApi.getPathForFile(file).trim();
  }
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

async function onSourceSubtitle(file: File | undefined): Promise<void> {
  if (!file) return;
  sourceFile.value = file;
  sourceSubtitle.value = { name: file.name, sizeBytes: file.size };
  sourceLanguage.value = (await detectFileLanguage(file)) ?? "und";
  mediaPathError.value = "";
}

function removeSource(): void {
  sourceFile.value = null;
  sourceSubtitle.value = null;
  mediaPathError.value = "";
}

function toggleLanguage(code: string, checked: boolean): void {
  if (checked) {
    if (!targetLanguages.value.includes(code)) {
      targetLanguages.value = [...targetLanguages.value, code];
    }
    return;
  }
  targetLanguages.value = targetLanguages.value.filter((item) => item !== code);
}

function onToggleLanguage(code: string): void {
  toggleLanguage(code, !targetLanguages.value.includes(code));
}

function selectAllLanguages(): void {
  targetLanguages.value = targetOptions.value.map((language) => language.code);
}

function clearLanguages(): void {
  targetLanguages.value = [];
}

function exportState(code: string): ExportState {
  return exporting.value[code] ?? { busy: false };
}

function setExportState(code: string, patch: Partial<ExportState>): void {
  exporting.value = {
    ...exporting.value,
    [code]: { ...exportState(code), ...patch },
  };
}

async function create(): Promise<void> {
  showErrors.value = true;
  resultError.value = "";
  resultProject.value = null;
  exporting.value = {};
  if (
    !projectName.value.trim() ||
    !sourceFile.value ||
    targetLanguages.value.length === 0
  ) {
    return;
  }
  const filePath = resolveFilePath(sourceFile.value);
  if (!filePath) {
    mediaPathError.value = t("tools.translate.pathUnavailable");
    return;
  }
  showErrors.value = false;
  const result = await startRun({
    projectName: projectName.value.trim(),
    filePath,
    payload: {
      template: "tool_translate",
      sourceLanguage: sourceLanguage.value,
      targetLanguages: [...targetLanguages.value],
    },
    afterImport: async ({ projectId, assetIds }) => {
      await api.importSubtitleTrack({
        projectId,
        assetId: assetIds[0],
        language: sourceLanguage.value,
      });
    },
  });
  if (!result) return;
  rememberTargetLanguages("translate", targetLanguages.value);
  resultProject.value = result.project;
  // 无轨、无 revision 或空句段都视为失败，不展示成功结果。
  // 一个语种都没产出时不能说“翻译完成”，需指向凭据/执行日志。
  const missing = resultLanguages.value.some(
    (code) => !resultTracks.value.some((item) => item.code === code),
  );
  if (missing) {
    resultError.value = t(
      resultTracks.value.length === 0
        ? "tools.translate.result.none"
        : "tools.translate.result.missing",
    );
  }
}

async function pollExport(projectId: string, exportId: string): Promise<ExportRecord | null> {
  const deadline = Date.now() + 15 * 60 * 1000;
  while (Date.now() < deadline) {
    const records = await api.listExports(projectId);
    const current = records.find((record) => record.id === exportId);
    if (current && TERMINAL_EXPORT.has(current.status)) return current;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  return null;
}

async function exportSrt(item: TrackResult): Promise<void> {
  const project = resultProject.value;
  if (!project) return;
  try {
    await resolveOutputDir();
  } catch {
    setExportState(item.code, { error: t("tools.common.export.dirUnavailable") });
    return;
  }
  if (!exportDir.value.trim() || !namingTemplate.value.trim()) {
    setExportState(item.code, { error: t("tools.translate.export.empty") });
    return;
  }
  setExportState(item.code, { busy: true, error: undefined, record: undefined });
  try {
    const created = await api.export({
      projectId: project.id,
      kind: "subtitle",
      format: "srt",
      language: item.code,
      inputRevisionId: item.revision.id,
      outputDir: exportDir.value.trim(),
      namingTemplate: namingTemplate.value.trim(),
    });
    setExportState(item.code, { record: created });
    if (TERMINAL_EXPORT.has(created.status)) {
      if (created.status !== "SUCCEEDED") {
        setExportState(item.code, { error: t("tools.translate.export.failed") });
      }
      return;
    }
    const final = await pollExport(project.id, created.id);
    if (!final) {
      setExportState(item.code, { error: t("tools.translate.export.timeout") });
      return;
    }
    setExportState(item.code, { record: final });
    if (final.status !== "SUCCEEDED") {
      setExportState(item.code, { error: t("tools.translate.export.failed") });
    }
  } catch (err) {
    setExportState(item.code, { error: errorText(err) });
  } finally {
    setExportState(item.code, { busy: false });
  }
}

onMounted(() => {
  if (caps.slots.length === 0) void caps.load().catch(() => undefined);
  void resolveOutputDir().catch(() => undefined);
  void restoreLastRun();
});

/** 返回翻译页时恢复上次已结束的 Run，让「执行过程」可回看。 */
async function restoreLastRun(): Promise<void> {
  const restored = await restoreRun("tool_translate");
  if (!restored) return;
  try {
    resultProject.value = await api.getProject(restored.projectId);
  } catch {
    // Run 与执行过程仍可用；结果读不到时不伪造。
  }
}
</script>

<template>
  <WorkbenchPage
    scaffold
    icon="globe"
    :subtitle="t('tools.translate.intro')"
    :title="t('tools.translate.title')"
    :primary-label="primaryLabel"
    :primary-icon="canOpenProject ? 'folder-open' : 'plus'"
    primary-shortcut
    :primary-loading="runBusy"
    :primary-disabled="runBusy || (!hasProject && createBlocked)"
    @primary="onPrimary"
  >
    <template #footer-actions>
      <YmButton
        v-if="hasProject && runBusy"
        variant="secondary"
        size="lg"
        icon="folder-open"
        @click="openProject"
      >
        {{ t("common.seeDetails") }}
      </YmButton>
      <YmButton
        v-else-if="hasProject"
        variant="secondary"
        size="lg"
        icon="refresh"
        @click="startNew"
      >
        {{ t("creation.result.again") }}
      </YmButton>
    </template>
    <template #config>
      <div class="page-form">
        <CapabilityAccessNotice access-key="tool-translate" />

        <YmPanel :title="t('tools.translate.step.project')">
          <div class="panel-stack">
            <YmField
              :label="t('tools.common.projectName')"
              :hint="t('tools.common.projectNameHint')"
              :error="projectError"
              required
            >
              <YmTextInput
                v-model="projectName"
                :placeholder="t('tools.common.projectName')"
              />
            </YmField>

            <YmField
              :label="t('tools.translate.source.label')"
              :error="sourceError"
              required
            >
              <YmFileRow
                v-if="sourceSubtitle"
                :name="sourceSubtitle.name"
                :size="formatBytes(sourceSubtitle.sizeBytes)"
                icon="subtitles"
              >
                <template #actions>
                  <YmIconButton
                    name="x"
                    :label="t('tools.common.remove')"
                    :size="16"
                    @click="removeSource"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('tools.translate.source.upload')"
                :hint="t('tools.translate.source.hint')"
                accept=".srt"
                @files="onSourceSubtitle($event[0])"
              />
              <p v-if="mediaPathError" class="flow-error">{{ mediaPathError }}</p>
            </YmField>
          </div>
        </YmPanel>

        <CreationNotice
          :items="[t('tools.translate.outputHint')]"
        />

        <YmPanel :title="t('tools.translate.step.languages')">
          <div class="panel-stack">
            <YmField
              :label="t('tools.translate.languages.label')"
              :hint="t('tools.translate.languages.hint')"
              :error="languagesError"
              required
            >
              <LanguageMultiSelect
                :model-value="targetLanguages"
                :options="targetOptions"
                @toggle="onToggleLanguage"
                @select-all="selectAllLanguages"
                @clear="clearLanguages"
              />
            </YmField>
          </div>
        </YmPanel>
      </div>
    </template>

    <template #results>
      <div class="results-stack">
        <section class="result-head">
          <div class="result-head__top">
            <h2 class="result-head__title">{{ t("tools.common.result.title") }}</h2>
            <div class="result-head__status">
              <YmBadge v-if="currentRun" :tone="runStatusTone(currentRun.status)">
                {{ t(runStatusLabelKey(currentRun.status)) }}
              </YmBadge>
              <YmBadge v-else-if="runBusy" tone="info">{{ runPhaseLabel }}</YmBadge>
              <YmBadge v-else tone="neutral">{{ t("tools.translate.result.idle") }}</YmBadge>
              <span v-if="runElapsedLabel" class="run-elapsed">{{ runElapsedLabel }}</span>
            </div>
          </div>
          <p class="result-head__desc">{{ t("tools.translate.result.desc") }}</p>
          <p v-if="runError" class="flow-error">{{ errorText(runError) }}</p>
          <p v-if="runPartial && resultTracks.length" class="flow-warning">{{ t("tools.flow.partial") }}</p>
          <p v-if="resultError" class="flow-error">{{ resultError }}</p>
          <ul v-if="runIssues.length" class="flow-issues">
            <li v-for="issue in runIssues" :key="preflightIssueKey(issue)">
              {{ errorText(issue) }}
            </li>
          </ul>
          <div class="cap-block">
            <h3 class="cap-block__title">{{ t("tools.common.capability") }}</h3>
            <div class="cap-list">
              <YmBadge
                v-for="cap in relatedCapabilities"
                :key="cap.slot"
                :tone="slotStateTone(cap.state)"
              >
                {{ t(cap.nameKey) }} · {{ t(slotStateLabelKey(cap.state)) }}
              </YmBadge>
              <span v-if="relatedCapabilities.length === 0" class="cap-hint">
                {{ t("tools.common.capabilityPending") }}
              </span>
            </div>
            <p class="cap-hint">{{ t("tools.common.capabilityHint") }}</p>
          </div>
        </section>

        <YmCard v-if="currentRun || runBusy">
          <template #header>
            <div class="summary-head">
              <span>{{ currentRun?.name ?? t("tools.translate.result.running") }}</span>
              <YmBadge v-if="currentRun" :tone="runStatusTone(currentRun.status)">
                {{ t(runStatusLabelKey(currentRun.status)) }}
              </YmBadge>
              <YmBadge v-else tone="info">{{ runPhaseLabel }}</YmBadge>
            </div>
          </template>
          <ul v-if="runNodeRows.length" class="node-list">
            <li v-for="row in runNodeRows" :key="row.key" class="node-row">
              <span class="node-row__name">{{ row.label }}</span>
              <YmBadge :tone="row.waiting ? 'neutral' : nodeStatusTone(row.status)">
                {{ row.waiting ? t("state.waitingResource") : t(nodeStatusLabelKey(row.status)) }}
              </YmBadge>
              <span class="node-row__progress">{{ row.progressText }}</span>
            </li>
          </ul>
          <p v-else class="run-status">{{ runPhaseLabel }}</p>
        </YmCard>

        <ExecutionLogPanel
          :task-id="translateTask?.id"
          :active="runBusy"
          :task-status="translateTask?.status"
          :waiting-message-key="translateTask?.error?.messageKey"
        />

        <YmCard v-if="resultTracks.length">
          <template #header>
            <div class="summary-head">
              <span>{{ t("tools.translate.export.title") }}</span>
            </div>
          </template>
          <div class="export-settings">
            <YmField
              :label="t('tools.translate.export.dir')"
              :hint="t('tools.common.export.defaultDirHint')"
            >
              <YmTextInput v-model="exportDir" disabled />
            </YmField>
            <YmField :label="t('tools.translate.export.naming')">
              <YmTextInput v-model="namingTemplate" />
            </YmField>
          </div>
        </YmCard>

        <YmCard v-for="item in resultTracks" :key="item.code">
          <template #header>
            <div class="summary-head">
              <span>{{ item.label }}</span>
              <YmBadge tone="success">
                {{ t("tools.translate.result.count", { count: item.revision.segments.length }) }}
              </YmBadge>
            </div>
          </template>
          <ul class="segment-list">
            <li v-for="segment in item.revision.segments" :key="segment.id" class="segment-row">
              <span class="segment-time">{{ formatTimestamp(segment.startMs) }}</span>
              <span class="segment-text">{{ segment.text }}</span>
            </li>
          </ul>
          <div class="export-block">
            <YmButton
              size="sm"
              :disabled="exportState(item.code).busy"
              @click="exportSrt(item)"
            >
              {{ t("tools.translate.export.action") }}
            </YmButton>
            <p v-if="exportState(item.code).record?.outputPath" class="export-done">
              {{ t("tools.translate.export.done", { path: exportState(item.code).record?.outputPath ?? "" }) }}
            </p>
            <p v-if="exportState(item.code).error" class="flow-error">
              {{ exportState(item.code).error }}
            </p>
          </div>
        </YmCard>

        <div v-if="!currentRun && !runBusy" class="results-empty">
          <YmEmpty
            :title="t('tools.common.result.emptyTitle')"
            :description="t('tools.translate.result.emptyDesc')"
            icon="subtitles"
          />
        </div>
      </div>
    </template>
    <template #footer>{{ footerStatus }}</template>
  </WorkbenchPage>
</template>

<style scoped>
.page-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.page-intro {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
}
.panel-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.notice {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
.notice--info {
  background: var(--color-info-soft);
  color: var(--color-info-strong);
}
.notice__icon {
  flex: none;
  margin-top: 1px;
}
.results-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.result-head {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.result-head__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.result-head__status {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.run-elapsed {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.result-head__title {
  font-size: var(--font-size-lg);
}
.result-head__desc {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.cap-block {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.cap-block__title {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.cap-list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.cap-hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.results-empty {
  background: var(--color-surface);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-lg);
}
.summary-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.flow-error {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
.flow-warning {
  font-size: var(--font-size-xs);
  color: var(--color-warning-strong);
}
.flow-issues {
  margin: 0;
  padding-left: var(--space-4);
  color: var(--color-warning-strong);
  font-size: var(--font-size-xs);
}
.node-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.node-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
  font-size: var(--font-size-sm);
}
.node-row__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-row__progress {
  flex: none;
  color: var(--color-text-faint);
  font-variant-numeric: tabular-nums;
}
.run-status {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.export-settings {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.segment-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  max-height: 280px;
  overflow: auto;
  margin: 0;
  padding: 0;
  list-style: none;
}
.segment-row {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--font-size-sm);
}
.segment-time {
  flex: none;
  color: var(--color-text-faint);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: var(--font-size-xs);
  white-space: nowrap;
}
.segment-text {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
.export-block {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.export-done {
  margin: 0;
  color: var(--color-success-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
</style>

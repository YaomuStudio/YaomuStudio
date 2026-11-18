<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { formatBytes, formatTimestamp, getLocale, t } from "@/i18n";
import { api } from "@/api";
import { errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import { slotAccessOf } from "@/domain/capabilityAccess";
import { ASR_LANGUAGES, OCR_LANGUAGES, targetLanguageLabel } from "@/domain/languages";
import type {
  Capability,
  CapabilitySlotKey,
  ExportRecord,
  Project,
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
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CapabilityAccessNotice from "@/components/layout/CapabilityAccessNotice.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import ExecutionLogPanel from "@/components/tools/ExecutionLogPanel.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import OcrQualityPicker from "@/components/ui/OcrQualityPicker.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmRadioGroup from "@/components/ui/YmRadioGroup.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import { useDefaultOutputDir } from "@/composables/useDefaultOutputDir";
import { useToolRun } from "./useToolRun";
import { useRunTask } from "./useRunTask";
import { useMediaPreview } from "@/composables/useMediaPreview";

interface PickedFile {
  name: string;
  sizeBytes: number;
}

type ExtractMode = "asr" | "ocr" | "asr_ocr";

interface MethodOption {
  value: ExtractMode;
  label: string;
  description: string;
  disabled: boolean;
}

interface LanguageOption {
  code: string;
  label: string;
  submitCode: string;
  dialect: boolean;
  pending: boolean;
}

const AUDIO_EXTENSIONS = [".mp3", ".wav", ".aac", ".flac", ".m4a"];
// docs/business-parity.md §6：繁体 zh-tw 与菲律宾 tl 的最终 code 待后端确认（未决）。
const PENDING_CODES = new Set(["zh-tw", "tl"]);
// ASR+OCR 使用 OCR ∩ ASR 交集；提交 ISO 639-1 码。
const ASR_OCR_CODES = ["zh", "zh-tw", "en", "ja"];
const OCR_FOR_ISO: Record<string, "chinese" | "chinese_traditional" | "english" | "japanese"> = {
  zh: "chinese",
  "zh-tw": "chinese_traditional",
  en: "english",
  ja: "japanese",
};

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const caps = useCapabilitiesStore();

/** 所需引擎未就绪时禁用创建，并在页脚说明缺少的能力。 */
const { blocked: createBlocked, blockedText: createBlockedText } = useCreateGuard("tool-extract");

/** 连续创建：清空表单、结果与当前 Run 视图，保留本工具的配置项。 */
function resetForm(): void {
  projectName.value = "";
  mediaFile.value = null;
  media.value = null;
  showErrors.value = false;
  mediaPathError.value = "";
  resultError.value = "";
  resultProject.value = null;
  exporting.value = false;
  exportRecord.value = null;
  resetRun();
}

const { hasProject, canOpenProject, primaryLabel, onPrimary, openProject, startNew } = useCreationFlow({
  projectId: () => currentRun.value?.projectId,
  busy: () => runBusy.value,
  create,
  reset: resetForm,
});

const projectName = ref("");
const mediaFile = ref<File | null>(null);
const media = ref<PickedFile | null>(null);
const { kind: mediaKind, url: mediaPreviewUrl } = useMediaPreview(mediaFile);
const mode = ref<ExtractMode>("asr");
const ocrQuality = ref<"standard" | "fine">("fine");
const selectedLanguage = ref("zh");
const languageQuery = ref("");
const showErrors = ref(false);
const mediaPathError = ref("");
const resultError = ref("");
const resultProject = ref<Project | null>(null);
const { outputDir: exportDir, resolve: resolveOutputDir } = useDefaultOutputDir();
const namingTemplate = ref("{project}_{language}");
const exporting = ref(false);
const exportRecord = ref<ExportRecord | null>(null);
const TERMINAL_EXPORT = new Set(["SUCCEEDED", "FAILED", "CANCELED"]);

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

const extensionOf = (name: string): string => {
  const index = name.lastIndexOf(".");
  return index >= 0 ? name.slice(index).toLowerCase() : "";
};

const isAudio = computed(() => {
  if (!media.value) return false;
  return AUDIO_EXTENSIONS.includes(extensionOf(media.value.name));
});

const mediaIcon = computed(() => (isAudio.value ? "wave" : "film"));

const METHOD_COPY: Record<ExtractMode, { labelKey: string; descriptionKey: string }> = {
  asr: {
    labelKey: "tools.extract.method.asr",
    descriptionKey: "tools.extract.method.asrDesc",
  },
  ocr: {
    labelKey: "tools.extract.method.ocr",
    descriptionKey: "tools.extract.method.ocrDesc",
  },
  asr_ocr: {
    labelKey: "tools.extract.method.both",
    descriptionKey: "tools.extract.method.bothDesc",
  },
};

// 目录未加载时槽位按可用处理（加载后自动重算），避免首帧模式闪烁。
// 提取方式必须能立即执行：已准入但未就绪（如 OCR 未安装）仍展示但禁用。
const slotUsable = (slot: CapabilitySlotKey): boolean => {
  if (caps.slots.length === 0) return true;
  const capability = caps.slotState(slot);
  return capability ? slotAccessOf(capability.state) === "usable" : false;
};
const asrUsable = computed(() => slotUsable("asr"));
const ocrUsable = computed(() => slotUsable("ocr"));
const modeAdmitted = (value: ExtractMode): boolean => {
  if (value === "asr") return asrUsable.value;
  if (value === "ocr") return ocrUsable.value;
  return asrUsable.value && ocrUsable.value;
};
const availableModes = computed<ExtractMode[]>(() => {
  const modes: ExtractMode[] = [];
  if (asrUsable.value) modes.push("asr");
  if (!isAudio.value) {
    if (ocrUsable.value) modes.push("ocr");
    if (asrUsable.value && ocrUsable.value) modes.push("asr_ocr");
  }
  return modes;
});
// 三种方式始终展示；暂未准入的 OCR/联合模式禁用而不是从界面移除。
const applicableModes = computed<ExtractMode[]>(() =>
  isAudio.value ? ["asr"] : ["asr", "ocr", "asr_ocr"],
);
const methodOptions = computed<MethodOption[]>(() =>
  applicableModes.value.map((value) => ({
    value,
    label: t(METHOD_COPY[value].labelKey),
    description: t(METHOD_COPY[value].descriptionKey),
    disabled: !modeAdmitted(value),
  })),
);
const noExtractMode = computed(() => availableModes.value.length === 0);
const footerStatus = computed(() => {
  if (extractTask.value?.status === "WAITING_RESOURCE") return t("state.waitingResource");
  if (runBusy.value) return t("creation.footer.running");
  if (createBlocked.value) return createBlockedText.value;
  const missing: string[] = [];
  if (!projectName.value.trim()) missing.push(t("tools.common.projectName"));
  if (!media.value) missing.push(t("tools.extract.media.label"));
  if (noExtractMode.value) missing.push(t("tools.common.capability"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});

const languageOptions = computed<LanguageOption[]>(() => {
  if (mode.value === "ocr") {
    return OCR_LANGUAGES.map((language) => ({
      code: language.code,
      label: targetLanguageLabel(language, getLocale()),
      submitCode: language.code,
      dialect: false,
      pending: false,
    }));
  }
  const list = mode.value === "asr"
    ? ASR_LANGUAGES
    : ASR_LANGUAGES.filter((language) => ASR_OCR_CODES.includes(language.code));
  return list.map((language) => ({
    code: language.code,
    label: targetLanguageLabel(language, getLocale()),
    // ASR 使用 submitCode（方言映射 zh）；ASR+OCR 使用 ISO 码。
    submitCode: mode.value === "asr" ? language.submitCode : language.code,
    dialect: language.dialect ?? false,
    pending: PENDING_CODES.has(language.code),
  }));
});

const filteredLanguages = computed<LanguageOption[]>(() => {
  const query = languageQuery.value.trim().toLowerCase();
  if (!query) return languageOptions.value;
  return languageOptions.value.filter(
    (language) =>
      language.label.toLowerCase().includes(query) ||
      language.code.toLowerCase().includes(query) ||
      language.submitCode.toLowerCase().includes(query),
  );
});

const selectedOption = computed<LanguageOption | undefined>(() =>
  languageOptions.value.find((language) => language.code === selectedLanguage.value),
);
const selectedCode = computed(() => selectedOption.value?.submitCode ?? "—");
const selectedDialect = computed(() => selectedOption.value?.dialect ?? false);
const selectedPending = computed(() => selectedOption.value?.pending ?? false);

const relatedSlotKeys = computed<CapabilitySlotKey[]>(() => {
  const keys: CapabilitySlotKey[] = [];
  if (mode.value === "asr" || mode.value === "asr_ocr") keys.push("asr");
  if (mode.value === "ocr" || mode.value === "asr_ocr") keys.push("ocr");
  keys.push("ffmpeg");
  return keys;
});
const extractNoticeItems = computed(() => [
  ...(isAudio.value ? [t("tools.extract.media.audioNote")] : []),
  t("tools.extract.outputHint"),
]);
const relatedCapabilities = computed<Capability[]>(() =>
  relatedSlotKeys.value
    .map((slot) => caps.slotState(slot))
    .filter((cap): cap is Capability => cap !== undefined),
);

const projectError = computed(() =>
  showErrors.value && !projectName.value.trim() ? t("tools.common.projectNameErr") : "",
);
const mediaError = computed(() =>
  showErrors.value && !media.value ? t("tools.extract.media.err") : "",
);
const languageError = computed(() =>
  showErrors.value && !selectedOption.value ? t("tools.extract.language.err") : "",
);

/** 只展示实际执行的节点；任务在等执行器时节点也显示等待资源，不能谎称运行中。 */
const runNodeRows = computed(() =>
  (currentRun.value?.nodes ?? [])
    .filter((node) => node.status !== "skipped")
    .map((node) => {
      const waiting = node.status === "running" && extractTask.value?.status === "WAITING_RESOURCE";
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
const extractTask = useRunTask(
  () => currentRun.value?.id,
  "04_extract",
  () => runBusy.value,
);
const resultTrack = computed<SubtitleTrack | undefined>(() =>
  resultProject.value?.tracks.find((track) => track.type === "transcription"),
);
const resultRevision = computed(() => {
  const track = resultTrack.value;
  if (!track) return undefined;
  return (
    track.revisions.find((revision) => revision.id === track.currentRevisionId) ??
    track.revisions.at(-1)
  );
});
const resultSegments = computed(() => resultRevision.value?.segments ?? []);
const runPhaseLabel = computed(() => t(`tools.flow.${runPhase.value}`));

function resolveFilePath(file: File): string {
  if (desktopApi) {
    return desktopApi.getPathForFile(file).trim();
  }
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

function onMedia(file: File | undefined): void {
  if (!file) return;
  mediaFile.value = file;
  media.value = { name: file.name, sizeBytes: file.size };
  mediaPathError.value = "";
}

function removeMedia(): void {
  mediaFile.value = null;
  media.value = null;
  mediaPathError.value = "";
}

function onModeChange(value: string): void {
  if (value === "asr" || value === "ocr" || value === "asr_ocr") {
    mode.value = value;
  }
}

watch(isAudio, (audio) => {
  if (audio && mode.value !== "asr") mode.value = "asr";
});

watch(mode, () => {
  languageQuery.value = "";
  if (!languageOptions.value.some((language) => language.code === selectedLanguage.value)) {
    selectedLanguage.value = languageOptions.value[0]?.code ?? "";
  }
});

// 能力目录加载后，若当前提取方式未准入则切到第一个可用方式。
// 放在 mode watcher 之后注册，确保初始 setup 期间切换也能重置源语言。
watch(
  availableModes,
  (modes) => {
    if (modes.length > 0 && !modes.includes(mode.value)) {
      mode.value = modes[0];
    }
  },
  { immediate: true },
);

function ocrLanguageForCurrentMode(): "chinese" | "chinese_traditional" | "english" | "japanese" | undefined {
  if (mode.value === "asr") return undefined;
  if (mode.value === "ocr") {
    if (selectedLanguage.value === "chinese" || selectedLanguage.value === "chinese_traditional" ||
      selectedLanguage.value === "english" || selectedLanguage.value === "japanese") {
      return selectedLanguage.value;
    }
    return undefined;
  }
  return OCR_FOR_ISO[selectedCode.value];
}

async function create(): Promise<void> {
  showErrors.value = true;
  resultError.value = "";
  resultProject.value = null;
  exportRecord.value = null;
  if (!projectName.value.trim() || !mediaFile.value || !selectedOption.value) return;
  const filePath = resolveFilePath(mediaFile.value);
  if (!filePath) {
    mediaPathError.value = t("tools.extract.pathUnavailable");
    return;
  }
  const ocrLanguage = ocrLanguageForCurrentMode();
  if (mode.value !== "asr" && !ocrLanguage) {
    resultError.value = t("tools.extract.language.err");
    return;
  }
  showErrors.value = false;
  const result = await startRun({
    projectName: projectName.value.trim(),
    filePath,
    payload: {
      template: "tool_extract",
      sourceLanguage: selectedCode.value,
      targetLanguages: [],
      extract: {
        mode: mode.value,
        ocrLanguage,
        ocrQuality: mode.value === "asr" ? undefined : ocrQuality.value,
      },
    },
  });
  if (!result) return;
  resultProject.value = result.project;
  exportRecord.value = null;
  if (!resultTrack.value || !resultRevision.value) {
    const extractFailed = result.run.nodes.some(
      (node) => node.key === "04_extract" && node.status === "failed",
    );
    // 导入成功、提取失败时 Run 是 PARTIAL。没有原文轨是失败结果，不是成功但漏轨。
    resultError.value = extractFailed
      ? t("task.worker_error")
      : t("tools.extract.result.noTrack");
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

async function exportSrt(): Promise<void> {
  const revision = resultRevision.value;
  if (!resultProject.value || !revision) {
    resultError.value = t("tools.extract.result.noTrack");
    return;
  }
  try {
    await resolveOutputDir();
  } catch {
    resultError.value = t("tools.common.export.dirUnavailable");
    return;
  }
  if (!exportDir.value.trim() || !namingTemplate.value.trim()) {
    resultError.value = t("tools.extract.export.empty");
    return;
  }
  exporting.value = true;
  resultError.value = "";
  try {
    const created = await api.export({
      projectId: resultProject.value.id,
      kind: "subtitle",
      format: "srt",
      language: selectedCode.value,
      inputRevisionId: revision.id,
      outputDir: exportDir.value.trim(),
      namingTemplate: namingTemplate.value.trim(),
    });
    exportRecord.value = created;
    if (TERMINAL_EXPORT.has(created.status)) {
      if (created.status === "FAILED" || created.status === "CANCELED") {
        resultError.value = t("tools.extract.export.failed");
      }
      return;
    }
    const final = await pollExport(resultProject.value.id, created.id);
    if (!final) {
      resultError.value = t("tools.extract.export.timeout");
      return;
    }
    exportRecord.value = final;
    if (final.status === "FAILED" || final.status === "CANCELED") {
      resultError.value = t("tools.extract.export.failed");
    }
  } catch (err) {
    resultError.value = errorText(err);
  } finally {
    exporting.value = false;
  }
}

onMounted(() => {
  if (caps.slots.length === 0) void caps.load().catch(() => undefined);
  void resolveOutputDir().catch(() => undefined);
  void restoreLastRun();
});

/** 返回提取页时恢复上次已结束的 Run，让「执行过程」可回看。 */
async function restoreLastRun(): Promise<void> {
  const restored = await restoreRun("tool_extract");
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
    icon="subtitles"
    :subtitle="t('tools.extract.intro')"
    :title="t('tools.extract.title')"
    :primary-label="primaryLabel"
    :primary-icon="canOpenProject ? 'folder-open' : 'plus'"
    primary-shortcut
    :primary-loading="runBusy"
    :primary-disabled="runBusy || noExtractMode || (!hasProject && createBlocked)"
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
        <CapabilityAccessNotice access-key="tool-extract" />

        <YmPanel :title="t('tools.extract.step.project')">
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
              :label="t('tools.extract.media.label')"
              :error="mediaPathError || mediaError"
              required
            >
              <YmFileRow
                v-if="media"
                :name="media.name"
                :size="formatBytes(media.sizeBytes)"
                :icon="mediaIcon"
                :video="mediaPreviewUrl"
                :media-kind="mediaKind"
              >
                <template #actions>
                  <YmIconButton
                    name="x"
                    :label="t('tools.common.remove')"
                    :size="16"
                    @click="removeMedia"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('tools.extract.media.upload')"
                :hint="t('tools.extract.media.hint')"
                accept=".mp4,.mov,.mkv,.m3u8,.mp3,.wav,.aac,.flac,.m4a,video/*,audio/*"
                @files="onMedia($event[0])"
              />
            </YmField>
          </div>
        </YmPanel>

        <CreationNotice :items="extractNoticeItems" />

        <YmPanel :title="t('tools.extract.step.method')">
          <div class="panel-stack">
            <div v-if="noExtractMode" class="notice notice--warning">
              <YmIcon name="alert" :size="14" class="notice__icon" />
              <span>{{ t("access.extractUnavailable") }}</span>
            </div>
            <YmField :label="t('tools.extract.method.label')">
              <YmRadioGroup
                :model-value="mode"
                :options="methodOptions"
                @update:model-value="onModeChange"
              />
            </YmField>
            <ul class="method-list">
              <li
                v-for="method in methodOptions"
                :key="method.value"
                class="method-item"
                :class="{ 'is-active': method.value === mode, 'is-disabled': method.disabled }"
              >
                <span class="method-item__head">
                  <span class="method-item__title">{{ method.label }}</span>
                  <YmBadge v-if="method.disabled" tone="neutral">
                    {{ t("tools.extract.method.unavailable") }}
                  </YmBadge>
                </span>
                <span class="method-item__desc">{{ method.description }}</span>
              </li>
            </ul>
          </div>
        </YmPanel>

        <YmPanel :title="t('tools.extract.step.language')">
          <div class="panel-stack">
            <YmField
              v-if="mode !== 'asr'"
              :label="t('ocr.quality.label')"
            >
              <OcrQualityPicker v-model="ocrQuality" />
            </YmField>

            <YmField
              :label="t('tools.extract.language.label')"
              :hint="t('tools.extract.language.hint')"
              :error="languageError"
              required
            >
              <div class="langpick">
                <YmTextInput
                  v-model="languageQuery"
                  :placeholder="t('tools.extract.language.search')"
                />
                <div class="langpick__list" role="listbox" :aria-label="t('tools.extract.language.label')">
                  <button
                    v-for="language in filteredLanguages"
                    :key="language.code"
                    type="button"
                    class="langpick__item"
                    :class="{ 'is-selected': selectedLanguage === language.code }"
                    role="option"
                    :aria-selected="selectedLanguage === language.code"
                    @click="selectedLanguage = language.code"
                  >
                    <span class="langpick__name">{{ language.label }}</span>
                    <YmBadge v-if="language.dialect" tone="neutral">
                      {{ t("tools.extract.language.dialect") }}
                    </YmBadge>
                    <YmBadge v-if="language.pending" tone="warning">
                      {{ t("tools.common.pending") }}
                    </YmBadge>
                    <span class="langpick__code">{{ language.submitCode }}</span>
                    <YmIcon v-if="selectedLanguage === language.code" name="check" :size="14" />
                  </button>
                  <p v-if="filteredLanguages.length === 0" class="langpick__empty">
                    {{ t("tools.extract.language.noMatch") }}
                  </p>
                </div>
              </div>

              <div class="code-notes">
                <p class="code-note">
                  {{ t("tools.extract.language.selectedCode", { code: selectedCode }) }}
                </p>
                <p v-if="selectedDialect" class="code-note">
                  {{ t("tools.extract.language.dialectHint") }}
                </p>
                <p v-if="selectedPending" class="code-note code-note--warning">
                  {{ t("tools.extract.language.pendingCode") }}
                </p>
                <p v-if="mode === 'asr_ocr'" class="code-note">
                  {{ t("tools.extract.language.bothNote") }}
                </p>
              </div>
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
            <YmBadge v-if="currentRun" :tone="runStatusTone(currentRun.status)">
              {{ t(runStatusLabelKey(currentRun.status)) }}
            </YmBadge>
            <YmBadge v-else-if="runBusy" tone="info">{{ runPhaseLabel }}</YmBadge>
            <YmBadge v-else tone="neutral">{{ t("tools.extract.result.idle") }}</YmBadge>
          </div>
          <p class="result-head__desc">{{ t("tools.extract.result.desc") }}</p>
          <p v-if="runError" class="flow-error">{{ errorText(runError) }}</p>
          <p v-if="runPartial" class="flow-warning">{{ t("tools.flow.partial") }}</p>
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
              <span>{{ currentRun?.name ?? t("tools.extract.result.running") }}</span>
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
          :task-id="extractTask?.id"
          :active="runBusy"
          :task-status="extractTask?.status"
          :waiting-message-key="extractTask?.error?.messageKey"
        />

        <YmCard v-if="resultSegments.length">
          <template #header>
            <div class="summary-head">
              <span>{{ t("tools.extract.result.subtitles") }}</span>
              <YmBadge tone="success">
                {{ t("tools.common.selectedCount", { count: resultSegments.length }) }}
              </YmBadge>
            </div>
          </template>
          <ul class="segment-list">
            <li v-for="segment in resultSegments" :key="segment.id" class="segment-row">
              <span class="segment-time">
                {{ formatTimestamp(segment.startMs) }} → {{ formatTimestamp(segment.endMs) }}
              </span>
              <span class="segment-text">{{ segment.text }}</span>
            </li>
          </ul>
          <div class="export-block">
            <h3 class="export-block__title">{{ t("tools.extract.export.title") }}</h3>
            <YmField
              :label="t('tools.extract.export.dir')"
              :hint="t('tools.common.export.defaultDirHint')"
            >
              <YmTextInput v-model="exportDir" disabled />
            </YmField>
            <YmField :label="t('tools.extract.export.naming')">
              <YmTextInput v-model="namingTemplate" />
            </YmField>
            <YmButton
              variant="primary"
              icon="download"
              :loading="exporting"
              :disabled="exporting"
              @click="exportSrt"
            >
              {{ t("tools.extract.export.action") }}
            </YmButton>
            <p v-if="exportRecord" class="export-done">
              {{
                exportRecord.outputPath
                  ? t("tools.extract.export.done", { path: exportRecord.outputPath })
                  : t("tools.extract.export.queued")
              }}
            </p>
          </div>
        </YmCard>

        <div v-else-if="!runBusy && !currentRun" class="results-empty">
          <YmEmpty
            :title="t('tools.common.result.emptyTitle')"
            :description="t('tools.extract.result.emptyDesc')"
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
.notice--warning {
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
}
.notice__icon {
  flex: none;
  margin-top: 1px;
}
.method-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.method-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-2) var(--space-3);
  border-left: 2px solid var(--color-border);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.method-item.is-active {
  border-left-color: var(--color-primary);
  background: var(--color-primary-soft);
}
.method-item.is-disabled {
  opacity: 0.6;
  border-left-style: dashed;
}
.method-item__head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.method-item__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.method-item.is-active .method-item__title {
  color: var(--color-primary);
}
.method-item__desc {
  line-height: var(--line-height-normal);
}
.langpick {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.langpick__list {
  max-height: 220px;
  overflow: auto;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.langpick__item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  padding: var(--space-2) var(--space-3);
  text-align: left;
  font-size: var(--font-size-sm);
  border-bottom: 1px solid var(--color-border);
}
.langpick__item:last-child {
  border-bottom: none;
}
.langpick__item:hover {
  background: var(--color-surface-2);
}
.langpick__item.is-selected {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.langpick__name {
  flex: 1;
  min-width: 0;
}
.langpick__code {
  flex: none;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.langpick__empty {
  padding: var(--space-3);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.code-notes {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: var(--space-2);
}
.code-note {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.code-note--warning {
  color: var(--color-warning-strong);
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
.summary-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
}
.summary-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
  font-size: var(--font-size-sm);
}
.summary-row dt {
  flex: none;
  color: var(--color-text-muted);
}
.summary-row dd {
  margin: 0;
  min-width: 0;
  text-align: right;
  overflow-wrap: anywhere;
}
.summary-note {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
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
.segment-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  max-height: 320px;
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
  gap: var(--space-2);
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.export-block__title {
  margin: 0;
  font-size: var(--font-size-sm);
}
.export-done {
  margin: 0;
  color: var(--color-success-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
</style>

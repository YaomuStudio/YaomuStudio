<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api } from "@/api";
import { formatBytes, getLocale, t } from "@/i18n";
import { errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import { messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { TARGET_LANGUAGES, targetLanguageLabel } from "@/domain/languages";
import { detectFileLanguage } from "@/domain/subtitleLanguage";
import type {
  Asset,
  Capability,
  CapabilitySlotKey,
  Project,
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
import CreationNotice from "@/components/tools/CreationNotice.vue";
import ExecutionLogPanel from "@/components/tools/ExecutionLogPanel.vue";
import RunOptionsSummary from "@/components/RunOptionsSummary.vue";
import LanguageMultiSelect from "@/components/LanguageMultiSelect.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import { useToolRun } from "../tools/useToolRun";
import { useRunTask } from "../tools/useRunTask";

interface PickedFile {
  name: string;
  sizeBytes: number;
  lastModified: number;
  file: File;
}

interface TranslatedFile extends PickedFile {
  /** 上传时按内容优先识别；识别不到时回退文件名/已选语言。 */
  language?: string;
}

interface FullDubRow {
  language: string;
  asset: Asset;
}

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const caps = useCapabilitiesStore();

/** 所需引擎未就绪时禁用创建，并在页脚说明缺少的能力。 */
const { blocked: createBlocked, blockedText: createBlockedText } = useCreateGuard("voice-clone");

/** 连续创建：清空表单、结果与当前 Run 视图，保留本工具的配置项。 */
function resetForm(): void {
  projectName.value = "";
  sourceAudio.value = null;
  originalSubtitle.value = null;
  translatedSubtitles.value = [];
  showErrors.value = false;
  pathError.value = "";
  resultError.value = "";
  resultRows.value = [];
  saveBusy.value = "";
  saveMessage.value = null;
  saveError.value = null;
  resetRun();
}

const { hasProject, canOpenProject, primaryLabel, onPrimary, openProject, startNew } = useCreationFlow({
  projectId: () => currentRun.value?.projectId,
  busy: () => runBusy.value,
  create,
  reset: resetForm,
});

const projectName = ref("");
const sourceAudio = ref<PickedFile | null>(null);
const originalSubtitle = ref<PickedFile | null>(null);
const translatedSubtitles = ref<TranslatedFile[]>([]);
const sourceLanguage = ref("und");
const targetLanguages = ref<string[]>(loadRecentTargetLanguages("voice-clone"));
const validateTimestamps = ref(false);
// 时长调整尚未实现：新建表单固定关闭且不可开启，不伪造可用性。
const adjustDuration = ref(false);
const showErrors = ref(false);
const pathError = ref("");
const resultError = ref("");
const resultRows = ref<FullDubRow[]>([]);
const saveBusy = ref("");
const saveMessage = ref<SafeNotice | null>(null);
const saveError = ref<SafeNotice | null>(null);
const saveMessageText = computed(() => renderNotice(saveMessage.value));
const saveErrorText = computed(() => renderNotice(saveError.value));

const relatedSlotKeys: CapabilitySlotKey[] = ["voice-clone", "ffmpeg"];
const relatedCapabilities = computed<Capability[]>(() =>
  relatedSlotKeys
    .map((slot) => caps.slotState(slot))
    .filter((cap): cap is Capability => cap !== undefined),
);
const capsReadyCount = computed(
  () => relatedCapabilities.value.filter((cap) => cap.state === "READY").length,
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

const resultLanguages = computed(() => currentRun.value?.languages ?? targetLanguages.value);
const resultGroups = computed(() =>
  resultLanguages.value
    .map((language) => ({
      language,
      label: languageLabel(language),
      rows: resultRows.value.filter((row) => row.language === language),
    }))
    .filter((group) => group.rows.length > 0),
);
/** 只展示实际执行的节点；任务在等执行器时节点也显示等待资源，不能谎称运行中。 */
const runNodeRows = computed(() =>
  (currentRun.value?.nodes ?? [])
    .filter((node) => node.status !== "skipped")
    .map((node) => {
      const waiting = node.status === "running" && cloneTask.value?.status === "WAITING_RESOURCE";
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
const cloneTask = useRunTask(
  () => currentRun.value?.id,
  "07_clone",
  () => runBusy.value,
);

const projectError = computed(() =>
  showErrors.value && !projectName.value.trim() ? t("tools.common.projectNameErr") : "",
);
const sourceError = computed(() =>
  showErrors.value && !sourceAudio.value ? t("voiceClone.source.err") : "",
);
const originalError = computed(() =>
  showErrors.value && !originalSubtitle.value ? t("voiceClone.sub.originalErr") : "",
);
const targetError = computed(() =>
  showErrors.value && targetLanguages.value.length === 0
    ? t("voiceClone.sub.targetErr")
    : "",
);
const footerStatus = computed(() => {
  if (cloneTask.value?.status === "WAITING_RESOURCE") return t("state.waitingResource");
  if (runBusy.value) return t("creation.footer.running");
  if (createBlocked.value) return createBlockedText.value;
  const missing: string[] = [];
  if (!projectName.value.trim()) missing.push(t("tools.common.projectName"));
  if (!sourceAudio.value) missing.push(t("voiceClone.source.label"));
  if (!originalSubtitle.value) missing.push(t("voiceClone.sub.original"));
  if (targetLanguages.value.length === 0) missing.push(t("voiceClone.sub.target"));
  if (translatedSubtitles.value.length === 0) missing.push(t("voiceClone.sub.translated"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});
const translatedError = computed(() => {
  if (translatedSubtitles.value.length === 0) {
    return showErrors.value ? t("voiceClone.sub.translatedErr") : "";
  }
  // 已上传文件后即时显示 1:1 匹配问题，避免点击创建才知道。
  return resolveTranslatedLanguages().error;
});

function languageLabel(code: string): string {
  const language = TARGET_LANGUAGES.find((item) => item.code === code);
  return language ? targetLanguageLabel(language, getLocale()) : code;
}

function translatedLanguageLabel(file: TranslatedFile): string {
  if (file.language) return languageLabel(file.language);
  return t("voiceClone.sub.translatedAuto");
}

function resolveFilePath(file: File): string {
  if (desktopApi) return desktopApi.getPathForFile(file).trim();
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

/**
 * 译文语言解析：优先按文件名识别，识别不到时按已选目标语言顺序兜底。
 * 文件名识别出的语言不在已选范围时由 create 补入目标语言（与参考分支一致）；
 * 重复语言或译文字幕多于已选目标语言时给出明确错误。
 */
function resolveTranslatedLanguages(): { languages: string[]; error: string } {
  const files = translatedSubtitles.value;
  const targets = targetLanguages.value;
  if (files.length === 0) return { languages: [], error: t("voiceClone.sub.translatedErr") };
  // 1:1：每种目标语言恰好一份译文字幕，不能多也不能少。
  if (targets.length !== files.length) {
    return {
      languages: [],
      error: t("voiceClone.sub.languageCount", { targets: targets.length, files: files.length }),
    };
  }
  const seen = new Set<string>();
  const languages: string[] = [];
  for (const file of files) {
    const language = file.language ?? "";
    if (!language) return { languages: [], error: t("voiceClone.sub.languageErr") };
    if (language === sourceLanguage.value) {
      return {
        languages: [],
        error: t("voiceClone.sub.languageSource", { language: languageLabel(language) }),
      };
    }
    if (!targets.includes(language) || seen.has(language)) {
      return { languages: [], error: t("voiceClone.sub.languageErr") };
    }
    seen.add(language);
    languages.push(language);
  }
  return { languages, error: "" };
}

function toPicked(file: File): PickedFile {
  return {
    name: file.name,
    sizeBytes: file.size,
    lastModified: file.lastModified,
    file,
  };
}

function fileKey(file: PickedFile): string {
  return `${file.name}:${file.sizeBytes}:${file.lastModified}`;
}

function onSourceAudio(files: File[]): void {
  const file = files[0];
  if (file) {
    sourceAudio.value = toPicked(file);
    pathError.value = "";
  }
}

async function onOriginalSubtitle(files: File[]): Promise<void> {
  const file = files[0];
  if (file) {
    originalSubtitle.value = toPicked(file);
    const detected = await detectFileLanguage(file);
    sourceLanguage.value = detected ?? "und";
    if (detected) {
      // 原文语种不能同时作为配音目标语言。
      targetLanguages.value = targetLanguages.value.filter((code) => code !== detected);
    }
    pathError.value = "";
  }
}

function removeOriginalSubtitle(): void {
  originalSubtitle.value = null;
  sourceLanguage.value = "und";
}

async function onTranslatedSubtitles(files: File[]): Promise<void> {
  const keys = new Set(translatedSubtitles.value.map(fileKey));
  for (const file of files) {
    const picked = toPicked(file);
    const key = fileKey(picked);
    if (keys.has(key)) continue;
    // 只按字幕全文内容识别语种，文件名不参与判断。
    const detected = await detectFileLanguage(file);
    translatedSubtitles.value.push({ ...picked, language: detected });
    keys.add(key);
    if (
      detected &&
      detected !== sourceLanguage.value &&
      !targetLanguages.value.includes(detected)
    ) {
      targetLanguages.value.push(detected);
    }
  }
  pathError.value = "";
}

function removeTranslated(index: number): void {
  translatedSubtitles.value.splice(index, 1);
}

const targetOptions = computed(() =>
  TARGET_LANGUAGES.filter((language) => language.code !== sourceLanguage.value),
);

function toggleLanguage(code: string, checked: boolean): void {
  // 原文语种不可作为配音目标语言。
  if (code === sourceLanguage.value) return;
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

async function create(): Promise<void> {
  showErrors.value = true;
  resultError.value = "";
  resultRows.value = [];
  saveMessage.value = null;
  saveError.value = null;
  if (
    !projectName.value.trim() ||
    !sourceAudio.value ||
    !originalSubtitle.value ||
    !sourceLanguage.value ||
    targetLanguages.value.length === 0
  ) {
    return;
  }
  if (translatedError.value) return;
  const sourcePath = resolveFilePath(sourceAudio.value.file);
  const originalPath = resolveFilePath(originalSubtitle.value.file);
  const resolved = resolveTranslatedLanguages();
  if (resolved.error) return;
  const translatedPaths = translatedSubtitles.value.map((item, index) => ({
    path: resolveFilePath(item.file),
    language: resolved.languages[index],
  }));
  if (!sourcePath || !originalPath || translatedPaths.some((item) => !item.path)) {
    pathError.value = t("voiceClone.pathUnavailable");
    return;
  }
  // 文件名识别出的目标语言不在已选范围时补入，保持提交语言与实际字幕一致。
  for (const item of translatedPaths) {
    if (!targetLanguages.value.includes(item.language)) {
      targetLanguages.value.push(item.language);
    }
  }
  showErrors.value = false;
  const result = await startRun({
    projectName: projectName.value.trim(),
    filePath: sourcePath,
    payload: {
      template: "tool_voice_clone",
      sourceLanguage: sourceLanguage.value,
      targetLanguages: [...targetLanguages.value],
      validateTimestamps: validateTimestamps.value,
      adjustDuration: adjustDuration.value,
    },
    afterImport: async ({ projectId }) => {
      const originalImport = await api.importAssets({
        projectId,
        paths: [originalPath],
        copyMode: true,
      });
      if (!originalImport.assetIds[0]) throw new Error("voiceClone.importFailed");
      await api.importSubtitleTrack({
        projectId,
        assetId: originalImport.assetIds[0],
        language: sourceLanguage.value,
        type: "transcription",
      });
      for (const item of translatedPaths) {
        const imported = await api.importAssets({
          projectId,
          paths: [item.path],
          copyMode: true,
        });
        if (!imported.assetIds[0]) throw new Error("voiceClone.importFailed");
        await api.importSubtitleTrack({
          projectId,
          assetId: imported.assetIds[0],
          language: item.language,
          type: "translation",
        });
      }
    },
  });
  if (!result) return;
  rememberTargetLanguages("voice-clone", targetLanguages.value);
  await loadResult(result.project);
}

async function loadResult(project: Project): Promise<void> {
  const runId = currentRun.value?.id;
  const rows = project.assets.filter(
    (asset) =>
      asset.status === "ready" &&
      asset.role === "full_dub_audio" &&
      (asset.type === "audio" || asset.type === "audio_track") &&
      (!runId || !asset.runId || asset.runId === runId),
  );
  const languages = resultLanguages.value;
  const ordered = [...rows].sort((a, b) => {
    const left = languages.indexOf(a.language ?? "");
    const right = languages.indexOf(b.language ?? "");
    const leftRank = left === -1 ? languages.length : left;
    const rightRank = right === -1 ? languages.length : right;
    if (leftRank !== rightRank) return leftRank - rightRank;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
  resultRows.value = ordered.map((asset) => ({
    language: asset.language ?? "",
    asset,
  }));
  const finished = currentRun.value?.status === "SUCCEEDED" || currentRun.value?.status === "PARTIAL";
  if (finished && resultRows.value.length === 0) {
    resultError.value = t("voiceClone.result.missing");
  }
}

function fullDubFileName(row: FullDubRow): string {
  const match = /\.([a-z0-9]+)$/i.exec(row.asset.name);
  const ext = match ? `.${match[1].toLowerCase()}` : ".mp3";
  return row.language ? `clone-${row.language}${ext}` : `clone-full${ext}`;
}

async function saveFullDub(row: FullDubRow): Promise<void> {
  saveBusy.value = row.asset.id;
  saveMessage.value = null;
  saveError.value = null;
  try {
    const saved = await api.saveAsset({
      assetId: row.asset.id,
      suggestedName: fullDubFileName(row),
      title: t("voiceClone.result.save"),
    });
    if (!saved) return;
    saveMessage.value = messageNotice("voiceClone.result.saved", { path: saved.path });
  } catch {
    saveError.value = messageNotice("voiceClone.result.saveFailed");
  } finally {
    saveBusy.value = "";
  }
}

onMounted(() => {
  if (caps.slots.length === 0) void caps.load().catch(() => undefined);
  void (async () => {
    const restored = await restoreRun("tool_voice_clone");
    if (!restored?.projectId) return;
    if (restored.status !== "SUCCEEDED" && restored.status !== "PARTIAL") return;
    const project = await api.getProject(restored.projectId);
    await loadResult(project);
  })().catch(() => undefined);
});
</script>

<template>
  <WorkbenchPage
    scaffold
    icon="mic"
    :title="t('voiceClone.title')"
    :subtitle="t('voiceClone.intro')"
    :primary-label="primaryLabel"
    :primary-icon="canOpenProject ? 'folder-open' : 'plus'"
    primary-shortcut
    :primary-disabled="runBusy || (!hasProject && createBlocked)"
    :primary-loading="runBusy"
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
        <CapabilityAccessNotice access-key="voice-clone" />

        <YmPanel :title="t('voiceClone.step.source')">
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
              :label="t('voiceClone.source.label')"
              :hint="t('voiceClone.source.hint')"
              :error="sourceError"
              required
            >
              <YmFileRow
                v-if="sourceAudio"
                :name="sourceAudio.name"
                :size="formatBytes(sourceAudio.sizeBytes)"
                icon="wave"
                media-kind="audio"
              >
                <template #actions>
                  <YmIconButton
                    name="x"
                    :label="t('tools.common.remove')"
                    :size="16"
                    @click="sourceAudio = null"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('voiceClone.source.upload')"
                :hint="t('voiceClone.source.dropHint')"
                accept=".mp3,.wav,.aac,.flac,.m4a,audio/*"
                @files="onSourceAudio"
              />
            </YmField>

            <div class="notice notice--info">
              <YmIcon name="info" :size="14" class="notice__icon" />
              <span>{{ t("voiceClone.source.videoHint") }}</span>
            </div>
          </div>
        </YmPanel>

        <CreationNotice
          :items="[
            t('voiceClone.source.warn'),
            t('voiceClone.sub.countHint'),
          ]"
        />

        <YmPanel :title="t('voiceClone.step.subtitles')">
          <div class="panel-stack">
            <YmField
              :label="t('voiceClone.sub.original')"
              :hint="t('voiceClone.sub.originalHint')"
              :error="originalError"
              required
            >
              <YmFileRow
                v-if="originalSubtitle"
                :name="originalSubtitle.name"
                :size="formatBytes(originalSubtitle.sizeBytes)"
                icon="subtitles"
              >
                <template #actions>
                  <YmIconButton
                    name="x"
                    :label="t('tools.common.remove')"
                    :size="16"
                    @click="removeOriginalSubtitle"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('voiceClone.sub.originalUpload')"
                accept=".srt"
                @files="onOriginalSubtitle"
              />
            </YmField>

            <p v-if="originalSubtitle" class="detected-language">
              {{
                sourceLanguage === "und"
                  ? t("voiceClone.sub.originalDetectPending")
                  : t("voiceClone.sub.originalDetected", {
                      language: languageLabel(sourceLanguage),
                    })
              }}
            </p>

            <YmField
              :label="t('voiceClone.sub.target')"
              :hint="t('voiceClone.sub.targetHint')"
              :error="targetError"
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

            <YmField
              :label="t('voiceClone.sub.translated')"
              :hint="t('voiceClone.sub.translatedHint')"
              :error="translatedError"
              required
            >
              <YmDropzone
                multiple
                :title="t('voiceClone.sub.translatedUpload')"
                accept=".srt"
                @files="onTranslatedSubtitles"
              />
              <ul v-if="translatedSubtitles.length" class="file-list">
                <li v-for="(file, index) in translatedSubtitles" :key="fileKey(file)">
                  <YmFileRow
                    :name="file.name"
                    :size="formatBytes(file.sizeBytes)"
                    icon="subtitles"
                  >
                    <template #between>
                      <YmBadge tone="info">{{ translatedLanguageLabel(file) }}</YmBadge>
                    </template>
                    <template #actions>
                      <YmIconButton
                        name="x"
                        :label="t('tools.common.remove')"
                        :size="16"
                        @click="removeTranslated(index)"
                      />
                    </template>
                  </YmFileRow>
                </li>
              </ul>
            </YmField>
          </div>
        </YmPanel>

        <YmPanel :title="t('voiceClone.step.options')">
          <div class="panel-stack">
            <div class="switch-row">
              <div class="switch-row__text">
                <span class="switch-row__title">{{ t("voiceClone.opt.validate") }}</span>
                <span class="switch-row__hint">{{ t("voiceClone.opt.validateHint") }}</span>
              </div>
              <YmSwitch
                v-model="validateTimestamps"
                :aria-label="t('voiceClone.opt.validate')"
              />
            </div>
            <div class="switch-row" data-testid="adjust-duration-hint" data-status="unavailable">
              <div class="switch-row__text">
                <span class="switch-row__title">{{ t("voiceClone.opt.adjust") }}</span>
                <span class="switch-row__hint">{{ t("voiceClone.opt.adjustHint") }}</span>
              </div>
              <YmSwitch
                v-model="adjustDuration"
                disabled
                :aria-label="t('voiceClone.opt.adjust')"
              />
            </div>
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
            <YmBadge v-else tone="neutral">{{ t("voiceClone.result.idle") }}</YmBadge>
          </div>
          <p class="result-head__desc">{{ t("voiceClone.result.desc") }}</p>
          <p v-if="runError" class="flow-error">{{ errorText(runError) }}</p>
          <p v-if="runPartial" class="flow-warning">{{ t("tools.flow.partial") }}</p>
          <p v-if="pathError" class="flow-error">{{ pathError }}</p>
          <p v-if="resultError" class="flow-error">{{ resultError }}</p>
          <p v-if="saveErrorText" class="flow-error"><bdi>{{ saveErrorText }}</bdi></p>
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
              <span>{{ currentRun?.name ?? t("voiceClone.result.running") }}</span>
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
          <RunOptionsSummary :params="currentRun?.params" />
        </YmCard>

        <ExecutionLogPanel
          :task-id="cloneTask?.id"
          :active="runBusy"
          :task-status="cloneTask?.status"
          :waiting-message-key="cloneTask?.error?.messageKey"
        />

        <YmCard v-for="group in resultGroups" :key="group.language">
          <template #header>
            <div class="summary-head">
              <span>{{ group.label }}</span>
            </div>
          </template>
          <ul class="candidate-list">
            <li v-for="row in group.rows" :key="row.asset.id" class="candidate-row">
              <span class="candidate-text">{{ row.asset.name }}</span>
              <span class="candidate-meta">{{ formatBytes(row.asset.sizeBytes) }}</span>
              <YmButton
                size="sm"
                :disabled="saveBusy === row.asset.id"
                @click="saveFullDub(row)"
              >
                {{ t("voiceClone.result.save") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <p v-if="saveMessageText" class="save-message"><bdi>{{ saveMessageText }}</bdi></p>

        <div v-if="!currentRun && !runBusy" class="results-empty">
          <YmEmpty
            :title="t('voiceClone.result.emptyTitle')"
            :description="t('voiceClone.result.emptyDesc')"
            icon="mic"
          />
        </div>
      </div>
    </template>

    <template #footer>
      <span class="wb-foot-form">{{ footerStatus }}</span>
      <span
        class="wb-foot-status"
        :class="{
          'is-ready':
            relatedCapabilities.length > 0 &&
            capsReadyCount === relatedCapabilities.length,
        }"
      >
        {{
          relatedCapabilities.length > 0 && capsReadyCount === relatedCapabilities.length
            ? t("tools.common.capsReady", {
                ready: capsReadyCount,
                total: relatedCapabilities.length,
              })
            : t("tools.common.capsPending")
        }}
      </span>
    </template>
  </WorkbenchPage>
</template>

<style scoped>
.page-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.panel-stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.file-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: var(--space-2) 0 0;
  padding: 0;
  list-style: none;
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
.detected-language {
  margin: calc(var(--space-2) * -1) 0 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}
.switch-row__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.switch-row__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.switch-row__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
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
.candidate-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.candidate-row {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--font-size-sm);
}
.candidate-row:last-child {
  border-bottom: none;
}
.candidate-text {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
.candidate-meta {
  flex: none;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.save-message {
  margin: 0;
  color: var(--color-success-strong);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}
.wb-foot-form {
  min-width: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.wb-foot-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.wb-foot-status::before {
  content: "";
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--color-warning);
}
.wb-foot-status.is-ready::before {
  background: var(--color-success);
}
</style>

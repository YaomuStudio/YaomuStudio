<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { formatBytes, getLocale, t } from "@/i18n";
import { api } from "@/api";
import { errorText, preflightIssueKey } from "@/i18n/errorDisplay";
import {
  fallbackErrorNotice,
  messageNotice,
  renderNotice,
  type SafeNotice,
} from "@/i18n/notice";
import {
  evaluateFeatureAccess,
  slotLabelKey,
  type AccessResult,
} from "@/domain/capabilityAccess";
import { TARGET_LANGUAGES, targetLanguageLabel } from "@/domain/languages";
import { detectFileLanguage } from "@/domain/subtitleLanguage";
import type {
  Asset,
  SubtitleStyle,
  AudioCandidate,
  Capability,
  CapabilitySlotKey,
  CustomPipelineFeature,
  CustomPipelineTemplate,
  ExportRecord,
  PipelineNodeKey,
  Project,
  ReviewGate,
  SubtitleRevision,
  SubtitleTrack,
  WorkflowRun,
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
import { useRunNodeTasks } from "@/composables/useRunNodeTasks";
import { useCreationFlow } from "@/composables/useCreationFlow";
import WorkbenchPage from "@/components/layout/WorkbenchPage.vue";
import CapabilityAccessNotice from "@/components/layout/CapabilityAccessNotice.vue";
import LanguageMultiSelect from "@/components/LanguageMultiSelect.vue";
import RunOptionsSummary from "@/components/RunOptionsSummary.vue";
import CreationNotice from "@/components/tools/CreationNotice.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDropzone from "@/components/ui/YmDropzone.vue";
import YmFileRow from "@/components/ui/YmFileRow.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmIconButton from "@/components/ui/YmIconButton.vue";
import OcrQualityPicker from "@/components/ui/OcrQualityPicker.vue";
import YmPanel from "@/components/ui/YmPanel.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import StylePanel from "@/pages/ManualCorrection/StylePanel.vue";
import { useDefaultOutputDir } from "@/composables/useDefaultOutputDir";
import { loadAllNodeArtifacts, useToolRun } from "./useToolRun";
import { useMediaPreview } from "@/composables/useMediaPreview";
import {
  loadRecentTargetLanguages,
  rememberTargetLanguages,
} from "@/composables/useRecentTargetLanguages";

interface PickedFile {
  name: string;
  sizeBytes: number;
}

interface SubtitleFile extends PickedFile {
  file: File;
  language: string;
}

interface TrackResult {
  language: string;
  track: SubtitleTrack;
  revision: SubtitleRevision;
  lineCount: number;
}

interface CandidateRow {
  key: string;
  language: string;
  segmentId: string;
  text: string;
  candidate: AudioCandidate;
  alternatives: number;
}

interface ExportState {
  busy: boolean;
  record?: ExportRecord;
  error?: SafeNotice;
}

type OCRCode = "chinese" | "chinese_traditional" | "english" | "japanese";
type EraseScope = "dialog_only" | "dialog_and_art";
type TemplateNotice =
  | ""
  | "saved"
  | "applied"
  | "deleted"
  | "error"
  | "loadFailed"
  | "saveFailed"
  | "tooMany"
  | "nameTooLong"
  | "languagesRequired";

const TERMINAL_EXPORT = new Set(["SUCCEEDED", "FAILED", "CANCELED"]);
const TEMPLATE_LIMIT = 20;

const CUSTOM_FEATURES: { value: CustomPipelineFeature; labelKey: string; descKey: string }[] = [
  { value: "separation", labelKey: "tools.custom.function.separation", descKey: "tools.custom.function.separationDesc" },
  { value: "erase", labelKey: "tools.custom.function.erase", descKey: "tools.custom.function.eraseDesc" },
  { value: "translate", labelKey: "tools.custom.function.translate", descKey: "tools.custom.function.translateDesc" },
  { value: "clone", labelKey: "tools.custom.function.clone", descKey: "tools.custom.function.cloneDesc" },
  { value: "mix", labelKey: "tools.custom.function.mix", descKey: "tools.custom.function.mixDesc" },
];

const NODE_LABEL_KEYS: Record<PipelineNodeKey, string> = {
  "01_import": "tools.custom.node.import",
  "02_analyze": "tools.custom.node.analyze",
  "03_separate": "tools.custom.node.separate",
  "04_extract": "tools.custom.node.extract",
  "05_erase": "tools.custom.node.erase",
  "06_translate": "tools.custom.node.translate",
  "07_clone": "tools.custom.node.clone",
  "08_mix": "tools.custom.node.mix",
};

const TEMPLATE_NOTICE_KEYS: Record<Exclude<TemplateNotice, "">, string> = {
  saved: "tools.custom.template.saved",
  applied: "tools.custom.template.applied",
  deleted: "tools.custom.template.deleted",
  error: "tools.custom.template.error",
  loadFailed: "tools.custom.template.loadFailed",
  saveFailed: "tools.custom.template.saveFailed",
  tooMany: "tools.custom.template.tooMany",
  nameTooLong: "tools.custom.template.nameTooLong",
  languagesRequired: "tools.custom.template.languagesRequired",
};

const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const caps = useCapabilitiesStore();

/** 所需引擎未就绪时禁用创建，并在页脚说明缺少的能力。 */
const { blocked: createBlocked, blockedText: createBlockedText } = useCreateGuard("tool-custom");

/** 连续创建：清空表单、结果与当前 Run 视图，保留本工具的配置项。 */
function resetForm(): void {
  projectName.value = "";
  mediaFile.value = null;
  media.value = null;
  originalSubtitle.value = null;
  translatedSubtitles.value = [];
  showErrors.value = false;
  mediaPathError.value = "";
  subtitleLanguageError.value = "";
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
const subtitleStyle = ref<SubtitleStyle>({});
const mediaFile = ref<File | null>(null);
const media = ref<PickedFile | null>(null);
const { kind: mediaKind, url: mediaPreviewUrl } = useMediaPreview(mediaFile);
const selectedFunctions = ref<CustomPipelineFeature[]>([]);
const sourceLanguage = ref("");
const selectedLanguages = ref<string[]>(loadRecentTargetLanguages("custom"));
const eraseScope = ref<EraseScope>("dialog_only");
const selectedOCR = ref<OCRCode>("chinese");
const ocrQuality = ref<"standard" | "fine">("fine");
const originalSubtitle = ref<{ file: File } & PickedFile | null>(null);
const translatedSubtitles = ref<SubtitleFile[]>([]);
const validateTimestamps = ref(false);
// 时长调整尚未实现：新建模板默认关闭；旧模板显式记录 true 时保留展示，
// 只允许用户明确取消，取消后不可再开启。
const adjustDuration = ref(false);
const showErrors = ref(false);
const mediaPathError = ref("");
const subtitleLanguageError = ref("");

const templates = ref<CustomPipelineTemplate[]>([]);
const templateName = ref("");
const templateNotice = ref<TemplateNotice>("");
const templateBusy = ref(false);

const resultProject = ref<Project | null>(null);
const separationAssets = ref<Asset[]>([]);
const eraseAssets = ref<Asset[]>([]);
const mixAssets = ref<Asset[]>([]);
const translatedTracks = ref<TrackResult[]>([]);
const candidateRows = ref<CandidateRow[]>([]);
const resultError = ref("");
const saveBusy = ref("");
const saveMessage = ref<SafeNotice | null>(null);
const saveError = ref<SafeNotice | null>(null);
const saveMessageText = computed(() => renderNotice(saveMessage.value));
const saveErrorText = computed(() => renderNotice(saveError.value));

const { outputDir: exportDir, resolve: resolveOutputDir } = useDefaultOutputDir();
const namingTemplate = ref("{project}_{language}_{timestamp}");
const exporting = ref<Record<string, ExportState>>({});

const pendingReviews = ref<ReviewGate[]>([]);
const reviewBusy = ref("");
let reviewResolver: (() => void) | null = null;

const {
  busy: runBusy,
  phase: runPhase,
  run: currentRun,
  issues: runIssues,
  error: runError,
  partial: runPartial,
  start: startRun,
  reset: resetRun,
} = useToolRun();

const selectedSet = computed(() => new Set(selectedFunctions.value));
const needsText = computed(() =>
  ["translate", "clone", "mix"].some((item) => selectedSet.value.has(item as CustomPipelineFeature)),
);
const eraseSelected = computed(() => selectedSet.value.has("erase"));
const cloneSelected = computed(() => selectedSet.value.has("clone"));
const mixSelected = computed(() => selectedSet.value.has("mix"));
/** 克隆配音/混合成片依赖字幕翻译：依赖项自动勾选并锁定，避免误取消造成组合失效。 */
const translateLocked = computed(() => cloneSelected.value || mixSelected.value);
const creationNoticeItems = computed(() => {
  const items: string[] = [];
  if (eraseSelected.value) items.push(t("tools.erase.scope.warn"));
  if (cloneSelected.value) items.push(t("voiceClone.source.warn"));
  return items;
});

// 每个功能按所需槽位判定；未准入不可勾选，未就绪保留但提示。
const featureAccess = computed<Record<CustomPipelineFeature, AccessResult>>(() => {
  const result = {} as Record<CustomPipelineFeature, AccessResult>;
  for (const feature of CUSTOM_FEATURES) {
    result[feature.value] = evaluateFeatureAccess(caps.slots, feature.value);
  }
  return result;
});
const availableFeatureCount = computed(
  () => CUSTOM_FEATURES.filter((feature) => featureAccess.value[feature.value].level !== "hidden").length,
);
const noAvailableFeatures = computed(() => availableFeatureCount.value === 0);
const blockedFeatures = computed(() =>
  selectedFunctions.value.filter(
    (feature) => featureAccess.value[feature].level === "hidden",
  ),
);
const hasBlockedSelection = computed(() => blockedFeatures.value.length > 0);
const footerStatus = computed(() => {
  if (runWaiting.value) return t("state.waitingResource");
  if (runBusy.value) return t("creation.footer.running");
  if (createBlocked.value) return createBlockedText.value;
  const missing: string[] = [];
  if (!projectName.value.trim()) missing.push(t("tools.common.projectName"));
  if (!media.value) missing.push(t("tools.custom.media.label"));
  if (selectedFunctions.value.length === 0) missing.push(t("tools.custom.functions.label"));
  if (needsText.value && selectedLanguages.value.length === 0) {
    missing.push(t("tools.translate.languages.label"));
  }
  if (hasBlockedSelection.value || noAvailableFeatures.value) {
    missing.push(t("tools.common.capability"));
  }
  if (adjustDuration.value) missing.push(t("tools.custom.opt.adjust"));
  return missing.length
    ? t("creation.footer.missing", { items: missing.join(" · ") })
    : t("creation.footer.ready");
});
const blockedFeatureNames = computed(() =>
  blockedFeatures.value.map((feature) => functionLabel(feature)).join(" · "),
);

function featureAvailable(value: CustomPipelineFeature): boolean {
  return featureAccess.value[value].level !== "hidden";
}

function featureLabels(slots: CapabilitySlotKey[]): string {
  return slots.map((slot) => t(slotLabelKey(slot))).join(" · ");
}

function featureSetupLabels(value: CustomPipelineFeature): string {
  return featureLabels(featureAccess.value[value].setup);
}

function featureMissingLabels(value: CustomPipelineFeature): string {
  return featureLabels(featureAccess.value[value].missing);
}

const targetOptions = computed(() =>
  TARGET_LANGUAGES.filter((language) => language.code !== sourceLanguage.value),
);
const targetSelectOptions = computed(() =>
  TARGET_LANGUAGES.map((language) => ({
    value: language.code,
    label: targetLanguageLabel(language, getLocale()),
  })),
);
const scopeOptions = computed(() => [
  { value: "dialog_only", label: t("tools.custom.scope.dialog") },
  { value: "dialog_and_art", label: t("tools.custom.scope.art") },
]);

const relatedSlotKeys = computed<CapabilitySlotKey[]>(() => {
  const keys = new Set<CapabilitySlotKey>();
  if (selectedSet.value.has("separation")) keys.add("separation");
  if (eraseSelected.value) {
    keys.add("ocr");
    keys.add("subtitle-erase");
  }
  if (needsText.value) keys.add("translate");
  if (cloneSelected.value) {
    keys.add("voice-clone");
  }
  if (selectedFunctions.value.length > 0) keys.add("ffmpeg");
  return [...keys];
});
const relatedCapabilities = computed<Capability[]>(() =>
  relatedSlotKeys.value
    .map((slot) => caps.slotState(slot))
    .filter((cap): cap is Capability => cap !== undefined),
);

const projectError = computed(() =>
  showErrors.value && !projectName.value.trim() ? t("tools.common.projectNameErr") : "",
);
const mediaError = computed(() =>
  showErrors.value && !media.value ? t("tools.custom.media.err") : "",
);
const functionsError = computed(() =>
  showErrors.value && selectedFunctions.value.length === 0
    ? t("tools.custom.functions.err")
    : "",
);
const languagesError = computed(() =>
  showErrors.value && needsText.value && selectedLanguages.value.length === 0
    ? t("tools.custom.languages.err")
    : "",
);
/** 只展示实际执行的节点：skipped 是 Core 的“本模板不需要”，不应出现在创建页进度里。 */
const runNodes = computed(() =>
  (currentRun.value?.nodes ?? []).filter((node) => node.status !== "skipped"),
);
const { byNode: runTasksByNode, waiting: runWaiting } = useRunNodeTasks(
  () => currentRun.value?.id,
  () => runBusy.value,
);
const runNodeRows = computed(() =>
  runNodes.value.map((node) => {
    const task = runTasksByNode.value.get(node.key);
    const waiting = node.status === "running" && task?.status === "WAITING_RESOURCE";
    return {
      key: node.key,
      label: nodeLabel(node.key),
      status: node.status,
      waiting,
      progress: waiting ? 0 : node.progress,
    };
  }),
);
const runPhaseLabel = computed(() => t(`tools.flow.${runPhase.value}`));
const templateNoticeText = computed(() =>
  templateNotice.value ? t(TEMPLATE_NOTICE_KEYS[templateNotice.value]) : "",
);

function resolveFilePath(file: File): string {
  if (desktopApi) {
    return desktopApi.getPathForFile(file).trim();
  }
  const legacy = (file as File & { path?: string }).path;
  return (legacy ?? file.name).trim();
}

function nodeLabel(key: PipelineNodeKey): string {
  return t(NODE_LABEL_KEYS[key]);
}

function languageLabel(code: string): string {
  const language = TARGET_LANGUAGES.find((item) => item.code === code);
  return language ? targetLanguageLabel(language, getLocale()) : code;
}

function functionLabel(value: CustomPipelineFeature): string {
  const feature = CUSTOM_FEATURES.find((item) => item.value === value);
  return feature ? t(feature.labelKey) : value;
}

function functionSummary(values: CustomPipelineFeature[]): string {
  return values.map(functionLabel).join(" · ");
}

function isCustomFeature(value: unknown): value is CustomPipelineFeature {
  return typeof value === "string" && CUSTOM_FEATURES.some((item) => item.value === value);
}

function resolvedSourceLanguage(): string {
  if (needsText.value) return sourceLanguage.value;
  return eraseSelected.value ? selectedOCR.value : "und";
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

async function onOriginalSubtitle(file: File | undefined): Promise<void> {
  if (!file) return;
  originalSubtitle.value = { file, name: file.name, sizeBytes: file.size };
  // 与网页端一致：按字幕内容识别源语言；文件名只作为内容无法判断时的兜底。
  const detected = await detectFileLanguage(file);
  if (detected) setSourceLanguage(detected);
}

async function onTranslatedSubtitles(files: File[]): Promise<void> {
  for (const file of files) {
    if (translatedSubtitles.value.some((item) => item.name === file.name && item.sizeBytes === file.size)) {
      continue;
    }
    const item: SubtitleFile = { file, name: file.name, sizeBytes: file.size, language: "" };
    translatedSubtitles.value.push(item);
    // 按字幕内容识别每个译文字幕的语言；识别不到或与源语言重复时保留手动选择。
    const detected = await detectFileLanguage(file);
    if (!detected || detected === sourceLanguage.value) continue;
    const duplicate = translatedSubtitles.value.some(
      (other) => other !== item && other.language === detected,
    );
    if (duplicate) continue;
    item.language = detected;
    if (!selectedLanguages.value.includes(detected)) selectedLanguages.value.push(detected);
  }
}

function removeTranslated(index: number): void {
  translatedSubtitles.value.splice(index, 1);
  subtitleLanguageError.value = "";
}

function setTranslatedLanguage(index: number, code: string): void {
  const duplicate = translatedSubtitles.value.some(
    (item, itemIndex) => itemIndex !== index && item.language === code,
  );
  if (duplicate) {
    subtitleLanguageError.value = t("tools.custom.subtitle.languageDuplicate", {
      language: languageLabel(code),
    });
    return;
  }
  subtitleLanguageError.value = "";
  translatedSubtitles.value[index].language = code;
  if (code && !selectedLanguages.value.includes(code)) {
    selectedLanguages.value = TARGET_LANGUAGES.map((item) => item.code).filter(
      (item) => item === code || selectedLanguages.value.includes(item),
    );
  }
}

function setSourceLanguage(code: string): void {
  sourceLanguage.value = code;
  selectedLanguages.value = selectedLanguages.value.filter((item) => item !== code);
  for (const file of translatedSubtitles.value) {
    if (file.language === code) file.language = "";
  }
}

/**
 * 功能依赖（与网页端一致）：克隆配音与混合成片都需要字幕翻译产出译文，
 * Core 也会为它们自动补 06 节点；UI 必须把依赖同步勾选，避免“看起来没选但实际会跑”。
 */
const FEATURE_DEPENDENCIES: Partial<
  Record<CustomPipelineFeature, CustomPipelineFeature[]>
> = {
  clone: ["translate"],
  mix: ["translate"],
};
/** 反向依赖：关闭被依赖项时一并关闭依赖它的功能，保证组合始终可执行。 */
const FEATURE_DEPENDENTS: Partial<
  Record<CustomPipelineFeature, CustomPipelineFeature[]>
> = {
  translate: ["clone", "mix"],
};

function toggleFunction(value: CustomPipelineFeature, checked: boolean): void {
  // 未准入功能不可勾选；已选功能变为未准入时保留选择并由创建校验提示。
  if (checked && !featureAvailable(value)) return;
  // 被依赖项在依赖方存续期间不可取消；依赖项本身仍可关闭（不会带走用户的主选择）。
  if (!checked && value === "translate" && translateLocked.value) return;
  const next = new Set(selectedFunctions.value);
  if (checked) {
    next.add(value);
    for (const dependency of FEATURE_DEPENDENCIES[value] ?? []) next.add(dependency);
  } else {
    next.delete(value);
  }
  selectedFunctions.value = CUSTOM_FEATURES.map((item) => item.value).filter((item) =>
    next.has(item),
  );
}

function toggleLanguage(code: string, checked: boolean): void {
  if (code === sourceLanguage.value) return;
  const next = new Set(selectedLanguages.value);
  if (checked) next.add(code);
  else next.delete(code);
  selectedLanguages.value = TARGET_LANGUAGES.map((item) => item.code).filter((item) =>
    next.has(item),
  );
}

function onToggleLanguage(code: string): void {
  toggleLanguage(code, !selectedLanguages.value.includes(code));
}

function selectAllLanguages(): void {
  selectedLanguages.value = TARGET_LANGUAGES.map((language) => language.code);
}

function clearLanguages(): void {
  selectedLanguages.value = [];
}

function templateFromState(name: string, existing?: CustomPipelineTemplate): CustomPipelineTemplate {
  const now = new Date().toISOString();
  return {
    id: existing?.id ?? `tpl-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    name,
    features: [...selectedFunctions.value],
    languages: needsText.value ? [...selectedLanguages.value] : [],
    sourceLanguage: resolvedSourceLanguage(),
    eraseMode: eraseSelected.value ? eraseScope.value : undefined,
    ocrLanguage: eraseSelected.value ? selectedOCR.value : undefined,
    validateTimestamps: validateTimestamps.value,
    adjustDuration: adjustDuration.value,
    createdAt: existing?.createdAt ?? now,
    updatedAt: now,
  };
}

async function loadTemplates(): Promise<void> {
  try {
    const list = await api.listCustomPipelineTemplates();
    templates.value = list.items;
  } catch {
    templates.value = [];
    templateNotice.value = "loadFailed";
  }
}

async function persistTemplates(items: CustomPipelineTemplate[]): Promise<boolean> {
  templateBusy.value = true;
  try {
    const saved = await api.saveCustomPipelineTemplates(items);
    templates.value = saved.items;
    return true;
  } catch (err) {
    templateNotice.value = "saveFailed";
    templateError(err);
    return false;
  } finally {
    templateBusy.value = false;
  }
}

function templateError(err: unknown): void {
  resultError.value = errorText(err);
}

async function saveTemplate(): Promise<void> {
  const name = templateName.value.trim();
  if (!name) {
    templateNotice.value = "error";
    return;
  }
  if (selectedFunctions.value.length === 0) {
    templateNotice.value = "error";
    return;
  }
  if (Array.from(name).length > 30) {
    templateNotice.value = "nameTooLong";
    return;
  }
  if (needsText.value && selectedLanguages.value.length === 0) {
    templateNotice.value = "languagesRequired";
    return;
  }
  const existing = templates.value.find(
    (item) => item.name.toLowerCase() === name.toLowerCase(),
  );
  if (!existing && templates.value.length >= TEMPLATE_LIMIT) {
    templateNotice.value = "tooMany";
    return;
  }
  const item = templateFromState(name, existing);
  const next = existing
    ? templates.value.map((template) => (template.id === existing.id ? item : template))
    : [item, ...templates.value];
  if (await persistTemplates(next)) {
    templateName.value = "";
    templateNotice.value = "saved";
    resultError.value = "";
  }
}

function applyTemplate(template: CustomPipelineTemplate): void {
  selectedFunctions.value = template.features.filter(isCustomFeature);
  sourceLanguage.value = template.sourceLanguage || "";
  selectedLanguages.value = template.languages.filter((code) => code !== sourceLanguage.value);
  if (template.eraseMode === "dialog_only" || template.eraseMode === "dialog_and_art") {
    eraseScope.value = template.eraseMode;
  }
  if (
    template.ocrLanguage === "chinese" ||
    template.ocrLanguage === "chinese_traditional" ||
    template.ocrLanguage === "english" ||
    template.ocrLanguage === "japanese"
  ) {
    selectedOCR.value = template.ocrLanguage;
  }
  validateTimestamps.value = template.validateTimestamps;
  adjustDuration.value = template.adjustDuration;
  templateNotice.value = "applied";
}

async function deleteTemplate(id: string): Promise<void> {
  const next = templates.value.filter((template) => template.id !== id);
  if (await persistTemplates(next)) {
    templateNotice.value = "deleted";
  }
}

function validate(): boolean {
  showErrors.value = true;
  resultError.value = "";
  saveMessage.value = null;
  saveError.value = null;
  subtitleLanguageError.value = "";
  if (hasBlockedSelection.value) {
    resultError.value = t("access.featureUnavailable", { features: blockedFeatureNames.value });
    return false;
  }
  // 旧模板记录的 true 不能被静默改成 false 提交；必须由用户明确取消后创建。
  if (adjustDuration.value) {
    resultError.value = t("tools.custom.opt.adjustBlocked");
    return false;
  }
  if (!projectName.value.trim() || !mediaFile.value || selectedFunctions.value.length === 0) {
    return false;
  }
  if (needsText.value && selectedLanguages.value.length === 0) return false;
  if (translatedSubtitles.value.some((item) => !item.language)) {
    subtitleLanguageError.value = t("tools.custom.subtitle.languageRequired");
    return false;
  }
  const outsideTargets = translatedSubtitles.value.find(
    (item) => item.language && !selectedLanguages.value.includes(item.language),
  );
  if (outsideTargets) {
    subtitleLanguageError.value = t("tools.custom.subtitle.languageNotTarget", {
      language: languageLabel(outsideTargets.language),
    });
    return false;
  }
  const filePath = resolveFilePath(mediaFile.value);
  if (!filePath) {
    mediaPathError.value = t("tools.custom.media.pathUnavailable");
    return false;
  }
  showErrors.value = false;
  return true;
}

async function importSubtitle(projectId: string, file: File, language: string, type: "transcription" | "translation"): Promise<string> {
  const path = resolveFilePath(file);
  if (!path) throw new Error(t("tools.custom.subtitle.pathUnavailable"));
  const imported = await api.importAssets({ projectId, paths: [path], copyMode: false });
  const assetId = imported.assetIds[0];
  if (!assetId) throw new Error(t("tools.flow.importEmpty"));
  const track = await api.importSubtitleTrack({ projectId, assetId, language, type });
  return track.id;
}

async function create(): Promise<void> {
  if (!validate()) return;
  const features = [...selectedFunctions.value];
  const languageText = needsText.value;
  const erase = eraseSelected.value;
  const clone = cloneSelected.value;
  resultProject.value = null;
  separationAssets.value = [];
  eraseAssets.value = [];
  mixAssets.value = [];
  translatedTracks.value = [];
  candidateRows.value = [];
  pendingReviews.value = [];

  const result = await startRun({
    projectName: projectName.value.trim(),
    filePath: resolveFilePath(mediaFile.value as File),
    payload: {
      template: "custom_pipeline",
      features,
      sourceLanguage: resolvedSourceLanguage() || undefined,
      targetLanguages: languageText ? [...selectedLanguages.value] : [],
      eraseMode: erase ? eraseScope.value : undefined,
      extract: erase ? { mode: "ocr", ocrLanguage: selectedOCR.value, ocrQuality: ocrQuality.value } : undefined,
      validateTimestamps: validateTimestamps.value,
      adjustDuration: adjustDuration.value,
      subtitleStyle:
        selectedSet.value.has("mix") && Object.keys(subtitleStyle.value).length > 0
          ? subtitleStyle.value
          : undefined,
    },
    afterImport: async ({ projectId }) => {
      let reuseSubtitleTrackId: string | undefined;
      if (languageText && originalSubtitle.value) {
        reuseSubtitleTrackId = await importSubtitle(
          projectId,
          originalSubtitle.value.file,
          sourceLanguage.value || "und",
          "transcription",
        );
      }
      if (languageText) {
        for (const item of translatedSubtitles.value) {
          await importSubtitle(projectId, item.file, item.language, "translation");
        }
      }
      return reuseSubtitleTrackId ? { reuseSubtitleTrackId } : undefined;
    },
    waitForReview: async ({ projectId }) => {
      const gates = await api.listReviews(projectId, currentRun.value?.id);
      pendingReviews.value = gates.filter((gate) => gate.status === "pending");
      if (pendingReviews.value.length === 0) return;
      await new Promise<void>((resolve) => {
        reviewResolver = resolve;
      });
    },
  });
  if (!result) return;
  rememberTargetLanguages("custom", selectedLanguages.value);
  resultProject.value = result.project;
  await loadResults(result.project, result.run);
}

async function loadResults(project: Project, run: WorkflowRun): Promise<void> {
  resultError.value = "";
  const features = selectedFunctions.value;
  const [separation, erase, mix] = await Promise.all([
    features.includes("separation") ? loadAllNodeArtifacts(run.id, project.id, "03_separate") : Promise.resolve([]),
    features.includes("erase") ? loadAllNodeArtifacts(run.id, project.id, "05_erase") : Promise.resolve([]),
    features.includes("mix") ? loadAllNodeArtifacts(run.id, project.id, "08_mix") : Promise.resolve([]),
  ]);
  separationAssets.value = separation;
  eraseAssets.value = erase;
  mixAssets.value = mix;

  const tracks: TrackResult[] = [];
  if (needsText.value) {
    for (const language of selectedLanguages.value) {
      const track = project.tracks.find(
        (item) => item.type === "translation" && item.language === language,
      );
      const revision = track?.revisions.find((item) => item.id === track.currentRevisionId);
      if (track && revision) {
        tracks.push({ language, track, revision, lineCount: revision.segments.length });
      }
    }
  }
  translatedTracks.value = tracks;

  const rows: CandidateRow[] = [];
  if (cloneSelected.value) {
    for (const item of tracks) {
      for (const segment of item.revision.segments) {
        const candidates = await api.listCandidates(segment.id, item.language);
        const candidate = candidates.find((option) => option.isRecommended) ?? candidates[0];
        if (!candidate) continue;
        rows.push({
          key: `${item.language}:${segment.id}`,
          language: item.language,
          segmentId: segment.id,
          text: segment.text,
          candidate,
          alternatives: Math.max(candidates.length - 1, 0),
        });
      }
    }
  }
  candidateRows.value = rows;

  if (features.includes("separation") && separation.length === 0) {
    resultError.value = t("tools.custom.result.separationMissing");
  } else if (features.includes("erase") && erase.length === 0) {
    resultError.value = t("tools.custom.result.eraseMissing");
  } else if (features.includes("mix") && mix.length === 0) {
    resultError.value = t("tools.custom.result.mixMissing");
  } else if (needsText.value && tracks.length < selectedLanguages.value.length) {
    resultError.value = t("tools.custom.result.translationMissing");
  } else if (cloneSelected.value && rows.length === 0) {
    resultError.value = t("tools.custom.result.candidatesMissing");
  }
}

async function saveCandidateAudio(row: CandidateRow): Promise<void> {
  saveBusy.value = row.candidate.audioAssetId;
  saveError.value = null;
  saveMessage.value = null;
  try {
    const saved = await api.saveAsset({
      assetId: row.candidate.audioAssetId,
      suggestedName: `${row.language}-${row.segmentId}.wav`,
      title: t("tools.custom.save.candidateTitle"),
    });
    if (!saved) return;
    saveMessage.value = messageNotice("tools.custom.save.saved", { path: saved.path });
  } catch {
    saveError.value = messageNotice("tools.custom.save.failed");
  } finally {
    saveBusy.value = "";
  }
}

async function saveAsset(asset: Asset): Promise<void> {
  saveBusy.value = asset.id;
  saveError.value = null;
  saveMessage.value = null;
  try {
    const saved = await api.saveAsset({
      assetId: asset.id,
      suggestedName: asset.name,
      title: t("tools.custom.save.dialogTitle"),
    });
    if (!saved) return;
    saveMessage.value = messageNotice("tools.custom.save.saved", { path: saved.path });
  } catch {
    saveError.value = messageNotice("tools.custom.save.failed");
  } finally {
    saveBusy.value = "";
  }
}

function exportState(language: string): ExportState {
  return exporting.value[language] ?? { busy: false };
}

/** 渲染期解析导出错误；切换语言只重渲染，不重发导出。 */
function exportErrorText(language: string): string {
  return renderNotice(exportState(language).error);
}

function setExportState(language: string, patch: Partial<ExportState>): void {
  exporting.value = {
    ...exporting.value,
    [language]: { ...exportState(language), ...patch },
  };
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
    setExportState(item.language, { error: messageNotice("tools.common.export.dirUnavailable") });
    return;
  }
  if (!exportDir.value.trim() || !namingTemplate.value.trim()) {
    setExportState(item.language, { error: messageNotice("tools.custom.export.empty") });
    return;
  }
  setExportState(item.language, { busy: true, error: undefined, record: undefined });
  try {
    const created = await api.export({
      projectId: project.id,
      kind: "subtitle",
      format: "srt",
      language: item.language,
      inputRevisionId: item.revision.id,
      outputDir: exportDir.value.trim(),
      namingTemplate: namingTemplate.value.trim(),
    });
    setExportState(item.language, { record: created });
    if (TERMINAL_EXPORT.has(created.status)) {
      if (created.status !== "SUCCEEDED") {
        setExportState(item.language, {
          error: messageNotice("tools.custom.export.failed"),
        });
      }
      return;
    }
    const final = await pollExport(project.id, created.id);
    if (!final) {
      setExportState(item.language, {
        error: messageNotice("tools.custom.export.timeout"),
      });
      return;
    }
    setExportState(item.language, { record: final });
    if (final.status !== "SUCCEEDED") {
      setExportState(item.language, {
        error: messageNotice("tools.custom.export.failed"),
      });
    }
  } catch (err) {
    setExportState(item.language, {
      error: fallbackErrorNotice(err, "tools.custom.export.failed"),
    });
  } finally {
    setExportState(item.language, { busy: false });
  }
}

async function approveGate(gate: ReviewGate): Promise<void> {
  reviewBusy.value = gate.id;
  try {
    await api.approveReview({
      reviewId: gate.id,
      revisionDigest: gate.revisionDigest,
      type: gate.type,
    });
    pendingReviews.value = pendingReviews.value.filter((item) => item.id !== gate.id);
    if (pendingReviews.value.length === 0) {
      const resolve = reviewResolver;
      reviewResolver = null;
      resolve?.();
    }
  } catch (err) {
    resultError.value = errorText(err);
  } finally {
    reviewBusy.value = "";
  }
}

onMounted(() => {
  if (caps.slots.length === 0) void caps.load().catch(() => undefined);
  void resolveOutputDir().catch(() => undefined);
});
</script>

<template>
  <WorkbenchPage
    scaffold
    icon="link"
    :subtitle="t('tools.custom.intro')"
    :title="t('tools.custom.title')"
    :primary-label="primaryLabel"
    :primary-icon="canOpenProject ? 'folder-open' : 'plus'"
    :primary-loading="runBusy"
    primary-shortcut
    :primary-disabled="runBusy || hasBlockedSelection || noAvailableFeatures || adjustDuration || (!hasProject && createBlocked)"
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
        <CapabilityAccessNotice access-key="tool-custom" />

        <YmPanel :title="t('tools.custom.step.project')">
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
              :label="t('tools.custom.media.label')"
              :hint="t('tools.custom.media.hint')"
              :error="mediaError || mediaPathError"
              required
            >
              <YmFileRow
                v-if="media"
                :name="media.name"
                :size="formatBytes(media.sizeBytes)"
                :icon="media.name.toLowerCase().endsWith('.mp4') ? 'film' : 'wave'"
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
                :title="t('tools.custom.media.upload')"
                :hint="t('tools.custom.media.types')"
                accept="video/*,audio/*,.mp4,.mov,.mkv,.m4v,.mp3,.wav,.aac,.flac,.m4a"
                @files="onMedia($event[0])"
              />
            </YmField>
          </div>
        </YmPanel>

        <CreationNotice
          v-if="creationNoticeItems.length"
          :items="creationNoticeItems"
        />

        <YmPanel :title="t('tools.custom.step.functions')">
          <YmField
            :label="t('tools.custom.functions.label')"
            :error="functionsError"
            required
          >
            <div class="feature-list">
              <div
                v-for="feature in CUSTOM_FEATURES"
                :key="feature.value"
                class="feature-item"
                :data-access="featureAccess[feature.value].level"
              >
                <YmCheckbox
                  :model-value="selectedFunctions.includes(feature.value)"
                  :disabled="
                    (featureAccess[feature.value].level === 'hidden' &&
                      !selectedFunctions.includes(feature.value)) ||
                    (feature.value === 'translate' && translateLocked)
                  "
                  :label="t(feature.labelKey)"
                  @update:model-value="(checked: boolean) => toggleFunction(feature.value, checked)"
                />
                <p class="feature-item__desc">{{ t(feature.descKey) }}</p>
                <p
                  v-if="feature.value === 'translate' && translateLocked"
                  class="feature-item__dependency"
                >
                  {{ t("tools.custom.function.translateDependency") }}
                </p>
                <p
                  v-if="featureAccess[feature.value].level === 'hidden'"
                  class="feature-item__notice"
                >
                  {{ t("access.notAdmitted") }}
                  <template v-if="featureMissingLabels(feature.value)">
                    · {{ featureMissingLabels(feature.value) }}
                  </template>
                </p>
                <p
                  v-else-if="
                    featureAccess[feature.value].level === 'setup' &&
                    featureSetupLabels(feature.value)
                  "
                  class="feature-item__notice"
                >
                  {{ t("access.setupSlots", { slots: featureSetupLabels(feature.value) }) }}
                </p>
              </div>
            </div>
          </YmField>
          <p v-if="hasBlockedSelection" class="feature-blocked" role="alert">
            {{ t("access.featureUnavailable", { features: blockedFeatureNames }) }}
          </p>
          <p v-if="noAvailableFeatures" class="feature-blocked" role="alert">
            {{ t("access.customUnavailable") }}
          </p>
        </YmPanel>

        <YmPanel :title="t('tools.custom.step.languages')">
          <div class="panel-stack">
            <YmField
              v-if="needsText"
              :label="t('tools.custom.languages.label')"
              :hint="t('tools.custom.languages.hint')"
              :error="languagesError"
              required
            >
              <LanguageMultiSelect
                :model-value="selectedLanguages"
                :options="targetOptions"
                @toggle="onToggleLanguage"
                @select-all="selectAllLanguages"
                @clear="clearLanguages"
              />
            </YmField>

            <template v-if="eraseSelected">
              <p class="ocr-hint">{{ t("tools.custom.ocr.hint") }}</p>
              <OcrQualityPicker v-model="ocrQuality" />
              <YmField :label="t('tools.custom.scope.label')">
                <YmSelect v-model="eraseScope" :options="scopeOptions" />
              </YmField>
            </template>

            <p v-if="!needsText && !eraseSelected" class="inline-hint">
              {{ t("tools.custom.language.noneHint") }}
            </p>
          </div>
        </YmPanel>

        <YmPanel v-if="needsText" :title="t('tools.custom.step.subtitles')">
          <div class="panel-stack">
            <p v-if="needsText && sourceLanguage" class="inline-hint">
              {{ t("voiceClone.sub.originalDetected", { language: languageLabel(sourceLanguage) }) }}
            </p>

            <YmField
              :label="t('tools.custom.subtitle.original')"
              :hint="eraseSelected ? t('tools.custom.subtitle.originalEraseHint') : t('tools.custom.subtitle.originalHint')"
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
                    @click="originalSubtitle = null"
                  />
                </template>
              </YmFileRow>
              <YmDropzone
                v-else
                :title="t('tools.custom.subtitle.originalUpload')"
                accept=".srt"
                @files="onOriginalSubtitle($event[0])"
              />
            </YmField>

            <YmField
              :label="t('tools.custom.subtitle.translated')"
              :hint="t('tools.custom.subtitle.translatedHint')"
              :error="subtitleLanguageError"
            >
              <YmDropzone
                multiple
                :title="t('tools.custom.subtitle.translatedUpload')"
                accept=".srt"
                @files="onTranslatedSubtitles"
              />
              <ul v-if="translatedSubtitles.length" class="file-list">
                <li v-for="(file, index) in translatedSubtitles" :key="`${file.name}:${file.sizeBytes}`">
                  <YmFileRow
                    :name="file.name"
                    :size="formatBytes(file.sizeBytes)"
                    icon="subtitles"
                  >
                    <template #between>
                      <div class="file-row__lang">
                        <YmSelect
                          :model-value="file.language"
                          :options="targetSelectOptions"
                          :placeholder="t('tools.custom.subtitle.chooseLanguage')"
                          @update:model-value="(value: string) => setTranslatedLanguage(index, value)"
                        />
                      </div>
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
              <p class="inline-hint">{{ t("tools.custom.subtitle.autoNote") }}</p>
            </YmField>
          </div>
        </YmPanel>

        <StylePanel
          v-if="selectedSet.has('mix')"
          :model-value="subtitleStyle"
          :enabled="true"
          :show-inherit="false"
          :title="t('tools.custom.style.title')"
          @update:model-value="subtitleStyle = $event"
        />

        <YmPanel :title="t('tools.custom.step.options')">
          <div class="panel-stack">
            <div class="switch-row">
              <div class="switch-row__text">
                <span class="switch-row__title">{{ t("tools.custom.opt.validate") }}</span>
                <span class="switch-row__hint">{{ t("tools.custom.opt.validateHint") }}</span>
              </div>
              <YmSwitch
                v-model="validateTimestamps"
                :aria-label="t('tools.custom.opt.validate')"
              />
            </div>
            <div
              class="switch-row"
              data-testid="adjust-duration-hint"
              :data-status="adjustDuration ? 'legacy-on' : 'unavailable'"
            >
              <div class="switch-row__text">
                <span class="switch-row__title">{{ t("tools.custom.opt.adjust") }}</span>
                <span
                  class="switch-row__hint"
                  :class="{ 'switch-row__hint--warning': adjustDuration }"
                >
                  {{
                    adjustDuration
                      ? t("tools.custom.opt.adjustLegacyHint")
                      : t("tools.custom.opt.adjustHint")
                  }}
                </span>
              </div>
              <YmSwitch
                v-model="adjustDuration"
                :disabled="!adjustDuration"
                :aria-label="t('tools.custom.opt.adjust')"
              />
            </div>
            <p
              v-if="adjustDuration"
              class="feature-blocked"
              role="alert"
              data-testid="adjust-duration-blocked"
            >
              {{ t("tools.custom.opt.adjustBlocked") }}
            </p>
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
          </div>
          <p class="result-head__desc">{{ t("tools.custom.result.desc") }}</p>
          <p v-if="runError" class="flow-error">{{ errorText(runError) }}</p>
          <p v-if="runPartial" class="flow-warning">{{ t("tools.flow.partial") }}</p>
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

        <YmCard v-if="currentRun">
          <template #header>
            <span>{{ t("tools.custom.run.nodes") }}</span>
          </template>
          <ul class="node-list">
            <li v-for="row in runNodeRows" :key="row.key" class="node-row">
              <span class="node-row__label">{{ row.label }}</span>
              <div class="node-row__bar">
                <span :style="{ width: `${row.progress}%` }" />
              </div>
              <YmBadge :tone="row.waiting ? 'neutral' : nodeStatusTone(row.status)">
                {{ row.waiting ? t("state.waitingResource") : t(nodeStatusLabelKey(row.status)) }}
              </YmBadge>
            </li>
          </ul>
          <RunOptionsSummary :params="currentRun?.params" />
        </YmCard>

        <YmCard v-if="pendingReviews.length">
          <template #header>
            <span>{{ t("tools.custom.review.title") }}</span>
          </template>
          <ul class="review-list">
            <li v-for="gate in pendingReviews" :key="gate.id" class="review-item">
              <div class="review-item__text">
                <span class="review-item__title">
                  {{ gate.type === "delivery" ? t("tools.custom.review.delivery") : t("tools.custom.review.translation") }}
                </span>
                <span v-if="gate.language" class="review-item__hint">{{ languageLabel(gate.language) }}</span>
              </div>
              <YmButton
                size="sm"
                :disabled="reviewBusy === gate.id"
                @click="approveGate(gate)"
              >
                {{ t("tools.custom.review.approve") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <p v-if="resultError" class="flow-error">{{ resultError }}</p>
        <p v-if="saveMessageText" class="flow-success"><bdi>{{ saveMessageText }}</bdi></p>
        <p v-if="saveErrorText" class="flow-error"><bdi>{{ saveErrorText }}</bdi></p>

        <YmCard v-if="separationAssets.length">
          <template #header><span>{{ t("tools.custom.result.separation") }}</span></template>
          <ul class="asset-list">
            <li v-for="asset in separationAssets" :key="asset.id" class="asset-row">
              <span class="asset-row__name">{{ asset.name }}</span>
              <span class="asset-row__meta">{{ formatBytes(asset.sizeBytes) }}</span>
              <YmButton size="sm" :disabled="saveBusy === asset.id" @click="saveAsset(asset)">
                {{ t("tools.custom.save.action") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <YmCard v-if="eraseAssets.length">
          <template #header><span>{{ t("tools.custom.result.erase") }}</span></template>
          <ul class="asset-list">
            <li v-for="asset in eraseAssets" :key="asset.id" class="asset-row">
              <span class="asset-row__name">{{ asset.name }}</span>
              <span class="asset-row__meta">{{ formatBytes(asset.sizeBytes) }}</span>
              <YmButton size="sm" :disabled="saveBusy === asset.id" @click="saveAsset(asset)">
                {{ t("tools.custom.save.action") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <YmCard v-if="translatedTracks.length">
          <template #header><span>{{ t("tools.custom.result.translation") }}</span></template>
          <div class="panel-stack">
            <div class="export-settings">
              <YmField
                :label="t('tools.custom.export.dir')"
                :hint="t('tools.common.export.defaultDirHint')"
              >
                <YmTextInput v-model="exportDir" disabled />
              </YmField>
              <YmField :label="t('tools.custom.export.naming')">
                <YmTextInput v-model="namingTemplate" />
              </YmField>
            </div>
            <ul class="asset-list">
              <li v-for="item in translatedTracks" :key="item.language" class="asset-row">
                <span class="asset-row__name">{{ languageLabel(item.language) }}</span>
                <span class="asset-row__meta">{{ t("tools.custom.result.lineCount", { count: item.lineCount }) }}</span>
                <YmButton
                  size="sm"
                  :disabled="exportState(item.language).busy"
                  @click="exportSrt(item)"
                >
                  {{ t("tools.custom.export.action") }}
                </YmButton>
                <span v-if="exportState(item.language).error" class="flow-error">
                  <bdi>{{ exportErrorText(item.language) }}</bdi>
                </span>
                <span v-else-if="exportState(item.language).record?.status === 'SUCCEEDED'" class="flow-success">
                  {{ t("tools.custom.export.done") }}
                </span>
              </li>
            </ul>
          </div>
        </YmCard>

        <YmCard v-if="candidateRows.length">
          <template #header><span>{{ t("tools.custom.result.clone") }}</span></template>
          <ul class="asset-list">
            <li v-for="row in candidateRows" :key="row.key" class="asset-row asset-row--stack">
              <div class="candidate-text">
                <span class="candidate-lang">{{ languageLabel(row.language) }}</span>
                <span class="candidate-line">{{ row.text }}</span>
                <span class="candidate-meta">
                  {{ t("tools.custom.result.candidateMeta", { count: row.alternatives }) }}
                </span>
              </div>
              <YmButton size="sm" :disabled="saveBusy === row.candidate.audioAssetId" @click="saveCandidateAudio(row)">
                {{ t("tools.custom.save.action") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <YmCard v-if="mixAssets.length">
          <template #header><span>{{ t("tools.custom.result.mix") }}</span></template>
          <ul class="asset-list">
            <li v-for="asset in mixAssets" :key="asset.id" class="asset-row">
              <span class="asset-row__name">{{ asset.name }}</span>
              <span class="asset-row__meta">{{ formatBytes(asset.sizeBytes) }}</span>
              <YmButton size="sm" :disabled="saveBusy === asset.id" @click="saveAsset(asset)">
                {{ t("tools.custom.save.action") }}
              </YmButton>
            </li>
          </ul>
        </YmCard>

        <div v-if="!currentRun && !runBusy" class="results-empty">
          <YmEmpty
            :title="t('tools.common.result.emptyTitle')"
            :description="t('tools.custom.result.emptyDesc')"
            icon="layers"
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
.file-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: var(--space-2) 0 0;
  padding: 0;
  list-style: none;
}
.feature-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.feature-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.feature-item__dependency {
  padding-left: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-primary-ink);
}

.feature-item__desc {
  padding-left: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
.feature-item__notice {
  padding-left: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-warning-strong);
  line-height: var(--line-height-normal);
}
.feature-blocked {
  margin-top: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
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
.switch-row__hint--warning {
  color: var(--color-warning-strong);
}
.template-save {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.template-notice {
  font-size: var(--font-size-xs);
  color: var(--color-success-strong);
}
.template-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.template-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.template-item__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1;
  min-width: 0;
}
.template-item__name {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.template-item__desc {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.inline-hint {
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


.file-row__lang {
  flex: none;
  width: 170px;
}
.closure-hint {
  margin-top: var(--space-2);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
  line-height: var(--line-height-normal);
}
.node-list,
.review-list,
.asset-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.node-row,
.review-item,
.asset-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.node-row__label {
  flex: none;
  width: 96px;
  font-size: var(--font-size-sm);
}
.node-row__bar {
  flex: 1;
  height: 6px;
  overflow: hidden;
  background: var(--color-surface-2);
  border-radius: 999px;
}
.node-row__bar span {
  display: block;
  height: 100%;
  background: var(--color-primary);
  transition: width var(--motion-duration-base) var(--motion-ease-out);
}
.review-item__text {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.review-item__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.review-item__hint,
.asset-row__meta,
.candidate-meta {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.asset-row__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--font-size-sm);
}
.asset-row--stack {
  align-items: flex-start;
}
.candidate-text {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.candidate-lang {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.candidate-line {
  font-size: var(--font-size-sm);
  overflow-wrap: anywhere;
}
.export-settings {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.ocr-hint {
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
.flow-error {
  font-size: var(--font-size-xs);
  color: var(--color-danger);
}
.flow-warning {
  font-size: var(--font-size-xs);
  color: var(--color-warning-strong);
}
.flow-success {
  font-size: var(--font-size-xs);
  color: var(--color-success-strong);
}
.flow-issues {
  margin: 0;
  padding-left: var(--space-4);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
</style>
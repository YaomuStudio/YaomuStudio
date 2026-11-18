<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { formatBytes, t } from "@/i18n";
import { errorNotice, messageNotice, noticeKey, renderNotice, type NoticeMessageParam, type SafeNotice } from "@/i18n/notice";
import { api } from "@/api";
import {
  CAPABILITY_SLOTS,
  DISPLAY_MODEL_SLOTS,
  HIDDEN_MODEL_SLOTS,
  isModelSlot,
  visibleModelSlots,
  type SlotMeta,
} from "@/domain/capabilities";
import {
  activeInstallationForSlot,
  hasDownloadableRelease,
  installationCoveredSlots,
  latestInstallationForSlot,
  planCoverage,
  siblingCurrentSlots,
} from "@/domain/installations";
import { slotStateLabelKey, slotStateTone } from "@/domain/status";
import type {
  Capability,
  CapabilitySlotKey,
  Installation,
  InstallationPlan,
  ModelSlotKey,
  ParamField,
  SlotState,
} from "@/domain/types";
import InstallPlanDialog from "@/components/InstallPlanDialog.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmField from "@/components/ui/YmField.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
import YmSelect from "@/components/ui/YmSelect.vue";
import YmTextInput from "@/components/ui/YmTextInput.vue";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useInstallationsStore } from "@/stores/installations";

interface SlotRow {
  meta: SlotMeta;
  cap: Capability | undefined;
}

type ParamValue = string | boolean;

/** 运行级业务配置由创建流程冻结到 Run.params，不属于模型安装/引擎参数。 */
const RUN_LEVEL_PARAMS = new Set([
  "erase_mode",
  "ocr_language",
  "extract_mode",
  "validate_timestamps",
  "adjust_duration",
  "bilingual",
  "subtitle_style",
]);

function visibleParams(cap: Capability): ParamField[] {
  return (cap.params ?? []).filter((field) => !RUN_LEVEL_PARAMS.has(field.key));
}

const STATE_HINTS: Record<SlotState, string> = {
  UNKNOWN: "models.stateHint.unknown",
  UNSUPPORTED: "models.stateHint.unsupported",
  UNVERIFIED: "models.stateHint.unverified",
  NEEDS_SETUP: "models.stateHint.needsSetup",
  INSTALLING: "models.stateHint.installing",
  VERIFYING: "models.stateHint.verifying",
  READY: "models.stateHint.ready",
  NEEDS_ACTION: "models.stateHint.needsAction",
  RESTART_REQUIRED: "models.stateHint.restartRequired",
  FAILED: "models.stateHint.failed",
  DEGRADED: "models.stateHint.degraded",
};

const caps = useCapabilitiesStore();
const installs = useInstallationsStore();
const route = useRoute();
const loading = ref(false);
const busySlot = ref<CapabilitySlotKey | null>(null);
const paramSaving = ref<CapabilitySlotKey | null>(null);
const engineBusy = ref<string | null>(null);
const actionError = ref<SafeNotice | null>(null);
const actionErrorText = computed(() => renderNotice(actionError.value));
/** 槽位内动作的即时反馈：就地显示在触发的卡片里，不放到页面顶部。 */
interface SlotFeedback {
  error: SafeNotice | null;
  notice: SafeNotice | null;
}
const slotFeedback = ref<Partial<Record<CapabilitySlotKey, SlotFeedback>>>({});
const slotFeedbackText = computed<
  Partial<Record<CapabilitySlotKey, { error: string; notice: string }>>
>(() => {
  const out: Partial<Record<CapabilitySlotKey, { error: string; notice: string }>> = {};
  for (const slot of Object.keys(slotFeedback.value) as CapabilitySlotKey[]) {
    const feedback = slotFeedback.value[slot];
    out[slot] = {
      error: renderNotice(feedback?.error ?? null),
      notice: renderNotice(feedback?.notice ?? null),
    };
  }
  return out;
});

function clearSlotFeedback(slot: CapabilitySlotKey): void {
  if (!slotFeedback.value[slot]) return;
  const next = { ...slotFeedback.value };
  delete next[slot];
  slotFeedback.value = next;
}

function setSlotError(slot: CapabilitySlotKey, err: unknown): void {
  slotFeedback.value = {
    ...slotFeedback.value,
    [slot]: { error: errorNotice(err, "models.error"), notice: null },
  };
}

function setSlotNotice(slot: CapabilitySlotKey, value: SafeNotice): void {
  slotFeedback.value = {
    ...slotFeedback.value,
    [slot]: { error: null, notice: value },
  };
}
const deleteTarget = ref<Capability | null>(null);
const planTarget = ref<InstallationPlan | null>(null);
const paramValues = reactive<Record<string, Record<string, ParamValue>>>({});

function metaFor(cap: Capability): SlotMeta {
  const known = CAPABILITY_SLOTS.find((meta) => meta.key === cap.slot);
  if (known) return known;
  return {
    key: cap.slot,
    nameKey: cap.nameKey,
    isModelSlot: cap.slot !== "ocr" && cap.slot !== "ffmpeg",
  };
}

const SLOT_ORDER = new Map(CAPABILITY_SLOTS.map((meta, index) => [meta.key, index]));

const rows = computed<SlotRow[]>(() =>
  [...caps.slots]
    .sort(
      (left, right) =>
        (SLOT_ORDER.get(left.slot) ?? Number.MAX_SAFE_INTEGER) -
        (SLOT_ORDER.get(right.slot) ?? Number.MAX_SAFE_INTEGER),
    )
    .map((cap) => ({ meta: metaFor(cap), cap })),
);
/** 未准入/未验证或 Core 未返回记录：模型页只显示“未配置”，不提供安装操作。 */
function isUnconfigured(row: SlotRow): boolean {
  if (!row.cap) return true;
  return row.cap.state === "UNSUPPORTED" || row.cap.state === "UNVERIFIED";
}

/** 这些状态下会提供下载入口；没有准入发布时按钮改为禁用。 */
const DOWNLOAD_STATES: ReadonlySet<SlotState> = new Set<SlotState>([
  "NEEDS_SETUP",
  "FAILED",
  "DEGRADED",
]);

/** 没有准入发布时说明“暂未提供版本”，不沿用“尚未安装”的误导提示。 */
function slotHint(row: SlotRow): string {
  if (!row.cap) return "";
  if (DOWNLOAD_STATES.has(row.cap.state) && !hasDownloadableRelease(row.cap)) {
    return t("installation.release_not_configured");
  }
  return t(STATE_HINTS[row.cap.state]);
}
const modelRows = computed(() =>
  DISPLAY_MODEL_SLOTS.map((slot) => rows.value.find((row) => row.cap?.slot === slot)).filter(
    (row): row is SlotRow => row !== undefined,
  ),
);
const auxRows = computed(() =>
  rows.value.filter((row) => {
    const slot = row.cap?.slot as CapabilitySlotKey | undefined;
    return (
      slot !== undefined &&
      !DISPLAY_MODEL_SLOTS.includes(slot) &&
      !HIDDEN_MODEL_SLOTS.includes(slot)
    );
  }),
);

/**
 * 从恢复入口显式带来的未就绪槽位；没有上下文时不推断“推荐先装哪个”。
 * 只做展示强调，不改变安装、删除或授权行为。
 */
const highlightedSlots = computed<Set<CapabilitySlotKey>>(() => {
  const raw = route.query.slots;
  const values = (Array.isArray(raw) ? raw : raw ? [raw] : []).flatMap((value) =>
    String(value).split(","),
  );
  return new Set(
    values.filter((value): value is CapabilitySlotKey =>
      CAPABILITY_SLOTS.some((meta) => meta.key === value),
    ),
  );
});

/** 单一引擎行：已报告字段照常显示，不再在标题与 meta 中重复。 */
function engineSummary(cap: Capability | undefined): string {
  if (!cap) return "";
  if ((cap.engines?.length ?? 0) > 1) return "";
  const parts: string[] = [];
  if (cap.engineName) parts.push(cap.engineName);
  if (cap.version) parts.push(`v${cap.version}`);
  return parts.join(" · ");
}

/** 每槽最多一条未知信息聚合成句；已知字段仍单独显示。 */
function hasUnknownDetails(cap: Capability | undefined): boolean {
  if (!cap) return true;
  if (!isModelSlot(cap.slot)) return !cap.engineName && !cap.version;
  return !cap.engineName || !cap.version;
}

function label(key: string): string {
  const value = t(key);
  return value === key ? key : value;
}

function slotNames(slots: CapabilitySlotKey[]): string {
  return visibleModelSlots(slots).map(slotNameKey).map(label).join(", ");
}

/** 同一物理安装按 boundSlots 命中：两个覆盖槽看到同一 ID/进度，不按发起 slot 猜。 */
function activeInstallation(slot: CapabilitySlotKey): Installation | undefined {
  return activeInstallationForSlot(installs.items, slot);
}

/** 活动安装的完整同意覆盖；共享时列出全部槽位，说明取消影响范围。 */
function activeSharedLabel(slot: CapabilitySlotKey): string {
  const inst = activeInstallation(slot);
  if (!inst) return "";
  const covered = installationCoveredSlots(inst);
  return covered.length > 1 ? t("models.sharedProgress", { slots: slotNames(covered) }) : "";
}

/**
 * 最近一条安装记录是失败、但槽位仍按旧版本 READY 时，保留重试入口
 * （失败升级不激活新版本，也不遮蔽既有可用版本）。
 */
function failedUpgrade(slot: CapabilitySlotKey): boolean {
  if (activeInstallation(slot)) return false;
  return latestInstallationForSlot(installs.items, slot)?.phase === "failed";
}

/** 删除只解绑该槽；同一 Done 实体仍被其他槽引用时说明文件会保留。 */
const deleteSharedNames = computed(() => {
  const target = deleteTarget.value;
  return target ? slotNames(siblingCurrentSlots(installs.items, target.slot)) : "";
});

function activeProgress(slot: CapabilitySlotKey): number {
  return activeInstallation(slot)?.progress ?? 0;
}

function activePhaseLabel(slot: CapabilitySlotKey): string {
  const inst = activeInstallation(slot);
  return inst ? t(`models.phase.${inst.phase}`) : "";
}

function ensureParams(cap: Capability): void {
  if (!paramValues[cap.slot]) paramValues[cap.slot] = {};
  const store = paramValues[cap.slot];
  for (const field of cap.params ?? []) {
    if (!(field.key in store)) {
      store[field.key] =
        field.type === "boolean"
          ? Boolean(field.default)
          : field.default == null
            ? ""
            : String(field.default);
    }
  }
}

function paramBool(slot: CapabilitySlotKey, key: string): boolean {
  return Boolean(paramValues[slot]?.[key]);
}

function paramText(slot: CapabilitySlotKey, key: string): string {
  const value = paramValues[slot]?.[key];
  if (value === undefined) return "";
  return typeof value === "string" ? value : String(value);
}

function setParam(slot: CapabilitySlotKey, key: string, value: ParamValue): void {
  if (!paramValues[slot]) paramValues[slot] = {};
  paramValues[slot][key] = value;
}

function fieldOptions(field: ParamField): { value: string; label: string }[] {
  return (field.options ?? []).map((option) => ({
    value: option.value,
    label: label(option.labelKey),
  }));
}

function fieldHint(field: ParamField): string {
  if (field.type !== "number") return "";
  if (field.min == null && field.max == null) return "";
  return t("models.paramRange", {
    min: field.min ?? "—",
    max: field.max ?? "—",
  });
}

/** 把 UI 草稿转成受控参数值（number 必须提交数字，否则 Core 校验失败）。 */
function paramValuesFor(cap: Capability): Record<string, unknown> {
  const values: Record<string, unknown> = {};
  for (const field of visibleParams(cap)) {
    const raw = paramValues[cap.slot]?.[field.key];
    if (raw === undefined) continue;
    if (field.type === "boolean") {
      values[field.key] = Boolean(raw);
    } else if (field.type === "number") {
      const parsed = Number(raw);
      if (Number.isFinite(parsed)) values[field.key] = parsed;
    } else {
      values[field.key] = String(raw);
    }
  }
  return values;
}

/** 读取槽位已保存参数（只对六个模型槽位有配置端点）。 */
async function hydrateEngineConfig(cap: Capability): Promise<void> {
  if (!cap.params?.length || !isModelSlot(cap.slot)) return;
  try {
    const config = await api.getEngineConfig(cap.slot as ModelSlotKey);
    if (!paramValues[cap.slot]) paramValues[cap.slot] = {};
    for (const field of visibleParams(cap)) {
      const value = config.values?.[field.key];
      if (value === undefined) continue;
      paramValues[cap.slot][field.key] =
        field.type === "boolean" ? Boolean(value) : String(value);
    }
  } catch {
    // 读取失败时保留能力目录里的默认值，不阻塞页面。
  }
}

async function selectEngine(cap: Capability, engineId: string): Promise<void> {
  if (!isModelSlot(cap.slot) || engineBusy.value) return;
  const engine = cap.engines?.find((item) => item.id === engineId);
  if (!engine || !engine.installed || engine.state !== "READY" || engine.selected) return;
  engineBusy.value = engineId;
  clearSlotFeedback(cap.slot);
  try {
    await api.setEngineConfig(cap.slot as ModelSlotKey, { engineId });
    await caps.load();
  } catch (err) {
    setSlotError(cap.slot, err);
  } finally {
    engineBusy.value = null;
  }
}

async function saveParams(cap: Capability): Promise<void> {
  if (!isModelSlot(cap.slot)) return;
  paramSaving.value = cap.slot;
  clearSlotFeedback(cap.slot);
  try {
    await api.setEngineConfig(cap.slot as ModelSlotKey, { values: paramValuesFor(cap) });
    setSlotNotice(cap.slot, messageNotice("models.paramsSaved", { name: slotNoticeName(cap.slot) }));
  } catch (err) {
    setSlotError(cap.slot, err);
  } finally {
    paramSaving.value = null;
  }
}

async function refresh(): Promise<void> {
  loading.value = true;
  actionError.value = null;
  try {
    await caps.load();
    await installs.load();
    await Promise.all(caps.slots.map((cap) => hydrateEngineConfig(cap)));
  } catch (err) {
    actionError.value = errorNotice(err, "models.error");
  } finally {
    loading.value = false;
  }
}

async function startInstall(slot: CapabilitySlotKey): Promise<void> {
  // 未支持/未准入的槽位没有可解析的发布，禁用入口且不发无效计划请求。
  // 权重预拉进行中槽位已是 INSTALLING，不能再打开下载确认。
  const state = caps.slotState(slot)?.state;
  if (
    activeInstallation(slot) ||
    state === "INSTALLING" ||
    state === "VERIFYING" ||
    !hasDownloadableRelease(caps.slotState(slot))
  ) {
    return;
  }
  busySlot.value = slot;
  clearSlotFeedback(slot);
  try {
    // 先解析安装计划（绑定下载/权限范围与哈希）；用户确认计划后才执行。
    planTarget.value = await api.planInstallation(slot);
  } catch (err) {
    setSlotError(slot, err);
  } finally {
    busySlot.value = null;
  }
}

async function confirmInstall(): Promise<void> {
  const plan = planTarget.value;
  if (!plan) return;
  const coverage = planCoverage(plan);
  // 覆盖范围异常时不允许提交；对话框已禁用确认，这里再 fail closed 一次。
  if (!coverage.valid) return;
  busySlot.value = plan.slot;
  clearSlotFeedback(plan.slot);
  try {
    const created = await api.install({ planId: plan.planId, planHash: plan.planHash });
    installs.track(created.id, plan.slot);
    planTarget.value = null;
    await refresh();
    // 提示只存安全键/槽位身份：切语言后按当前 locale 重渲染，不缓存已翻译名称。
    if (coverage.slots.length > 1) {
      setSlotNotice(plan.slot, messageNotice("models.installStartedShared"));
    } else {
      setSlotNotice(
        plan.slot,
        messageNotice("models.installStarted", {
          name: slotNoticeName(coverage.slots[0] ?? plan.slot),
        }),
      );
    }
  } catch (err) {
    planTarget.value = null;
    setSlotError(plan.slot, err);
  } finally {
    busySlot.value = null;
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value;
  if (!target) return;
  const slot = target.slot;
  busySlot.value = slot;
  clearSlotFeedback(slot);
  try {
    await api.deleteInstallation(slot);
    deleteTarget.value = null;
    await refresh();
    setSlotNotice(slot, messageNotice("models.deleted", { name: slotNoticeName(slot) }));
  } catch (err) {
    setSlotError(slot, err);
  } finally {
    busySlot.value = null;
  }
}

async function rollbackForSlot(slot: CapabilitySlotKey): Promise<void> {
  busySlot.value = slot;
  clearSlotFeedback(slot);
  try {
    await api.rollbackInstallation(slot);
    await refresh();
    setSlotNotice(
      slot,
      messageNotice("models.rollbackStarted", {
        name: slotNoticeName(slot),
      }),
    );
  } catch (err) {
    setSlotError(slot, err);
  } finally {
    busySlot.value = null;
  }
}

async function cancelForSlot(slot: CapabilitySlotKey): Promise<void> {
  const inst = activeInstallation(slot);
  if (!inst) return;
  const shared = installationCoveredSlots(inst).length > 1;
  busySlot.value = slot;
  clearSlotFeedback(slot);
  try {
    // 取消作用于该物理安装 ID 的全部覆盖槽；写后重读，不盲信旧回执。
    await api.cancelInstallation(inst.id);
    await refresh();
    if (shared) {
      setSlotNotice(slot, messageNotice("models.cancelStartedShared"));
    } else {
      setSlotNotice(slot, messageNotice("models.cancelStarted"));
    }
  } catch (err) {
    setSlotError(slot, err);
  } finally {
    busySlot.value = null;
  }
}

function slotNameKey(slot: CapabilitySlotKey): string {
  return caps.slotState(slot)?.nameKey ?? slot;
}

/**
 * 槽位是提示事件期的不可变身份；展示名在渲染时按当前 locale 解析。
 * 字面量键由 i18n guard 校验注册；`Record<CapabilitySlotKey, ...>` 保证新增槽位时编译报错。
 */
const SLOT_NOTICE_NAME: Record<CapabilitySlotKey, NoticeMessageParam> = {
  asr: noticeKey("slot.asr"),
  translate: noticeKey("slot.translate"),
  tts: noticeKey("slot.tts"),
  "voice-clone": noticeKey("slot.voiceClone"),
  "subtitle-erase": noticeKey("slot.subtitleErase"),
  separation: noticeKey("slot.separation"),
  ocr: noticeKey("slot.ocr"),
  ffmpeg: noticeKey("slot.ffmpeg"),
};

function slotNoticeName(slot: CapabilitySlotKey): NoticeMessageParam {
  return SLOT_NOTICE_NAME[slot];
}

watch(
  () => caps.slots,
  (slots) => {
    for (const cap of slots) ensureParams(cap);
  },
  { immediate: true },
);

onMounted(() => {
  void refresh();
});
</script>

<template>
  <div class="page">
    <header class="page__head">
      <div>
        <h1 class="page__title">{{ t("models.title") }}</h1>
        <p class="page__desc">{{ t("models.desc") }}</p>
      </div>
      <YmButton
        variant="secondary"
        size="sm"
        icon="refresh"
        :loading="loading"
        @click="refresh"
      >
        {{ t("models.refresh") }}
      </YmButton>
    </header>

    <YmNotice v-if="actionErrorText" tone="error">
      <bdi>{{ actionErrorText }}</bdi>
    </YmNotice>

    <YmNotice tone="warning" :title="t('models.budget')">
      <p>{{ t("models.budgetBody") }}</p>
      <p class="notice-note">{{ t("models.budgetUnmeasured") }}</p>
    </YmNotice>

    <div class="section-head">
      <h2 class="section-title">{{ t("models.modelSlots") }}</h2>
    </div>

    <p v-if="!loading && !caps.coreReady" class="muted">{{ t("api.coreNotReady") }}</p>
    <p v-else-if="!loading && modelRows.length === 0" class="muted">{{ t("models.noSlots") }}</p>
    <div v-else class="cards">
      <article
        v-for="row in modelRows"
        :key="row.meta.key"
        class="slot"
        :class="{ 'slot--needed': highlightedSlots.has(row.meta.key) }"
      >
        <header class="slot__head">
          <div class="slot__title">
            <h3>{{ t(row.meta.nameKey) }}</h3>
            <p v-if="engineSummary(row.cap)" class="slot__engine">
              {{ engineSummary(row.cap) }}
            </p>
          </div>
          <YmBadge
            :tone="isUnconfigured(row) ? 'neutral' : row.cap ? slotStateTone(row.cap.state) : 'neutral'"
          >
            {{
              isUnconfigured(row)
                ? t("models.unconfigured")
                : row.cap
                  ? t(slotStateLabelKey(row.cap.state))
                  : t("models.unknown")
            }}
          </YmBadge>
        </header>

        <p v-if="highlightedSlots.has(row.meta.key)" class="slot__needed">
          {{ t("models.neededByEntry") }}
        </p>

        <template v-if="row.cap && !isUnconfigured(row)">
          <p v-if="hasUnknownDetails(row.cap)" class="slot__unknown">
            {{ t("models.infoUnavailable") }}
          </p>

          <div
            v-if="row.meta.isModelSlot && (row.cap.engines?.length ?? 0) > 1"
            class="engines"
          >
            <p class="engines__title">{{ t("models.engine") }}</p>
            <label
              v-for="engine in row.cap.engines"
              :key="engine.id"
              class="engine-option"
              :class="{ 'is-selected': engine.selected, 'is-disabled': !engine.installed || engine.state !== 'READY' }"
            >
              <input
                type="radio"
                class="engine-option__radio"
                :name="`engine-${row.cap.slot}`"
                :checked="engine.selected"
                :disabled="!engine.installed || engine.state !== 'READY' || engineBusy === engine.id"
                @change="selectEngine(row.cap, engine.id)"
              />
              <span class="engine-option__main">
                <span class="engine-option__name">{{ engine.name }}</span>
                <span class="engine-option__meta">
                  {{ engine.version ? `v${engine.version}` : t("models.unknown") }}
                  <template v-if="engine.device"> · {{ engine.device }}</template>
                  <template v-if="engine.source"> · {{ engine.source }}</template>
                  <template v-if="engine.sizeBytes"> · {{ formatBytes(engine.sizeBytes) }}</template>
                </span>
              </span>
              <YmBadge
                :tone="slotStateTone(engine.state)"
                class="engine-option__state"
              >
                {{ t(slotStateLabelKey(engine.state)) }}
              </YmBadge>
            </label>
          </div>

          <div v-if="activeInstallation(row.cap.slot)" class="progress">
            <div class="progress__head">
              <span>{{ t("models.progress") }}</span>
              <span class="mono">{{ Math.round(activeProgress(row.cap.slot)) }}%</span>
            </div>
            <YmProgress :value="activeProgress(row.cap.slot)" tone="info" />
            <p class="progress__phase">{{ activePhaseLabel(row.cap.slot) }}</p>
            <p v-if="activeSharedLabel(row.cap.slot)" class="progress__shared">
              {{ activeSharedLabel(row.cap.slot) }}
            </p>
          </div>
          <div
            v-else-if="row.cap.state === 'INSTALLING' || row.cap.state === 'VERIFYING'"
            class="progress"
          >
            <div class="progress__head">
              <span>{{ t("models.progress") }}</span>
            </div>
            <YmProgress indeterminate tone="info" />
            <p class="progress__phase">{{ t(STATE_HINTS[row.cap.state]) }}</p>
          </div>

          <div v-if="visibleParams(row.cap).length > 0" class="params">
            <p class="params__title">{{ t("models.params") }}</p>
            <div class="params__list">
              <div v-for="field in visibleParams(row.cap)" :key="field.key" class="param">
                <YmCheckbox
                  v-if="field.type === 'boolean'"
                  :model-value="paramBool(row.cap.slot, field.key)"
                  :label="label(field.labelKey)"
                  @update:model-value="setParam(row.cap.slot, field.key, $event)"
                />
                <YmField
                  v-else
                  :label="label(field.labelKey)"
                  :hint="fieldHint(field)"
                >
                  <YmSelect
                    v-if="field.type === 'enum'"
                    :model-value="paramText(row.cap.slot, field.key)"
                    :options="fieldOptions(field)"
                    :aria-label="label(field.labelKey)"
                    @update:model-value="setParam(row.cap.slot, field.key, $event)"
                  />
                  <YmTextInput
                    v-else
                    :model-value="paramText(row.cap.slot, field.key)"
                    :type="field.type === 'number' ? 'number' : 'text'"
                    :aria-label="label(field.labelKey)"
                    @update:model-value="setParam(row.cap.slot, field.key, $event)"
                  />
                </YmField>
              </div>
            </div>
            <div class="params__actions">
              <YmButton
                v-if="row.meta.isModelSlot"
                size="sm"
                variant="secondary"
                icon="check"
                :loading="paramSaving === row.cap.slot"
                @click="saveParams(row.cap)"
              >
                {{ t("models.saveParams") }}
              </YmButton>
              <span v-else class="params__hint">{{ t("models.paramsLocalOnly") }}</span>
            </div>
          </div>

          <footer class="slot__actions">
            <template v-if="activeInstallation(row.cap.slot)">
              <YmButton
                variant="secondary"
                size="sm"
                :loading="busySlot === row.cap.slot"
                :title="activeSharedLabel(row.cap.slot) || undefined"
                @click="cancelForSlot(row.cap.slot)"
              >
                {{ t("models.cancelInstall") }}
              </YmButton>
            </template>
            <template
              v-else-if="
                row.cap.state === 'NEEDS_SETUP' ||
                row.cap.state === 'FAILED' ||
                row.cap.state === 'DEGRADED'
              "
            >
              <YmButton
                variant="primary"
                size="sm"
                icon="download"
                :disabled="!hasDownloadableRelease(row.cap)"
                :loading="busySlot === row.cap.slot"
                @click="startInstall(row.cap.slot)"
              >
                {{ t("models.install") }}
              </YmButton>
            </template>
            <template v-else-if="row.cap.state === 'INSTALLING' || row.cap.state === 'VERIFYING'">
              <span class="muted">{{ t(STATE_HINTS[row.cap.state]) }}</span>
            </template>
            <template
              v-else-if="
                row.cap.state === 'READY' ||
                row.cap.state === 'UNVERIFIED' ||
                row.cap.state === 'NEEDS_ACTION' ||
                row.cap.state === 'RESTART_REQUIRED'
              "
            >
              <YmButton
                v-if="failedUpgrade(row.cap.slot)"
                variant="primary"
                size="sm"
                icon="download"
                :loading="busySlot === row.cap.slot"
                @click="startInstall(row.cap.slot)"
              >
                {{ t("models.retryInstall") }}
              </YmButton>
              <YmButton
                variant="secondary"
                size="sm"
                icon="trash"
                :loading="busySlot === row.cap.slot"
                @click="deleteTarget = row.cap"
              >
                {{ t("models.delete") }}
              </YmButton>
              <YmButton
                variant="ghost"
                size="sm"
                icon="refresh"
                :loading="busySlot === row.cap.slot"
                :title="t('models.rollbackHint')"
                @click="rollbackForSlot(row.cap.slot)"
              >
                {{ t("models.rollback") }}
              </YmButton>
            </template>
            <template v-else>
              <span class="muted">{{ t(STATE_HINTS[row.cap.state]) }}</span>
            </template>
            <span class="slot__spacer"></span>
            <span v-if="row.cap.updateAvailable" class="slot__update">
              {{ t("models.updateAvailable", { version: row.cap.updateVersion ?? "" }) }}
            </span>
            <span v-else-if="row.cap.state === 'READY'" class="slot__update">
              {{ t("models.upToDate") }}
            </span>
          </footer>

          <!-- 动作反馈紧跟触发的卡片，避免用户点了按钮却在页面顶部找结果。 -->
          <p
            v-if="slotFeedbackText[row.cap.slot]?.error"
            class="slot__feedback slot__feedback--error"
            role="alert"
          >
            <YmIcon name="alert" :size="16" />
            <bdi>{{ slotFeedbackText[row.cap.slot]?.error }}</bdi>
          </p>
          <p
            v-else-if="slotFeedbackText[row.cap.slot]?.notice"
            class="slot__feedback slot__feedback--notice"
            role="status"
          >
            <YmIcon name="check" :size="16" />
            <bdi>{{ slotFeedbackText[row.cap.slot]?.notice }}</bdi>
          </p>

          <p class="slot__hint">{{ slotHint(row) }}</p>
        </template>
        <p v-else class="muted">{{ t("models.unconfiguredHint") }}</p>
      </article>
    </div>

    <div class="section-head">
      <h2 class="section-title">{{ t("models.auxiliary") }}</h2>
    </div>

    <div class="cards">
      <article
        v-for="row in auxRows"
        :key="row.meta.key"
        class="slot slot--aux"
        :class="{ 'slot--needed': highlightedSlots.has(row.meta.key) }"
      >
        <header class="slot__head">
          <div class="slot__title">
            <h3>{{ t(row.meta.nameKey) }}</h3>
            <p v-if="engineSummary(row.cap)" class="slot__engine">
              {{ engineSummary(row.cap) }}
            </p>
          </div>
          <YmBadge
            :tone="isUnconfigured(row) ? 'neutral' : row.cap ? slotStateTone(row.cap.state) : 'neutral'"
          >
            {{
              isUnconfigured(row)
                ? t("models.unconfigured")
                : row.cap
                  ? t(slotStateLabelKey(row.cap.state))
                  : t("models.unknown")
            }}
          </YmBadge>
        </header>
        <p v-if="highlightedSlots.has(row.meta.key)" class="slot__needed">
          {{ t("models.neededByEntry") }}
        </p>
        <p v-if="hasUnknownDetails(row.cap)" class="slot__unknown">
          {{ t("models.infoUnavailable") }}
        </p>
        <template v-if="row.cap?.slot === 'tts'">
          <div v-if="activeInstallation(row.cap.slot)" class="progress">
            <div class="progress__head">
              <span>{{ t("models.progress") }}</span>
              <span class="mono">{{ Math.round(activeProgress(row.cap.slot)) }}%</span>
            </div>
            <YmProgress :value="activeProgress(row.cap.slot)" tone="info" />
            <p class="progress__phase">{{ activePhaseLabel(row.cap.slot) }}</p>
            <p v-if="activeSharedLabel(row.cap.slot)" class="progress__shared">
              {{ activeSharedLabel(row.cap.slot) }}
            </p>
          </div>
          <div
            v-else-if="row.cap.state === 'INSTALLING' || row.cap.state === 'VERIFYING'"
            class="progress"
          >
            <div class="progress__head">
              <span>{{ t("models.progress") }}</span>
            </div>
            <YmProgress indeterminate tone="info" />
            <p class="progress__phase">{{ t(STATE_HINTS[row.cap.state]) }}</p>
          </div>
          <footer class="slot__actions">
            <template v-if="activeInstallation(row.cap.slot)">
              <YmButton
                variant="secondary"
                size="sm"
                :loading="busySlot === row.cap.slot"
                @click="cancelForSlot(row.cap.slot)"
              >
                {{ t("models.cancelInstall") }}
              </YmButton>
            </template>
            <template
              v-else-if="
                row.cap.state === 'NEEDS_SETUP' ||
                row.cap.state === 'FAILED' ||
                row.cap.state === 'DEGRADED'
              "
            >
              <YmButton
                variant="primary"
                size="sm"
                icon="download"
                :disabled="!hasDownloadableRelease(row.cap)"
                :loading="busySlot === row.cap.slot"
                @click="startInstall(row.cap.slot)"
              >
                {{ t("models.install") }}
              </YmButton>
            </template>
            <template
              v-else-if="
                row.cap.state === 'READY' ||
                row.cap.state === 'UNVERIFIED' ||
                row.cap.state === 'NEEDS_ACTION' ||
                row.cap.state === 'RESTART_REQUIRED'
              "
            >
              <YmButton
                v-if="failedUpgrade(row.cap.slot)"
                variant="primary"
                size="sm"
                icon="download"
                :loading="busySlot === row.cap.slot"
                @click="startInstall(row.cap.slot)"
              >
                {{ t("models.retryInstall") }}
              </YmButton>
              <YmButton
                variant="secondary"
                size="sm"
                icon="trash"
                :loading="busySlot === row.cap.slot"
                @click="deleteTarget = row.cap"
              >
                {{ t("models.delete") }}
              </YmButton>
              <YmButton
                variant="ghost"
                size="sm"
                icon="refresh"
                :loading="busySlot === row.cap.slot"
                :title="t('models.rollbackHint')"
                @click="rollbackForSlot(row.cap.slot)"
              >
                {{ t("models.rollback") }}
              </YmButton>
            </template>
            <template v-else>
              <span class="muted">{{ t(STATE_HINTS[row.cap.state]) }}</span>
            </template>
          </footer>
          <p
            v-if="slotFeedbackText[row.cap.slot]?.error"
            class="slot__feedback slot__feedback--error"
            role="alert"
          >
            <YmIcon name="alert" :size="16" />
            <bdi>{{ slotFeedbackText[row.cap.slot]?.error }}</bdi>
          </p>
          <p
            v-else-if="slotFeedbackText[row.cap.slot]?.notice"
            class="slot__feedback slot__feedback--notice"
            role="status"
          >
            <YmIcon name="check" :size="16" />
            <bdi>{{ slotFeedbackText[row.cap.slot]?.notice }}</bdi>
          </p>
          <p class="slot__hint">{{ slotHint(row) }}</p>
        </template>
        <p v-else class="muted">{{ t("models.managedByApp") }}</p>
      </article>
    </div>

    <p class="footnote">{{ t("models.updateNote") }} {{ t("models.actionPlaceholder") }}</p>

    <InstallPlanDialog
      :model-value="planTarget !== null"
      :plan="planTarget"
      :loading="planTarget !== null && busySlot === planTarget.slot"
      @update:model-value="planTarget = null"
      @confirm="confirmInstall"
    />

    <YmDialog
      :model-value="deleteTarget !== null"
      :title="t('models.deleteTitle')"
      @update:model-value="deleteTarget = null"
    >
      <p>{{ t("models.deleteBody") }}</p>
      <p v-if="deleteSharedNames" class="dialog-meta">
        {{ t("models.deleteShared", { slots: deleteSharedNames }) }}
      </p>
      <p v-if="deleteTarget" class="dialog-meta mono">
        {{ t(slotNameKey(deleteTarget.slot)) }}
        <span v-if="deleteTarget.version"> · v{{ deleteTarget.version }}</span>
      </p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.close") }}</YmButton>
        <YmButton
          variant="danger"
          :loading="deleteTarget ? busySlot === deleteTarget.slot : false"
          @click="confirmDelete"
        >
          {{ t("common.delete") }}
        </YmButton>
      </template>
    </YmDialog>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 1080px;
}
.page__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__desc {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.notice-note {
  margin-top: var(--space-1);
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: var(--space-2);
}
.section-title {
  font-size: var(--font-size-lg);
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: var(--space-3);
}
.slot {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.slot--aux {
  background: var(--color-surface-2);
}
/* 从恢复入口带来的槽位：只用品牌蓝描边强调，不改变状态或可用性。 */
.slot--needed {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 1px var(--color-primary-soft);
}
.slot__needed {
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 500;
}
.slot__unknown {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.slot__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.slot__title h3 {
  font-size: var(--font-size-lg);
}
.slot__engine {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.meta__row {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  gap: var(--space-2);
}
.meta__row dt {
  color: var(--color-text-faint);
}
.meta__row dd {
  margin: 0;
  color: var(--color-text-muted);
  word-break: break-word;
}
.progress {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.progress__head {
  display: flex;
  justify-content: space-between;
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.progress__phase {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.progress__shared {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.engines {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.engines__title {
  margin: 0 0 2px;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  font-weight: 600;
}
.engine-option {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
  transition: border-color var(--motion-duration-snap) var(--motion-ease-out), background var(--motion-duration-snap) var(--motion-ease-out);
}
.engine-option:hover:not(.is-disabled) {
  border-color: var(--color-border-strong);
  background: var(--color-surface-2);
}
.engine-option.is-selected {
  border-color: color-mix(in srgb, var(--color-primary) 42%, var(--color-border));
  background: color-mix(in srgb, var(--color-primary) 4%, var(--color-surface));
}
.engine-option.is-disabled {
  cursor: not-allowed;
  opacity: 0.62;
}
.engine-option__radio {
  flex: none;
  accent-color: var(--color-primary);
}
.engine-option__main {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}
.engine-option__name {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.engine-option__meta {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.engine-option__state {
  flex: none;
}
.params {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.params__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.params__list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.params__actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.params__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.param {
  min-width: 0;
}
.slot__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
}
.slot__spacer {
  flex: 1;
}
.slot__update {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.slot__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.slot__feedback {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid;
  border-radius: var(--radius-md);
  font-size: var(--font-size-sm);
}
.slot__feedback svg {
  flex: none;
  margin-top: 1px;
}
.slot__feedback--error {
  border-color: var(--color-error);
  background: var(--color-error-soft);
}
.slot__feedback--error svg {
  color: var(--color-error);
}
.slot__feedback--notice {
  border-color: var(--color-info);
  background: var(--color-info-soft);
}
.slot__feedback--notice svg {
  color: var(--color-info);
}
.muted {
  font-size: var(--font-size-sm);
  color: var(--color-text-faint);
}
.footnote {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.dialog-meta {
  margin-top: var(--space-2);
  font-size: var(--font-size-sm);
}
</style>

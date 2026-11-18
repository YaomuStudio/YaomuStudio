<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import brandMark from "@/assets/brand/yaomu-mark.svg";
import { formatBytes, t } from "@/i18n";
import { api } from "@/api";
import {
  errorNotice,
  messageNotice,
  renderNotice,
  type SafeNotice,
} from "@/i18n/notice";
import {
  CAPABILITY_SLOTS,
  DISPLAY_MODEL_SLOTS,
  visibleModelSlots,
} from "@/domain/capabilities";
import {
  activeInstallationForSlot,
  hasDownloadableRelease,
  installationCoveredSlots,
  planCoverage,
} from "@/domain/installations";
import { slotStateLabelKey, slotStateTone, type Tone } from "@/domain/status";
import type {
  CapabilitySlotKey,
  EnvironmentCheck,
  EnvironmentInfo,
  Installation,
  InstallationPlan,
} from "@/domain/types";
import BulkInstallPlanDialog from "@/components/BulkInstallPlanDialog.vue";
import InstallPlanDialog from "@/components/InstallPlanDialog.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
import YmStepper from "@/components/ui/YmStepper.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useInstallationsStore } from "@/stores/installations";
import { useSettingsStore } from "@/stores/settings";

const STEPS = [
  { key: "welcome", labelKey: "onboarding.step.welcome" },
  { key: "environment", labelKey: "onboarding.step.environment" },
  { key: "models", labelKey: "onboarding.step.models" },
  { key: "storage", labelKey: "onboarding.step.storage" },
  { key: "permissions", labelKey: "onboarding.step.permissions" },
  { key: "ready", labelKey: "onboarding.step.ready" },
] as const;

type StepKey = (typeof STEPS)[number]["key"];

const STEP_STORAGE_KEY = "yaomu.onboarding.step";

const MODEL_METAS = DISPLAY_MODEL_SLOTS.map((key) =>
  CAPABILITY_SLOTS.find((meta) => meta.key === key),
).filter((meta): meta is (typeof CAPABILITY_SLOTS)[number] => meta !== undefined);

const settings = useSettingsStore();
const caps = useCapabilitiesStore();
const installs = useInstallationsStore();
const router = useRouter();

function loadStep(): StepKey {
  const saved = localStorage.getItem(STEP_STORAGE_KEY);
  const found = STEPS.find((s) => s.key === saved);
  return found ? found.key : "welcome";
}

const step = ref<StepKey>(loadStep());
const panelRef = ref<HTMLElement | null>(null);
const busy = ref<CapabilitySlotKey | null>(null);
const planTarget = ref<InstallationPlan | null>(null);
const bulkPlans = ref<InstallationPlan[]>([]);
const desktopWindow = typeof window !== "undefined" ? window.yaomu?.window : undefined;
const isLinuxDesktop =
  typeof window !== "undefined" && window.yaomu?.platform === "linux";
const maximized = ref(false);
let maximizePoll: ReturnType<typeof setInterval> | null = null;

function minimizeApp(): void {
  void desktopWindow?.minimize();
}
function closeApp(): void {
  void desktopWindow?.close();
}
function toggleMaximize(): void {
  void desktopWindow?.toggleMaximize().then((value) => {
    maximized.value = value;
  });
}

onMounted(() => {
  if (!isLinuxDesktop || !desktopWindow) return;
  void desktopWindow.isMaximized().then((value) => {
    maximized.value = value;
  });
  maximizePoll = setInterval(() => {
    void desktopWindow.isMaximized().then((value) => {
      maximized.value = value;
    });
  }, 800);
});
onBeforeUnmount(() => {
  if (maximizePoll) clearInterval(maximizePoll);
});

const bulkOpen = ref(false);
const bulkPreparing = ref(false);
const bulkInstalling = ref(false);
const error = ref<SafeNotice | null>(null);
const notice = ref<SafeNotice | null>(null);
const errorText = computed(() => renderNotice(error.value));
const noticeText = computed(() => renderNotice(notice.value));
const environment = ref<EnvironmentInfo | null>(null);
const environmentLoading = ref(false);
const environmentError = ref<SafeNotice | null>(null);
const environmentErrorText = computed(() => renderNotice(environmentError.value));

watch(step, async (value) => {
  localStorage.setItem(STEP_STORAGE_KEY, value);
  error.value = null;
  notice.value = null;
  if (value === "environment") void loadEnvironment();
  // 步骤切换后把阅读位置和焦点交给新步骤，避免焦点留在已消失的按钮上。
  await nextTick();
  panelRef.value?.scrollTo({ top: 0 });
  panelRef.value?.focus({ preventScroll: true });
});

const stepIndex = computed(() => STEPS.findIndex((s) => s.key === step.value));
const currentStepLabel = computed(() => t(STEPS[stepIndex.value]?.labelKey ?? "onboarding.step.welcome"));
const stepperSteps = computed(() =>
  STEPS.map((s) => ({ key: s.key, label: t(s.labelKey) })),
);

const storageRows = computed(() => [
  { key: "models", labelKey: "onboarding.storage.models", bytes: caps.storage?.models ?? 0 },
  { key: "media", labelKey: "onboarding.storage.media", bytes: caps.storage?.media ?? 0 },
  { key: "cache", labelKey: "onboarding.storage.cache", bytes: caps.storage?.cache ?? 0 },
  { key: "exports", labelKey: "onboarding.storage.exports", bytes: caps.storage?.exports ?? 0 },
]);

const storageTotal = computed(() =>
  storageRows.value.reduce((sum, row) => sum + row.bytes, 0),
);

const ffmpeg = computed(() => caps.slotState("ffmpeg"));

async function loadEnvironment(force = false): Promise<void> {
  environmentLoading.value = true;
  environmentError.value = null;
  try {
    environment.value = await api.getEnvironment({ refresh: force });
  } catch (err) {
    environmentError.value = errorNotice(err, "onboarding.environment.error");
  } finally {
    environmentLoading.value = false;
  }
}

function checkTone(severity: EnvironmentCheck["severity"]): Tone {
  switch (severity) {
    case "pass":
      return "success";
    case "blocker":
      return "error";
    default:
      return "warning";
  }
}

function severityLabel(severity: EnvironmentCheck["severity"]): string {
  return t(`onboarding.environment.severity.${severity}`);
}

function checkMessage(key: string): string {
  const text = t(key);
  return text === key ? t("onboarding.environment.unknownCheck") : text;
}

function hasAction(action: string): boolean {
  return environment.value?.checks.some((check) => check.actionIds.includes(action)) ?? false;
}

function deviceKindLabel(kind: EnvironmentInfo["devices"][number]["kind"]): string {
  return t(`settings.devices.kind.${kind}`);
}

function slotNameKey(slot: CapabilitySlotKey): string {
  return CAPABILITY_SLOTS.find((meta) => meta.key === slot)?.nameKey ?? slot;
}

function capOf(slot: CapabilitySlotKey) {
  return caps.slotState(slot);
}

function slotTone(slot: CapabilitySlotKey): Tone {
  const cap = capOf(slot);
  return cap ? slotStateTone(cap.state) : "neutral";
}

function slotLabel(slot: CapabilitySlotKey): string {
  const cap = capOf(slot);
  return cap ? t(slotStateLabelKey(cap.state)) : t("state.unknown");
}

function slotNames(slots: CapabilitySlotKey[]): string {
  return slots.map(slotNameKey).map((key) => t(key)).join(", ");
}

/** 位于引导页时也不按发起 slot 猜：共享安装按 boundSlots 找到同一 ID/进度。 */
function activeInstallation(slot: CapabilitySlotKey): Installation | undefined {
  return activeInstallationForSlot(installs.items, slot);
}

/** 共享下载的完整覆盖说明；取消的是同一个物理操作。 */
function activeSharedLabel(slot: CapabilitySlotKey): string {
  const inst = activeInstallation(slot);
  if (!inst) return "";
  const covered = visibleModelSlots(installationCoveredSlots(inst));
  return covered.length > 1
    ? t("onboarding.models.sharedProgress", { slots: slotNames(covered) })
    : "";
}

function activeProgress(slot: CapabilitySlotKey): number {
  return activeInstallation(slot)?.progress ?? 0;
}

/** 安装进行中：有活动记录，或能力目录已经是安装/校验。 */
function slotInstalling(slot: CapabilitySlotKey): boolean {
  if (activeInstallation(slot)) return true;
  const state = capOf(slot)?.state;
  return state === "INSTALLING" || state === "VERIFYING";
}

function activePhaseLabel(slot: CapabilitySlotKey): string {
  const inst = activeInstallation(slot);
  return inst ? t(`models.phase.${inst.phase}`) : "";
}

/** 当前版本可下载的槽位；未适配/未准入的模型只展示禁用下载，不参与批量下载。 */
function isDownloadable(slot: CapabilitySlotKey): boolean {
  return hasDownloadableRelease(capOf(slot));
}

/** 行内提示：未支持的模型说明没有可下载版本，其余保持模型包信息。 */
function modelHint(slot: CapabilitySlotKey): string {
  const cap = capOf(slot);
  if (cap && !hasDownloadableRelease(cap)) {
    return t("installation.release_not_configured");
  }
  return t("onboarding.models.sizeHint");
}

const missingModelSlots = computed(() =>
  MODEL_METAS.filter((meta) => {
    const cap = capOf(meta.key);
    if (!cap || cap.state === "READY" || activeInstallation(meta.key)) return false;
    return hasDownloadableRelease(cap);
  }),
);
const activeModelSlots = computed(() =>
  MODEL_METAS.filter((meta) => Boolean(activeInstallation(meta.key))),
);
const activeModelProgress = computed(() => {
  if (!activeModelSlots.value.length) return 0;
  const sum = activeModelSlots.value.reduce((total, meta) => total + activeProgress(meta.key), 0);
  return sum / activeModelSlots.value.length;
});

async function refreshInstallations(): Promise<void> {
  try {
    // 与 Models 共用同一安装 store/ID 通知去重；事件到达时只刷新同一列表。
    await installs.load();
  } catch (err) {
    error.value = errorNotice(err, "onboarding.error");
  }
}

async function loadCapabilities(): Promise<void> {
  try {
    await caps.load();
  } catch (err) {
    error.value = errorNotice(err, "onboarding.error");
  }
}

async function installModel(slot: CapabilitySlotKey): Promise<void> {
  // 未支持的模型没有可解析的发布，直接禁用入口，不发无效计划请求。
  if (activeInstallation(slot) || !isDownloadable(slot)) return;
  busy.value = slot;
  error.value = null;
  notice.value = null;
  try {
    // 计划先给用户确认（下载/权限范围），确认后才执行安装。
    planTarget.value = await api.planInstallation(slot);
  } catch (err) {
    error.value = errorNotice(err, "onboarding.error");
  } finally {
    busy.value = null;
  }
}

async function planAllModels(): Promise<void> {
  if (bulkPreparing.value || bulkInstalling.value || !missingModelSlots.value.length) return;
  bulkPreparing.value = true;
  error.value = null;
  notice.value = null;
  try {
    const plans: InstallationPlan[] = [];
    const seen = new Set<string>();
    for (const meta of missingModelSlots.value) {
      const plan = await api.planInstallation(meta.key);
      if (!planCoverage(plan).valid) {
        error.value = errorNotice(new Error(t("install.plan.invalidRange")), "onboarding.error");
        return;
      }
      const key = [
        plan.release.releaseId,
        plan.release.version,
        plan.release.digest,
        plan.release.channel ?? "",
      ].join("|");
      if (seen.has(key)) continue;
      seen.add(key);
      plans.push(plan);
    }
    if (!plans.length) return;
    bulkPlans.value = plans;
    bulkOpen.value = true;
  } catch (err) {
    error.value = errorNotice(err, "onboarding.error");
  } finally {
    bulkPreparing.value = false;
  }
}

async function confirmBulkInstall(): Promise<void> {
  if (!bulkPlans.value.length || bulkInstalling.value) return;
  bulkInstalling.value = true;
  error.value = null;
  notice.value = null;
  let started = 0;
  try {
    for (const plan of bulkPlans.value) {
      const created = await api.install({ planId: plan.planId, planHash: plan.planHash });
      installs.track(created.id, plan.slot);
      started += 1;
    }
    bulkOpen.value = false;
    bulkPlans.value = [];
    await refreshInstallations();
    await loadCapabilities();
    notice.value = messageNotice("onboarding.models.bulkStarted", { count: started });
  } catch (err) {
    bulkOpen.value = false;
    bulkPlans.value = [];
    error.value = errorNotice(err, "onboarding.error");
    await refreshInstallations();
    await loadCapabilities();
  } finally {
    bulkInstalling.value = false;
  }
}

async function confirmInstall(): Promise<void> {
  const plan = planTarget.value;
  if (!plan) return;
  const coverage = planCoverage(plan);
  // 覆盖范围异常时不允许提交；不把未知范围猜成单槽。
  if (!coverage.valid) return;
  busy.value = plan.slot;
  error.value = null;
  notice.value = null;
  try {
    const created = await api.install({ planId: plan.planId, planHash: plan.planHash });
    // 与 Models 共用同一物理 ID 的通知去重；完成时只提醒一次。
    installs.track(created.id, plan.slot);
    planTarget.value = null;
    // 写后重读：列表与能力目录都由 Core 投影，不用旧回执假装已 READY。
    await refreshInstallations();
    await loadCapabilities();
  } catch (err) {
    planTarget.value = null;
    error.value = errorNotice(err, "onboarding.error");
  } finally {
    busy.value = null;
  }
}

async function cancelModel(slot: CapabilitySlotKey): Promise<void> {
  const inst = activeInstallation(slot);
  if (!inst) return;
  const shared = installationCoveredSlots(inst).length > 1;
  busy.value = slot;
  error.value = null;
  notice.value = null;
  try {
    // 取消作用于该物理安装 ID 的全部覆盖槽；完成后重读而非乐观改本地状态。
    await api.cancelInstallation(inst.id);
    await refreshInstallations();
    await loadCapabilities();
    notice.value = shared
      ? messageNotice("onboarding.models.cancelRequestedShared")
      : messageNotice("onboarding.models.cancelRequested");
  } catch (err) {
    error.value = errorNotice(err, "onboarding.error");
  } finally {
    busy.value = null;
  }
}

function next(): void {
  const index = stepIndex.value;
  const nextStep = STEPS[index + 1];
  if (nextStep) step.value = nextStep.key;
}

function back(): void {
  const index = stepIndex.value;
  const previous = STEPS[index - 1];
  if (previous) step.value = previous.key;
}

function jump(value: string): void {
  const found = STEPS.find((s) => s.key === value);
  if (found) step.value = found.key;
}

function finish(): void {
  localStorage.removeItem(STEP_STORAGE_KEY);
  settings.markOnboarded();
  void router.push("/");
}

const mainRef = ref<HTMLElement | null>(null);
/** 高度不足时压缩固定内容，把空间留给步骤内滚动列表（zoom 缩放下同样生效）。 */
const compactLayout = ref(false);
let mainResizeObserver: ResizeObserver | null = null;

onMounted(() => {
  void refreshInstallations();
  if (!caps.slots.length) void loadCapabilities();
  if (step.value === "environment") void loadEnvironment();

  const main = mainRef.value;
  if (main && typeof ResizeObserver !== "undefined") {
    const updateCompact = (): void => {
      compactLayout.value = main.clientHeight < 560;
    };
    updateCompact();
    mainResizeObserver = new ResizeObserver(updateCompact);
    mainResizeObserver.observe(main);
  }
});

onBeforeUnmount(() => {
  mainResizeObserver?.disconnect();
  mainResizeObserver = null;
});
</script>

<template>
  <div class="onboarding">
    <header class="onboarding__top">
      <div class="brand">
        <span class="brand__mark">
          <img :src="brandMark" alt="" aria-hidden="true" />
        </span>
        <div>
          <p class="brand__name">{{ t("app.name") }}</p>
          <p class="brand__tag">{{ t("app.tagline") }}</p>
        </div>
      </div>
      <p class="step-count" aria-live="polite" aria-atomic="true">
        {{ t("onboarding.stepProgress", { current: stepIndex + 1, total: STEPS.length }) }}
      </p>
      <div
        v-if="isLinuxDesktop"
        class="onboarding__wincaps"
        role="group"
        :aria-label="t('app.name')"
      >
        <button type="button" aria-label="Minimize" @click="minimizeApp">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path d="M0 5h10" stroke="currentColor" stroke-width="1" />
          </svg>
        </button>
        <button type="button" aria-label="Maximize" @click="toggleMaximize">
          <svg v-if="maximized" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" fill="none">
            <rect x="0.5" y="2.5" width="7" height="7" stroke="currentColor" stroke-width="1" />
            <path d="M2.5 2.5V0.5h7v7h-2" stroke="currentColor" stroke-width="1" />
          </svg>
          <svg v-else width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" fill="none">
            <rect x="0.5" y="0.5" width="9" height="9" stroke="currentColor" stroke-width="1" />
          </svg>
        </button>
        <button type="button" class="is-close" aria-label="Close" @click="closeApp">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path d="M0 0l10 10M10 0L0 10" stroke="currentColor" stroke-width="1" />
          </svg>
        </button>
      </div>
    </header>

    <main
      ref="mainRef"
      class="onboarding__main"
      :class="{ 'onboarding__main--compact': compactLayout }"
    >
      <header class="onboarding__head">
        <h1 class="onboarding__title">{{ t("onboarding.title") }}</h1>
        <p class="onboarding__subtitle">{{ t("onboarding.subtitle") }}</p>
      </header>

      <div class="stepper-wrap">
        <YmStepper :steps="stepperSteps" :current="step" @update:current="jump" />
      </div>

      <section
        ref="panelRef"
        class="panel"
        :class="{ 'panel--scroll': step !== 'models' && step !== 'environment' }"
        role="region"
        tabindex="-1"
        :aria-label="currentStepLabel"
        data-testid="onboarding-step-panel"
      >
        <template v-if="step === 'welcome'">
          <div class="hero">
            <span class="hero__icon"><YmIcon name="shield" :size="26" /></span>
            <div>
              <h2 class="panel__title">{{ t("onboarding.welcome.title") }}</h2>
              <p class="panel__body">{{ t("onboarding.welcome.body") }}</p>
            </div>
          </div>
          <ul class="points">
            <li class="point">
              <YmIcon name="check" :size="15" />
              <span>{{ t("onboarding.welcome.point1") }}</span>
            </li>
            <li class="point">
              <YmIcon name="check" :size="15" />
              <span>{{ t("onboarding.welcome.point2") }}</span>
            </li>
            <li class="point">
              <YmIcon name="check" :size="15" />
              <span>{{ t("onboarding.welcome.point3") }}</span>
            </li>
          </ul>
          <p class="hint">{{ t("onboarding.welcome.note") }}</p>
        </template>

        <template v-else-if="step === 'environment'">
          <h2 class="panel__title">{{ t("onboarding.environment.title") }}</h2>
          <p class="panel__body">{{ t("onboarding.environment.body") }}</p>

          <div v-scroll-fade class="env-scroll ym-scroll-fade">
            <div v-if="environmentLoading" class="hint">
              {{ t("onboarding.environment.loading") }}
            </div>
            <YmNotice v-else-if="environmentErrorText" tone="error">
              <bdi>{{ environmentErrorText }}</bdi>
            </YmNotice>
            <template v-else-if="environment">
              <div class="env-summary">
                <div class="env-summary__row">
                  <span class="env-summary__label">{{ t("onboarding.environment.platform") }}</span>
                  <span>{{ t(`onboarding.environment.platform.${environment.platform}`) }}</span>
                </div>
                <div v-if="environment.wsl" class="env-summary__row">
                  <span class="env-summary__label">WSL</span>
                  <span>
                    {{
                      environment.wsl.available
                        ? t("onboarding.environment.wslReady", {
                            version: environment.wsl.version ?? "—",
                          })
                        : t("onboarding.environment.wslMissing")
                    }}
                  </span>
                </div>
              </div>

              <div class="env-checks" data-testid="onboarding-environment-checks">
                <h3 class="env-section">{{ t("onboarding.environment.checks") }}</h3>
                <div v-for="check in environment.checks" :key="check.code" class="env-check">
                  <YmBadge :tone="checkTone(check.severity)">
                    {{ severityLabel(check.severity) }}
                  </YmBadge>
                  <span>{{ checkMessage(check.messageKey) }}</span>
                </div>
                <p v-if="environment.checks.length === 0" class="hint">
                  {{ t("onboarding.environment.noChecks") }}
                </p>
              </div>

              <div v-if="hasAction('install_wsl')" class="env-guide" data-testid="env-guide-wsl">
                <p class="env-guide__title">{{ t("onboarding.environment.guide.wslTitle") }}</p>
                <p class="env-guide__body">{{ t("onboarding.environment.guide.wslBody") }}</p>
                <code class="env-guide__code">wsl --install</code>
              </div>
              <div
                v-if="hasAction('install_gpu_driver')"
                class="env-guide"
                data-testid="env-guide-driver"
              >
                <p class="env-guide__title">{{ t("onboarding.environment.guide.driverTitle") }}</p>
                <p class="env-guide__body">{{ t("onboarding.environment.guide.driverBody") }}</p>
              </div>

              <div class="env-devices">
                <h3 class="env-section">{{ t("onboarding.environment.devices") }}</h3>
                <div v-for="device in environment.devices" :key="device.id" class="env-device">
                  <div class="env-device__main">
                    <span class="env-device__name"><bdi dir="auto">{{ device.name }}</bdi></span>
                    <span class="hint">
                      {{ deviceKindLabel(device.kind) }}
                      <template v-if="device.driverVersion"> · {{ device.driverVersion }}</template>
                    </span>
                  </div>
                  <span class="mono hint">
                    <template v-if="device.memoryTotalBytes !== undefined">
                      {{ device.memoryUsedBytes !== undefined ? formatBytes(device.memoryUsedBytes) : "—" }} /
                      {{ formatBytes(device.memoryTotalBytes) }}
                    </template>
                    <template v-else>—</template>
                  </span>
                  <YmBadge v-if="device.recommended" tone="info">
                    {{ t("onboarding.environment.recommended") }}
                  </YmBadge>
                </div>
                <p v-if="environment.devices.length === 0" class="hint">
                  {{ t("onboarding.environment.noDevices") }}
                </p>
              </div>
            </template>

          </div>
          <div v-if="!environmentLoading" class="env-actions">
            <YmButton size="sm" icon="refresh" @click="loadEnvironment(true)">
              {{ t("onboarding.environment.refresh") }}
            </YmButton>
          </div>
        </template>

        <template v-else-if="step === 'models'">
          <h2 class="panel__title">{{ t("onboarding.models.title") }}</h2>
          <p class="panel__body">{{ t("onboarding.finishHint") }}</p>
          <!-- 错误放列表顶部：失败时原先只在页脚上方，长列表下容易看成“没反应”。 -->
          <YmNotice v-if="errorText" tone="error"><bdi>{{ errorText }}</bdi></YmNotice>
          <YmNotice v-if="noticeText" tone="success"><bdi>{{ noticeText }}</bdi></YmNotice>

          <div v-if="missingModelSlots.length" class="bulk-toolbar">
            <p class="bulk-toolbar__hint">{{ t("onboarding.models.bulkHint") }}</p>
            <YmButton
              variant="primary"
              icon="download"
              :loading="bulkPreparing"
              :disabled="bulkInstalling"
              @click="planAllModels"
            >
              {{ t("onboarding.models.downloadAll", { count: missingModelSlots.length }) }}
            </YmButton>
          </div>

          <div v-if="activeModelSlots.length" class="bulk-progress">
            <div class="bulk-progress__head">
              <span>{{ t("onboarding.models.bulkRunning", { count: activeModelSlots.length }) }}</span>
              <span v-if="activeModelProgress > 0" class="mono">{{ Math.round(activeModelProgress) }}%</span>
            </div>
            <YmProgress
              :value="activeModelProgress"
              :indeterminate="activeModelProgress <= 0"
              tone="info"
            />
          </div>

          <div class="model-list">
            <div v-scroll-fade class="model-list__scroll ym-scroll-fade">
              <div v-for="meta in MODEL_METAS" :key="meta.key" class="model">
                <div class="model__main">
                  <div class="model__title">
                    <span>{{ t(meta.nameKey) }}</span>
                    <YmBadge :tone="slotTone(meta.key)">
                      {{ slotLabel(meta.key) }}
                    </YmBadge>
                  </div>
                  <div v-if="slotInstalling(meta.key)" class="model__progress">
                    <YmProgress
                      :value="activeProgress(meta.key)"
                      :indeterminate="activeProgress(meta.key) <= 0"
                      tone="info"
                    />
                    <span class="hint">
                      <template v-if="activeProgress(meta.key) > 0">
                        {{ Math.round(activeProgress(meta.key)) }}% ·
                      </template>
                      {{ activePhaseLabel(meta.key) || t("state.installing") }}
                    </span>
                    <span v-if="activeSharedLabel(meta.key)" class="hint">
                      {{ activeSharedLabel(meta.key) }}
                    </span>
                  </div>
                  <p v-else class="hint">{{ modelHint(meta.key) }}</p>
                </div>
                <div class="model__actions">
                  <template v-if="slotInstalling(meta.key)">
                    <YmButton size="sm" disabled>
                      {{ t("state.installing") }}
                    </YmButton>
                    <YmButton
                      v-if="activeInstallation(meta.key)"
                      size="sm"
                      :loading="busy === meta.key"
                      :title="activeSharedLabel(meta.key) || undefined"
                      @click="cancelModel(meta.key)"
                    >
                      {{ t("onboarding.models.cancel") }}
                    </YmButton>
                  </template>
                  <YmButton
                    v-else-if="capOf(meta.key)?.state === 'READY'"
                    size="sm"
                    icon="check"
                    disabled
                  >
                    {{ t("onboarding.models.ready") }}
                  </YmButton>
                  <YmButton
                    v-else
                    size="sm"
                    variant="secondary"
                    icon="download"
                    :disabled="!isDownloadable(meta.key)"
                    :loading="busy === meta.key"
                    @click="installModel(meta.key)"
                  >
                    {{ t("onboarding.models.install") }}
                  </YmButton>
                </div>
              </div>
            </div>
          </div>

          <p class="hint">{{ t("onboarding.models.resumeHint") }}</p>
        </template>

        <template v-else-if="step === 'storage'">
          <h2 class="panel__title">{{ t("onboarding.storage.title") }}</h2>
          <p class="panel__body">{{ t("onboarding.storage.body") }}</p>

          <div class="storage-total">
            <span>{{ t("onboarding.storage.total") }}</span>
            <strong class="mono">{{ formatBytes(storageTotal) }}</strong>
          </div>

          <div class="storage-list">
            <div v-for="row in storageRows" :key="row.key" class="storage-row">
              <div class="storage-row__head">
                <span>{{ t(row.labelKey) }}</span>
                <span class="mono storage-row__bytes">{{ formatBytes(row.bytes) }}</span>
              </div>
              <YmProgress
                :value="storageTotal > 0 ? (row.bytes / storageTotal) * 100 : 0"
                tone="accent"
              />
            </div>
          </div>

          <p class="hint">{{ t("onboarding.storage.location") }}</p>
          <p class="warning">{{ t("onboarding.storage.warning") }}</p>
        </template>

        <template v-else-if="step === 'permissions'">
          <h2 class="panel__title">{{ t("onboarding.permissions.title") }}</h2>
          <p class="panel__body">{{ t("onboarding.permissions.body") }}</p>

          <div class="permission-list">
            <div class="permission">
              <div>
                <p class="permission__title">{{ t("onboarding.permissions.folders") }}</p>
                <p class="permission__body">{{ t("onboarding.permissions.foldersValue") }}</p>
              </div>
              <YmBadge tone="info">{{ t("onboarding.permissions.later") }}</YmBadge>
            </div>
            <div class="permission">
              <div>
                <p class="permission__title">{{ t("onboarding.permissions.mic") }}</p>
                <p class="permission__body">{{ t("onboarding.permissions.micValue") }}</p>
              </div>
              <YmBadge tone="neutral">{{ t("onboarding.permissions.notRequested") }}</YmBadge>
            </div>
            <div class="permission">
              <div>
                <p class="permission__title">{{ t("onboarding.permissions.media") }}</p>
                <p class="permission__body">{{ t("onboarding.permissions.mediaValue") }}</p>
              </div>
              <YmBadge :tone="ffmpeg ? slotStateTone(ffmpeg.state) : 'neutral'">
                {{ ffmpeg ? t(slotStateLabelKey(ffmpeg.state)) : t("state.unverified") }}
              </YmBadge>
            </div>
          </div>
        </template>

        <template v-else>
          <div class="hero">
            <span class="hero__icon"><YmIcon name="check" :size="26" /></span>
            <div>
              <h2 class="panel__title">{{ t("onboarding.ready.title") }}</h2>
              <p class="panel__body">{{ t("onboarding.ready.body") }}</p>
            </div>
          </div>

          <p class="offline">{{ t("onboarding.ready.offline") }}</p>

          <div class="telemetry">
            <div class="telemetry__text">
              <p class="permission__title">{{ t("onboarding.ready.telemetryTitle") }}</p>
              <p class="permission__body">{{ t("onboarding.ready.telemetryBody") }}</p>
            </div>
            <div class="telemetry__control">
              <YmSwitch
                :aria-label="t('onboarding.ready.telemetryTitle')"
                :model-value="settings.telemetry"
                @update:model-value="settings.telemetry = $event"
              />
              <span class="hint">{{ t("onboarding.ready.telemetrySwitch") }}</span>
            </div>
          </div>

          <p class="hint">{{ t("onboarding.finishHint") }}</p>
        </template>
      </section>

      <!-- 模型步骤已在面板内靠近列表显示提示，避免同一状态在页脚上方重复出现。 -->
      <YmNotice v-if="errorText && step !== 'models'" tone="error">
        <bdi>{{ errorText }}</bdi>
      </YmNotice>
      <YmNotice v-if="noticeText && step !== 'models'" tone="success">
        <bdi>{{ noticeText }}</bdi>
      </YmNotice>

      <footer class="onboarding__actions">
        <YmButton
          v-if="stepIndex > 0"
          data-testid="onboarding-back"
          variant="secondary"
          icon="chevron-left"
          @click="back"
        >
          {{ t("onboarding.back") }}
        </YmButton>
        <span class="spacer"></span>
        <YmButton v-if="step === 'models'" variant="ghost" @click="next">
          {{ t("onboarding.skip") }}
        </YmButton>
        <YmButton v-if="step !== 'ready'" variant="primary" @click="next">
          {{ t("onboarding.next") }}
        </YmButton>
        <YmButton v-else variant="primary" icon="check" @click="finish">
          {{ t("onboarding.finish") }}
        </YmButton>
      </footer>
    </main>

    <BulkInstallPlanDialog
      :model-value="bulkOpen"
      :plans="bulkPlans"
      :loading="bulkInstalling"
      @update:model-value="bulkOpen = $event"
      @confirm="confirmBulkInstall"
    />

    <InstallPlanDialog
      :model-value="planTarget !== null"
      :plan="planTarget"
      :loading="planTarget !== null && busy === planTarget.slot"
      @update:model-value="planTarget = null"
      @confirm="confirmInstall"
    />
  </div>
</template>

<style scoped>
.onboarding {
  display: flex;
  flex-direction: column;
  height: 100%;
  /* 首屏引导不允许整页滚动；滚动只发生在步骤内的列表区域。 */
  overflow: hidden;
  background: var(--color-bg);
}
.onboarding__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-6);
  background: transparent;
  flex: none;
}
.brand {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.brand__mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
}
.brand__mark img {
  display: block;
  height: 32px;
  width: auto;
}
.brand__name {
  font-size: var(--font-size-md);
  font-weight: 600;
}
.brand__tag {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.step-count {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.onboarding__wincaps {
  position: absolute;
  top: 0;
  right: 0;
  display: flex;
  height: 100%;
  -webkit-app-region: no-drag;
}
.onboarding__wincaps button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 46px;
  height: 100%;
  color: var(--color-text-muted);
  background: transparent;
  transition: background var(--motion-duration-snap) var(--motion-ease-snap);
}
.onboarding__wincaps button:hover {
  background: color-mix(in srgb, var(--color-text) 8%, transparent);
}
.onboarding__wincaps button.is-close:hover {
  background: #e81123;
  color: #fff;
}
.onboarding__main {
  width: 100%;
  max-width: 880px;
  margin: 0 auto;
  padding: var(--space-6) var(--space-5) var(--space-7);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
.onboarding__head {
  text-align: center;
  padding: var(--space-2) 0;
  flex: none;
}
.onboarding__title {
  font-size: var(--font-size-2xl);
}
.onboarding__subtitle {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.stepper-wrap {
  overflow-x: auto;
  display: flex;
  justify-content: center;
  padding: var(--space-1) 0;
  flex: none;
}
.panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  flex: 1;
  min-height: 0;
  padding: var(--space-5);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xl);
  overflow: hidden;
}
/* 无长列表的步骤在面板内部兜底滚动，仍然不影响整页。 */
.panel--scroll {
  overflow-y: auto;
}
.panel:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}
.panel__title {
  font-size: var(--font-size-xl);
}
.panel__body {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.hero {
  display: flex;
  gap: var(--space-4);
  align-items: flex-start;
}
.hero__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  flex: none;
  border-radius: var(--radius-lg);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.points {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.point {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  color: var(--color-text-muted);
}
.point svg {
  color: var(--color-success);
  flex: none;
  margin-top: 2px;
}
.hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.warning {
  padding: var(--space-3);
  border: 1px solid var(--color-warning);
  border-radius: var(--radius-md);
  background: var(--color-warning-soft);
  font-size: var(--font-size-sm);
}
.env-summary {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.env-summary__row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-4);
  font-size: var(--font-size-sm);
}
.env-summary__label {
  color: var(--color-text-muted);
}
.env-checks,
.env-devices {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
/* 环境详情（检查项、指引、设备）作为一个列表区滚动；标题与刷新按钮固定。 */
.env-scroll {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.env-actions {
  flex: none;
}
.env-section {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.env-check {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.env-guide {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-3);
  border: 1px solid var(--color-info);
  border-radius: var(--radius-md);
  background: var(--color-info-soft);
}
.env-guide__title {
  font-size: var(--font-size-sm);
  font-weight: 600;
}
.env-guide__body {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.env-guide__code {
  align-self: flex-start;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  font-size: var(--font-size-xs);
}
.env-device {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.env-device__main {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}
.env-device__name {
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bulk-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.bulk-toolbar__hint {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
@media (max-width: 720px) {
  .bulk-toolbar {
    align-items: stretch;
    flex-direction: column;
  }
  .bulk-toolbar :deep(.ym-btn) {
    align-self: flex-end;
  }
}
.bulk-progress {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.bulk-progress__head {
  display: flex;
  justify-content: space-between;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.model-list {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow: hidden;
}
.model-list__scroll {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.model {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--color-border);
}
.model:last-child {
  border-bottom: none;
}
.model__main {
  min-width: 0;
  flex: 1;
}
.model__title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  font-weight: 500;
}
.model__progress {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin-top: var(--space-2);
}
.model__actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: var(--space-2);
}
.storage-total {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.storage-total strong {
  font-size: var(--font-size-xl);
  color: var(--color-text);
}
.storage-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.storage-row {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.storage-row__head {
  display: flex;
  justify-content: space-between;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.storage-row__bytes {
  font-size: var(--font-size-xs);
}
.permission-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.permission {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.permission__title {
  font-size: var(--font-size-md);
  font-weight: 500;
}
.permission__body {
  margin-top: 2px;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.offline {
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-accent-soft);
  color: var(--color-text);
  font-size: var(--font-size-sm);
}
.telemetry {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.telemetry__text {
  min-width: 0;
}
.telemetry__control {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: var(--space-1);
  flex: none;
  max-width: 160px;
  text-align: right;
}
.onboarding__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
  row-gap: var(--space-2);
  flex: none;
}
.spacer {
  flex: 1;
}
.alert {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-error);
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  font-size: var(--font-size-sm);
}
.notice {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-info);
  border-radius: var(--radius-md);
  background: var(--color-info-soft);
  font-size: var(--font-size-sm);
}
/* 短窗口/高缩放下压缩固定内容，把高度留给步骤内的滚动列表。 */
.onboarding__main--compact {
  padding-top: var(--space-3);
  padding-bottom: var(--space-4);
  gap: var(--space-2);
}
.onboarding__main--compact .onboarding__head {
  display: none;
}
.onboarding__main--compact .panel {
  gap: var(--space-2);
  padding: var(--space-3);
}
.onboarding__main--compact .panel__body {
  margin-top: 0;
}
</style>

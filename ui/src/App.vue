<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { t } from "@/i18n";
import { apiMode } from "@/api";
import AppShell from "@/components/layout/AppShell.vue";
import RuntimeInitialization from "@/components/runtime/RuntimeInitialization.vue";
import { runtimePhaseLabel, runtimeReasonLabel } from "@/components/runtime/labels";
import YmButton from "@/components/ui/YmButton.vue";
import { useProjectsStore } from "@/stores/projects";
import { useTasksStore } from "@/stores/tasks";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useNotificationsStore } from "@/stores/notifications";
import { useRuntimeStore } from "@/stores/runtime";
import { safeYaomu } from "@/api/bridge-access";

const route = useRoute();
const isOnboarding = computed(() => route.name === "onboarding");

const projects = useProjectsStore();
const tasks = useTasksStore();
const caps = useCapabilitiesStore();
const notifications = useNotificationsStore();
const isBrowserPreview =
  import.meta.env.MODE === "development" &&
  !safeYaomu() &&
  !navigator.userAgent.includes("Electron");
const runtime = useRuntimeStore();

/**
 * 首次宿主就绪前只挂初始化面板；旧桥/演示/不可用模式不等待。
 * 本机 Core 通常 40–150ms 就绪，直接挂载会让语言/主题卡片只闪一帧：
 * 延迟出现，只有真正慢的启动或失败才展示进度与重试入口。
 */
const STARTUP_PANEL_DELAY_MS = 400;
/** 一旦出现至少保持该时长，避免刚好在阈值附近就绪时仍只闪一下。 */
const STARTUP_PANEL_MIN_VISIBLE_MS = 300;
const startupPanelVisible = ref(false);
const startupPending = computed(() => runtime.gated && !runtime.firstReady);
let startupShowTimer: number | undefined;
let startupHideTimer: number | undefined;
let startupPanelShownAt = 0;

function clearStartupPanelTimers(): void {
  if (startupShowTimer !== undefined) {
    window.clearTimeout(startupShowTimer);
    startupShowTimer = undefined;
  }
  if (startupHideTimer !== undefined) {
    window.clearTimeout(startupHideTimer);
    startupHideTimer = undefined;
  }
}

function showStartupPanel(): void {
  if (startupShowTimer !== undefined) {
    window.clearTimeout(startupShowTimer);
    startupShowTimer = undefined;
  }
  if (startupPanelVisible.value) return;
  startupPanelVisible.value = true;
  startupPanelShownAt = performance.now();
}

function hideStartupPanel(): void {
  if (!startupPanelVisible.value) return;
  const elapsed = performance.now() - startupPanelShownAt;
  if (elapsed >= STARTUP_PANEL_MIN_VISIBLE_MS) {
    startupPanelVisible.value = false;
    return;
  }
  if (startupHideTimer === undefined) {
    startupHideTimer = window.setTimeout(() => {
      startupHideTimer = undefined;
      startupPanelVisible.value = false;
    }, STARTUP_PANEL_MIN_VISIBLE_MS - elapsed);
  }
}

/** 就绪前延迟展示初始化面板；失败立即展示，就绪后取消尚未出现的面板。 */
function syncStartupPanel(): void {
  if (!startupPending.value) {
    if (startupShowTimer !== undefined) {
      window.clearTimeout(startupShowTimer);
      startupShowTimer = undefined;
    }
    hideStartupPanel();
    return;
  }
  if (startupHideTimer !== undefined) {
    window.clearTimeout(startupHideTimer);
    startupHideTimer = undefined;
  }
  if (startupPanelVisible.value) return;
  // 失败需要立即给出诊断与重试入口，不等延迟阈值。
  if (runtime.phase === "failed") {
    showStartupPanel();
    return;
  }
  if (startupShowTimer === undefined) {
    startupShowTimer = window.setTimeout(() => {
      startupShowTimer = undefined;
      // 触发时再次确认仍未就绪，避免 ready 与定时器竞态多显示一帧。
      if (startupPending.value) showStartupPanel();
    }, STARTUP_PANEL_DELAY_MS);
  }
}

watch(
  () => [runtime.gated, runtime.firstReady, runtime.phase],
  syncStartupPanel,
  { immediate: true },
);

onBeforeUnmount(clearStartupPanelTimers);

const showStartup = computed(() => startupPanelVisible.value);
const showUnavailableBanner = computed(
  () => runtime.gated && runtime.firstReady && !runtime.ready,
);
const bannerPhase = computed(() => runtimePhaseLabel(runtime.phase));
const bannerReason = computed(() =>
  runtime.reason ? runtimeReasonLabel(runtime.reason) : "",
);
const bannerAccessError = computed(() =>
  runtime.accessErrorKey ? t(runtime.accessErrorKey) : "",
);
const canRetry = computed(() => runtime.phase === "failed" && runtime.canRetry);

// Core 掉线进入通知中心（同一 dedupeKey 只保留最新，避免反复打扰）。
watch(
  () => caps.coreReady,
  (ready, was) => {
    if (was === true && !ready) {
      notifications.push({
        id: "system:core-down",
        dedupeKey: "system:core-down",
        kind: "system",
        tone: "warning",
        title: t("notify.coreDown.title"),
        body: t("notify.coreDown.body"),
      });
    }
  },
);

/** 首次/每次新连接就绪后刷新只读全局缓存并重建订阅；失联时不重放业务写。 */
function refreshBusiness(): void {
  void projects.load().catch(() => undefined);
  void caps.load().catch(() => undefined);
  void tasks.reconnect().catch(() => undefined);
}

let lastReadyEpoch = 0;

// 旧完整桥/演示/无桥：保持原有挂载即读取的行为。
onMounted(() => {
  if (runtime.gated) return;
  void projects.load().catch(() => undefined);
  void caps.load().catch(() => undefined);
  void tasks.load().catch(() => undefined);
  void tasks.startListening();
});

// runtime 桥：首次 ready 与后续新连接各刷新一次，同一 epoch 不重复。
watch(
  () => runtime.readyEpoch,
  (epoch) => {
    if (!runtime.gated || !epoch || epoch === lastReadyEpoch) return;
    lastReadyEpoch = epoch;
    refreshBusiness();
  },
  { immediate: true },
);

// 宿主进入非 ready 立即撤销“Core ready”状态（banner/状态栏一致）；
// 不清理业务缓存或编辑器草稿；重连后由 caps.load 的真实 system 读回再置 true。
// 旧桥/演示/无桥（gated=false）保持原有判定。
watch(
  () => runtime.ready,
  (ready) => {
    if (!runtime.gated) return;
    if (!ready) caps.markRuntimeUnavailable();
  },
  { immediate: true },
);
</script>

<template>
  <div class="app-root" :data-api-mode="apiMode">
    <p
      v-if="isBrowserPreview"
      class="app-mode-note app-mode-note--info"
      role="status"
      data-testid="browser-preview-banner"
    >
      <strong class="app-mode-note__title">{{ t("shell.browserPreview.title") }}</strong>
      <span>{{ t("shell.browserPreview.body") }}</span>
    </p>
    <p
      v-else-if="apiMode === 'unavailable'"
      class="app-mode-note app-mode-note--blocked"
      role="alert"
      data-testid="bridge-unavailable-banner"
    >
      <strong class="app-mode-note__title">{{ t("shell.bridgeUnavailable.title") }}</strong>
      <span>{{ t("shell.bridgeUnavailable.body") }}</span>
    </p>
    <p
      v-if="showUnavailableBanner"
      class="app-mode-note app-mode-note--blocked"
      role="status"
      data-testid="runtime-unavailable-banner"
    >
      <strong class="app-mode-note__title">{{ t("runtime.banner.title") }}</strong>
      <span>{{ t("runtime.banner.body", { phase: bannerPhase }) }}</span>
      <span v-if="bannerReason">{{ t("runtime.banner.reason", { reason: bannerReason }) }}</span>
      <span v-if="bannerAccessError" role="alert"><bdi>{{ bannerAccessError }}</bdi></span>
      <YmButton
        v-if="canRetry"
        size="sm"
        variant="secondary"
        icon="refresh"
        data-testid="runtime-banner-retry"
        :loading="runtime.retrying"
        :disabled="runtime.retrying"
        @click="runtime.retry()"
      >
        {{ t("runtime.retry") }}
      </YmButton>
    </p>
    <div class="app-root__body">
      <RuntimeInitialization v-if="showStartup" />
      <!-- 初始化页延迟期间保持空白；业务页仍由首次就绪门控，不能提前挂载。 -->
      <template v-else-if="!startupPending">
        <AppShell v-if="!isOnboarding" />
        <RouterView v-else />
      </template>
    </div>
  </div>
</template>

<style scoped>
.app-root {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--color-bg);
}
/* 提示条占用真实布局高度：不隐藏或覆盖内容让布局测试“通过”。 */
.app-mode-note {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--space-2);
  flex: none;
  margin: 0;
  padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--color-border);
  font-size: var(--font-size-xs);
  line-height: var(--line-height-normal);
}
.app-mode-note--blocked {
  background: var(--color-error-soft);
  color: var(--color-text);
}
.app-mode-note--info {
  background: color-mix(in srgb, var(--color-primary) 6%, var(--color-surface));
  color: var(--color-text-muted);
}
.app-mode-note__title {
  font-weight: 600;
  color: var(--color-info-strong);
}
.app-mode-note--blocked .app-mode-note__title {
  color: var(--color-error-strong);
}
.app-root__body {
  flex: 1;
  min-height: 0;
}
</style>

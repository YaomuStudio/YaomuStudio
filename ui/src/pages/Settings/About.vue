<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import pkg from "../../../package.json";
import { t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import { useUpdater } from "@/composables/useUpdater";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useSettingsStore } from "@/stores/settings";

const settings = useSettingsStore();
const caps = useCapabilitiesStore();

const {
  phase,
  version: updateVersion,
  releaseNotes,
  progress,
  installState,
  installBridgeAvailable,
  installActionError,
  installBusy,
  installWaiting,
  canCancelInstall,
  check,
  download,
  install,
  cancelInstall,
} = useUpdater();

const appVersion = pkg.version;
const platform = window.yaomu?.platform ?? navigator.platform;
const updateSupported = Boolean(window.yaomu?.update);
const autoDownloadSupported =
  typeof window.yaomu?.api?.setAutoDownloadUpdates === "function";
const diagnosticsExported = ref(false);

const phaseText = computed(() => {
  switch (phase.value) {
    case "checking":
      return t("settings.about.phase.checking");
    case "available":
      return t("settings.about.phase.available", { version: updateVersion.value });
    case "downloading":
      return t("settings.about.phase.downloading", {
        version: updateVersion.value,
        percent: progress.value,
      });
    case "downloaded":
      return t("settings.about.phase.downloaded", { version: updateVersion.value });
    case "error":
      return t("settings.about.phase.error");
    default:
      return t("settings.about.phase.idle");
  }
});

/** 有限宿主安装状态；只映射已注册文案，不显示任意异常文本。 */
const installStatusText = computed(() => {
  const state = installState.value;
  if (!state || state.phase === "idle") return "";
  const version = state.version ?? "";
  switch (state.phase) {
    case "waiting":
      switch (state.reason) {
        case "active_work":
          return t("settings.about.install.waitingActive", { version });
        case "request_in_flight":
          return t("settings.about.install.waitingRequests", { version });
        case "executor_starting":
          return t("settings.about.install.waitingExecutor", { version });
        default:
          return t("settings.about.install.waiting", { version });
      }
    case "preparing":
      return t("settings.about.install.preparing", { version });
    case "stopping":
      return t("settings.about.install.stopping");
    case "installing":
      return t("settings.about.install.installing", { version });
    case "blocked":
      switch (state.reason) {
        case "runtime_unavailable":
          return t("settings.about.install.blocked.runtimeUnavailable");
        case "runtime_changed":
          return t("settings.about.install.blocked.runtimeChanged");
        case "executor_unconfirmed":
          return t("settings.about.install.blocked.executorUnconfirmed");
        case "prepare_failed":
          return t("settings.about.install.blocked.prepareFailed");
        case "stop_unconfirmed":
          return t("settings.about.install.blocked.stopUnconfirmed");
        case "restart_required":
          return t("settings.about.install.blocked.restartRequired");
        case "update_unavailable":
          return t("settings.about.install.blocked.updateUnavailable");
        case "install_failed":
          return t("settings.about.install.blocked.installFailed");
        default:
          return t("settings.about.install.blocked.prepareFailed");
      }
    default:
      return "";
  }
});

const installStatusVisible = computed(() => installStatusText.value.length > 0);

const installCommandErrorText = computed(() => {
  switch (installActionError.value) {
    case "unavailable":
      return t("settings.about.install.unsupported");
    case "denied":
      return t("settings.about.install.denied");
    case "failed":
      return t("settings.about.install.failed");
    default:
      return "";
  }
});

const showInstallRestartHint = computed(() => {
  const state = installState.value;
  if (!state || state.phase !== "blocked") return false;
  return (
    state.reason === "restart_required" ||
    state.reason === "stop_unconfirmed" ||
    state.reason === "prepare_failed" ||
    state.reason === "executor_unconfirmed" ||
    state.reason === "install_failed"
  );
});

const installRetryable = computed(() => {
  const state = installState.value;
  if (!state || state.phase === "idle") return true;
  if (state.phase === "blocked") {
    return (
      state.reason === "update_unavailable" ||
      state.reason === "runtime_unavailable" ||
      state.reason === "runtime_changed"
    );
  }
  return false;
});

const installDisabled = computed(
  () =>
    !updateSupported ||
    !installBridgeAvailable.value ||
    installBusy.value ||
    installState.value?.phase === "waiting" ||
    !installRetryable.value,
);

const checkDisabled = computed(
  () =>
    !updateSupported ||
    phase.value === "downloading" ||
    installBusy.value ||
    installWaiting.value,
);

function exportDiagnostics(): void {
  const report = {
    app: "YaomuStudio",
    version: appVersion,
    platform,
    locale: settings.locale,
    theme: settings.theme,
    uiScale: settings.uiScale,
    coreReady: caps.coreReady,
    capabilities: caps.slots.map((slot) => ({
      slot: slot.slot,
      state: slot.state,
      engine: slot.engineName ?? null,
      version: slot.version ?? null,
    })),
    storage: caps.storage,
    generatedAt: new Date().toISOString(),
    excluded: [
      "media content",
      "subtitle text",
      "voiceprints",
      "tokens",
      "file paths",
      "environment variables",
    ],
  };
  const blob = new Blob([JSON.stringify(report, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "yaomu-diagnostics.json";
  anchor.click();
  URL.revokeObjectURL(url);
  diagnosticsExported.value = true;
}

onMounted(() => {
  if (!caps.slots.length) void caps.load();
});
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.about.title')"
      :description="t('settings.about.desc')"
      icon="info"
    />

    <YmCard>
      <div class="info">
        <div class="info__row">
          <p class="info__label">{{ t("settings.about.version") }}</p>
          <p class="info__value mono">v{{ appVersion }}</p>
        </div>
        <div class="info__row">
          <p class="info__label">{{ t("settings.about.platform") }}</p>
          <p class="info__value mono">{{ platform }}</p>
        </div>
        <div class="info__row">
          <p class="info__label">{{ t("settings.about.repo") }}</p>
          <p class="info__value info__value--muted">{{ t("settings.about.repoPending") }}</p>
        </div>
        <div class="info__row">
          <p class="info__label">{{ t("settings.about.license") }}</p>
          <div class="licenses">
            <div class="license">
              <div class="license__head">
                <span>{{ t("settings.about.licenseClient") }}</span>
                <YmBadge tone="neutral">{{ t("settings.about.licenseClientValue") }}</YmBadge>
              </div>
            </div>
            <div class="license">
              <div class="license__head">
                <span>{{ t("settings.about.licenseModels") }}</span>
                <YmBadge tone="neutral">{{ t("settings.about.licenseModelsBadge") }}</YmBadge>
              </div>
              <p class="license__body">{{ t("settings.about.licenseModelsValue") }}</p>
            </div>
          </div>
        </div>
        <p class="copyright">{{ t("settings.about.copyright") }}</p>
      </div>
    </YmCard>

    <YmCard>
      <div class="updates">
        <h2 class="section-title">{{ t("settings.about.updates") }}</h2>
        <div class="switch-row">
          <div class="switch-row__text">
            <p class="switch-row__label">{{ t("settings.about.autoCheck") }}</p>
            <p class="switch-row__hint">{{ t("settings.about.autoCheckHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.about.autoCheck')"
            :disabled="!updateSupported"
            :model-value="settings.autoCheckUpdates"
            @update:model-value="settings.autoCheckUpdates = $event"
          />
        </div>
        <div class="switch-row">
          <div class="switch-row__text">
            <p class="switch-row__label">{{ t("settings.about.autoDownload") }}</p>
            <p class="switch-row__hint">{{ t("settings.about.autoDownloadHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.about.autoDownload')"
            :disabled="!updateSupported || !autoDownloadSupported"
            :model-value="settings.autoDownloadUpdates"
            @update:model-value="settings.autoDownloadUpdates = $event"
          />
        </div>
        <p v-if="updateSupported && !autoDownloadSupported" class="note">
          {{ t("settings.about.autoDownloadOutdated") }}
        </p>
        <p class="status-line">{{ phaseText }}</p>
        <p
          v-if="installStatusVisible"
          class="status-line status-line--install"
          data-testid="update-install-status"
        >
          {{ installStatusText }}
        </p>
        <p
          v-if="installCommandErrorText"
          class="note note--error"
          role="alert"
          data-testid="update-install-error"
        >
          {{ installCommandErrorText }}
        </p>
        <p
          v-if="updateSupported && !installBridgeAvailable"
          class="note"
          data-testid="update-install-bridge-outdated"
        >
          {{ t("settings.about.install.bridgeOutdated") }}
        </p>
        <p
          v-if="showInstallRestartHint"
          class="note"
          data-testid="update-install-restart"
        >
          {{ t("settings.about.install.restartHint") }}
        </p>
        <pre v-if="phase === 'available' && releaseNotes" class="release-notes">{{ releaseNotes }}</pre>
        <p v-if="!updateSupported" class="note">{{ t("settings.about.bridgeUnavailable") }}</p>

        <div class="actions">
          <YmButton
            variant="primary"
            icon="refresh"
            :loading="phase === 'checking'"
            :disabled="checkDisabled"
            data-testid="update-check"
            @click="check"
          >
            {{ t("settings.about.check") }}
          </YmButton>
          <YmButton
            v-if="phase === 'available'"
            variant="secondary"
            icon="download"
            data-testid="update-download"
            :disabled="installBusy"
            @click="download"
          >
            {{ t("settings.about.download") }}
          </YmButton>
          <YmButton
            v-if="phase === 'downloaded'"
            variant="primary"
            icon="refresh"
            data-testid="update-install"
            :disabled="installDisabled"
            @click="install"
          >
            {{ t("settings.about.install") }}
          </YmButton>
          <YmButton
            v-if="canCancelInstall"
            variant="secondary"
            icon="x"
            data-testid="update-install-cancel"
            @click="cancelInstall"
          >
            {{ t("settings.about.install.cancel") }}
          </YmButton>
        </div>
      </div>
    </YmCard>

    <YmCard>
      <div class="diagnostics">
        <h2 class="section-title">{{ t("settings.about.diagnostics") }}</h2>
        <p class="note">{{ t("settings.about.diagnosticsBody") }}</p>
        <div class="actions">
          <YmButton variant="secondary" icon="download" @click="exportDiagnostics">
            {{ t("settings.about.export") }}
          </YmButton>
          <span v-if="diagnosticsExported" class="exported" role="status">
            {{ t("settings.about.exported") }}
          </span>
        </div>
      </div>
    </YmCard>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 1240px;
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__desc {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.info {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.info__row {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr);
  gap: var(--space-4);
  align-items: start;
}
.info__label {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.info__value {
  font-size: var(--font-size-md);
  word-break: break-word;
}
.info__value--muted {
  color: var(--color-text-faint);
  font-size: var(--font-size-sm);
}
.licenses {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.license {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.license__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  font-size: var(--font-size-sm);
  font-weight: 500;
}
.license__body {
  margin-top: var(--space-1);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.copyright {
  padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.updates,
.diagnostics {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.section-title {
  font-size: var(--font-size-lg);
}
.switch-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
  padding-top: var(--space-1);
}
.switch-row__text {
  min-width: 0;
}
.switch-row__label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.switch-row__hint {
  margin-top: var(--space-1);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.status-line {
  font-size: var(--font-size-md);
  color: var(--color-text-muted);
}
.status-line--install {
  color: var(--color-text);
}
.note--error {
  color: var(--color-error-strong);
}
.release-notes {
  margin: 0;
  padding: var(--space-3);
  max-height: 140px;
  overflow: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg);
  font-size: var(--font-size-xs);
  white-space: pre-wrap;
}
.note {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.exported {
  font-size: var(--font-size-xs);
  color: var(--color-success-strong);
}
</style>

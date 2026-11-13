<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { formatBytes, t } from "@/i18n";
import { api } from "@/api";
import { errorNotice, messageNotice, renderNotice, type SafeNotice } from "@/i18n/notice";
import { MODEL_SLOTS, CAPABILITY_SLOTS } from "@/domain/capabilities";
import type { DeviceInfo } from "@/domain/types";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmEmpty from "@/components/ui/YmEmpty.vue";
import YmField from "@/components/ui/YmField.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import YmRadioGroup from "@/components/ui/YmRadioGroup.vue";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useSettingsStore } from "@/stores/settings";

const KIND_KEYS: Record<DeviceInfo["kind"], string> = {
  cpu: "settings.devices.kind.cpu",
  cuda: "settings.devices.kind.cuda",
  mps: "settings.devices.kind.mps",
};

const caps = useCapabilitiesStore();
const settings = useSettingsStore();
const pending = ref<string | null>(null);
const loading = ref(false);
const saving = ref(false);
const actionError = ref<SafeNotice | null>(null);
const actionErrorText = computed(() => renderNotice(actionError.value));
const notice = ref<SafeNotice | null>(null);
const noticeText = computed(() => renderNotice(notice.value));

const kindLabel = (kind: DeviceInfo["kind"]): string => t(KIND_KEYS[kind]);

const deviceOptions = computed(() => [
  { value: "auto", label: t("settings.devices.auto") },
  ...caps.devices.map((d) => {
    const kind = kindLabel(d.kind);
    const parts = [d.name];
    if (!d.name.toLowerCase().includes(kind.toLowerCase())) parts.push(kind);
    if (d.recommended) parts.push(t("settings.devices.recommended"));
    return { value: d.id, label: parts.join(" · ") };
  }),
]);

function nameIncludesKind(device: DeviceInfo): boolean {
  return device.name.toLowerCase().includes(kindLabel(device.kind).toLowerCase());
}

const currentDevice = computed<DeviceInfo | undefined>(() => {
  if (settings.device === "auto") {
    return caps.devices.find((d) => d.recommended) ?? caps.devices[0];
  }
  return caps.devices.find((d) => d.id === settings.device) ?? caps.devices[0];
});

const currentLabel = computed(() => {
  const device = currentDevice.value;
  if (!device) return t("common.none");
  if (settings.device === "auto") {
    return t("settings.devices.currentAuto", { name: device.name });
  }
  const kind = kindLabel(device.kind);
  if (device.name.toLowerCase().includes(kind.toLowerCase())) return device.name;
  return t("settings.devices.currentValue", {
    name: device.name,
    kind,
  });
});

const modelSlotRows = computed(() =>
  CAPABILITY_SLOTS.filter((slot) => slot.isModelSlot).map((slot) => {
    const capability = caps.slots.find((item) => item.slot === slot.key);
    const running = capability?.runtimeState === "running";
    return {
      key: slot.key,
      label: t(slot.nameKey),
      running,
      runningTasks: running ? (capability?.runningTasks ?? 0) : 0,
    };
  }),
);

const runningSlots = computed(() => modelSlotRows.value.filter((row) => row.running).length);

/** 设备级内存/显存展示；任一数值缺失时保留 —，不伪造 0。 */
function memoryPair(device?: DeviceInfo): string {
  if (!device) return "—";
  if (device.memoryUsedBytes === undefined && device.memoryTotalBytes === undefined) {
    return "—";
  }
  const used =
    device.memoryUsedBytes !== undefined ? formatBytes(device.memoryUsedBytes) : "—";
  const total =
    device.memoryTotalBytes !== undefined ? formatBytes(device.memoryTotalBytes) : "—";
  return `${used} / ${total}`;
}

const pendingLabel = computed(() => {
  if (pending.value === null) return "";
  if (pending.value === "auto") return t("settings.devices.auto");
  const device = caps.devices.find((d) => d.id === pending.value);
  return device ? `${device.name} · ${kindLabel(device.kind)}` : pending.value;
});

function selectDevice(value: string): void {
  if (value === settings.device) return;
  pending.value = value;
}

/** 设备选择通过各模型槽位的受控配置端点提交，只对后续任务生效。 */
async function confirmSwitch(): Promise<void> {
  const value = pending.value;
  pending.value = null;
  if (value === null || value === settings.device) return;
  saving.value = true;
  actionError.value = null;
  notice.value = null;
  const previous = settings.device;
  const results = await Promise.allSettled(
    MODEL_SLOTS.map((slot) => api.setEngineConfig(slot, { deviceId: value })),
  );
  const failed = results.find(
    (result): result is PromiseRejectedResult => result.status === "rejected",
  );
  if (failed) {
    // 部分槽位可能已切换：尽量回滚，避免六个槽位停在混合设备上。
    const switched = MODEL_SLOTS.filter((_, index) => results[index].status === "fulfilled");
    await Promise.allSettled(
      switched.map((slot) => api.setEngineConfig(slot, { deviceId: previous })),
    );
    actionError.value = errorNotice(
      failed.reason,
      "settings.devices.switchFailed",
    );
    saving.value = false;
    return;
  }
  try {
    settings.device = value;
    await caps.load();
    notice.value = messageNotice("settings.devices.switched");
  } catch (err) {
    actionError.value = errorNotice(err, "settings.devices.switchFailed");
  } finally {
    saving.value = false;
  }
}

async function refresh(): Promise<void> {
  loading.value = true;
  try {
    await caps.load();
  } finally {
    loading.value = false;
  }
}

onMounted(() => {
  // 每次进入都刷新快照：能力目录在启动时已加载，槽位运行状态可能已经变化。
  // 服务端设备探测仍受 TTL 约束，不会因路由进入反复执行外部命令。
  void refresh().catch(() => {
    // Core 未就绪时由全局状态与错误横幅表达，页面保留上一份快照。
  });
});
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.devices.title')"
      :description="t('settings.devices.desc')"
      icon="cpu"
    />

    <YmNotice v-if="actionErrorText" tone="error"><bdi>{{ actionErrorText }}</bdi></YmNotice>
    <YmNotice v-if="noticeText" tone="success"><bdi>{{ noticeText }}</bdi></YmNotice>

    <YmCard>
      <div class="stack">
        <YmField
          :label="t('settings.devices.selection')"
          :hint="t('settings.devices.autoHint')"
        >
          <YmRadioGroup
            :model-value="settings.device"
            :options="deviceOptions"
            @update:model-value="selectDevice"
          />
        </YmField>

        <div class="summary">
          <div class="summary__item">
            <p class="summary__label">{{ t("settings.devices.current") }}</p>
            <p class="summary__value">{{ currentLabel }}</p>
          </div>
          <div class="summary__item">
            <p class="summary__label">{{ t("settings.devices.loadStatus") }}</p>
            <p class="summary__value">
              {{ t("settings.devices.loadValue", { running: runningSlots, total: MODEL_SLOTS.length }) }}
            </p>
            <p class="summary__hint">{{ t("settings.devices.loadHint") }}</p>
          </div>
          <div class="summary__item">
            <p class="summary__label">{{ t("settings.devices.usage") }}</p>
            <p class="summary__value mono">{{ memoryPair(currentDevice) }}</p>
            <p class="summary__hint">{{ t("settings.devices.usageHint") }}</p>
          </div>
        </div>

        <div class="list-head">
          <h2 class="section-title">{{ t("settings.devices.deviceList") }}</h2>
          <YmButton
            size="sm"
            variant="ghost"
            icon="refresh"
            :loading="loading"
            @click="refresh"
          >
            {{ t("settings.devices.refresh") }}
          </YmButton>
        </div>

        <YmEmpty v-if="!caps.devices.length" :title="t('settings.devices.noDevices')" icon="cpu" />
        <div v-else class="device-list">
          <div v-for="d in caps.devices" :key="d.id" class="device">
            <div class="device__main">
              <div class="device__name">
                <span><bdi dir="auto">{{ d.name }}</bdi></span>
                <YmBadge v-if="!nameIncludesKind(d)" tone="neutral">
                  {{ kindLabel(d.kind) }}
                </YmBadge>
                <YmBadge v-if="d.recommended" tone="accent">
                  {{ t("settings.devices.recommended") }}
                </YmBadge>
                <YmBadge v-if="d.inUse" tone="success">{{ t("settings.devices.inUse") }}</YmBadge>
              </div>
              <p class="device__meta mono">
                {{ t("settings.devices.vram") }}: {{ memoryPair(d) }}
                <template v-if="d.driverVersion">
                  · {{ t("settings.devices.driver") }}: {{ d.driverVersion }}
                </template>
              </p>
            </div>
          </div>
        </div>

        <div class="list-head">
          <h2 class="section-title">{{ t("settings.devices.runtimeTitle") }}</h2>
        </div>
        <div class="slot-list">
          <div v-for="row in modelSlotRows" :key="row.key" class="slot-row">
            <span class="slot-row__name">{{ row.label }}</span>
            <YmBadge :tone="row.running ? 'success' : 'neutral'">
              {{ row.running ? t("settings.devices.runtimeRunning") : t("settings.devices.runtimeIdle") }}
            </YmBadge>
            <span v-if="row.running" class="slot-row__count mono">
              {{ t("settings.devices.runtimeTaskCount", { count: row.runningTasks }) }}
            </span>
          </div>
        </div>
        <p class="summary__hint">{{ t("settings.devices.runtimeHint") }}</p>
      </div>
    </YmCard>

    <YmDialog
      :model-value="pending !== null"
      :title="t('settings.devices.switchTitle')"
      :confirm-text="t('settings.devices.switchConfirm')"
      :cancel-text="t('common.cancel')"
      @update:model-value="pending = null"
      @confirm="confirmSwitch"
    >
      <p>{{ t("settings.devices.switchBody") }}</p>
      <p v-if="pendingLabel" class="dialog-target mono">{{ pendingLabel }}</p>
    </YmDialog>
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
.stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
.summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.summary__item {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.summary__label {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.summary__value {
  margin-top: 2px;
  font-size: var(--font-size-md);
  font-weight: 500;
}
.summary__hint {
  margin-top: var(--space-1);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
}
.section-title {
  font-size: var(--font-size-md);
  font-weight: 600;
}
.device-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.device {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.device__name {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  font-weight: 500;
}
.device__meta {
  margin-top: var(--space-1);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.slot-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.slot-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.slot-row__name {
  flex: 1;
  font-size: var(--font-size-sm);
}
.slot-row__count {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.dialog-target {
  margin-top: var(--space-2);
  font-size: var(--font-size-sm);
  color: var(--color-text);
}
</style>

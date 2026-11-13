<script setup lang="ts">
import { computed } from "vue";
import { t, formatBytes } from "@/i18n";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useTasksStore } from "@/stores/tasks";

const caps = useCapabilitiesStore();
const tasks = useTasksStore();

const syncLabel = computed(() => {
  if (!tasks.syncError) return "";
  return tasks.syncError === "EVENT_CURSOR_EXPIRED"
    ? t("statusbar.syncCursorExpired")
    : t("statusbar.syncStopped");
});

// Core 在 /devices 里用 selected 标注当前生效设备（含 auto 解析结果），
// recommended 仅表示硬件推荐的默认项，不能代表用户选择。
const deviceName = computed(() => {
  const active = caps.devices.find((d) => d.selected);
  const fallback = caps.devices.find((d) => d.recommended) ?? caps.devices[0];
  return (active ?? fallback)?.name ?? "—";
});

const usedBytes = computed(() => {
  const s = caps.storage;
  if (!s) return 0;
  return s.models + s.media + s.cache + s.exports;
});
</script>

<template>
  <footer class="statusbar">
    <span class="statusbar__item" :title="caps.errorKey ? t(caps.errorKey) : undefined">
      <span class="statusbar__dot" :class="{ 'is-ready': caps.coreReady }" aria-hidden="true"></span>
      {{ caps.coreReady ? t("statusbar.coreReady") : caps.errorKey ? t(caps.errorKey) : t("statusbar.coreDown") }}
    </span>
    <span
      v-if="syncLabel"
      class="statusbar__item statusbar__item--warn"
      :title="t('statusbar.syncStoppedHint')"
    >
      {{ syncLabel }}
    </span>
    <span class="statusbar__item">
      <span class="statusbar__k">{{ t("statusbar.device") }}</span>
      <bdi dir="auto">{{ deviceName }}</bdi>
    </span>
    <span class="statusbar__item">
      <span class="statusbar__k">{{ t("statusbar.disk") }}</span>
      <bdi dir="ltr">
        {{ caps.storage ? `${formatBytes(usedBytes)} ${t("settings.storageUsed")}` : "—" }}
      </bdi>
    </span>
  </footer>
</template>

<style scoped>
.statusbar {
  display: flex;
  align-items: center;
  gap: var(--space-5);
  height: var(--statusbar-height);
  padding: 0 var(--space-4);
  background: var(--color-surface);
  border-top: 1px solid var(--color-border);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
  flex: none;
}
.statusbar__item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.statusbar__k {
  color: var(--color-text-faint);
}
.statusbar__dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--color-error);
}
.statusbar__dot.is-ready {
  background: var(--color-accent);
}
.statusbar__item--warn {
  color: var(--color-warning-strong);
}
</style>

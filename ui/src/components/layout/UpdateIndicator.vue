<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import YmIcon from "@/components/ui/YmIcon.vue";
import { useUpdater } from "@/composables/useUpdater";

const { phase, version, progress, download, install } = useUpdater();

const visible = computed(
  () =>
    phase.value === "available" ||
    phase.value === "downloading" ||
    phase.value === "downloaded",
);

const label = computed(() => {
  if (phase.value === "downloaded") {
    return t("update.indicator.downloaded", { version: version.value });
  }
  if (phase.value === "downloading") {
    return t("update.indicator.downloading", {
      version: version.value,
      percent: progress.value,
    });
  }
  return t("update.indicator.available", { version: version.value });
});

const icon = computed(() => (phase.value === "downloaded" ? "refresh" : "download"));

/** 点击即执行：有更新则下载，已下载则进入安装；下载中不重复触发。 */
function activate(): void {
  if (phase.value === "available") {
    download();
  } else if (phase.value === "downloaded") {
    void install();
  }
}
</script>

<template>
  <button
    v-if="visible"
    type="button"
    class="update-indicator"
    :class="{
      'is-available': phase === 'available',
      'is-ready': phase === 'downloaded',
      'is-downloading': phase === 'downloading',
    }"
    :aria-label="label"
    :title="label"
    :aria-busy="phase === 'downloading'"
    data-testid="update-indicator"
    @click="activate"
  >
    <YmIcon :name="icon" :size="16" />
    <span v-if="phase === 'downloaded'" class="update-indicator__dot" aria-hidden="true" />
  </button>
</template>

<style scoped>
.update-indicator {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  transition:
    background var(--motion-duration-snap) var(--motion-ease-out),
    color var(--motion-duration-snap) var(--motion-ease-out);
}
.update-indicator:hover {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.update-indicator.is-available,
.update-indicator.is-ready {
  color: var(--color-primary);
}
.update-indicator.is-downloading {
  cursor: default;
}
.update-indicator.is-downloading :deep(svg) {
  animation: update-indicator-pulse 1.4s var(--motion-ease-out) infinite;
}
.update-indicator__dot {
  position: absolute;
  top: 5px;
  right: 5px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--color-primary);
  box-shadow: 0 0 0 2px var(--color-surface);
}
@keyframes update-indicator-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.45;
  }
}
@media (prefers-reduced-motion: reduce) {
  .update-indicator.is-downloading :deep(svg) {
    animation: none;
  }
}
</style>

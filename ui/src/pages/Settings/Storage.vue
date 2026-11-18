<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { formatBytes, formatPercent, t } from "@/i18n";
import { api } from "@/api";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
import YmNotice from "@/components/ui/YmNotice.vue";
import type { Tone } from "@/domain/status";
import { useCapabilitiesStore } from "@/stores/capabilities";

interface StorageCategory {
  key: string;
  labelKey: string;
  icon: string;
  bytes: number;
  tone: Tone;
}

const caps = useCapabilitiesStore();
const desktopApi = typeof window !== "undefined" ? window.yaomu?.api : undefined;
const isWindows = typeof window !== "undefined" && window.yaomu?.platform === "win32";
const loading = ref(false);
const cleanTarget = ref<StorageCategory | null>(null);
const runtimeCleanOpen = ref(false);
const runtimeCleaning = ref(false);
const runtimeNotice = ref("");
const runtimeError = ref("");
const cleanBusy = ref(false);
const cleanNotice = ref("");
const cleanError = ref("");

const categories = computed<StorageCategory[]>(() => {
  const usage = caps.storage;
  return [
    {
      key: "models",
      labelKey: "settings.storage.models",
      icon: "box",
      bytes: usage?.models ?? 0,
      tone: "accent",
    },
    {
      key: "media",
      labelKey: "settings.storage.media",
      icon: "film",
      bytes: usage?.media ?? 0,
      tone: "info",
    },
    {
      key: "cache",
      labelKey: "settings.storage.cache",
      icon: "layers",
      bytes: usage?.cache ?? 0,
      tone: "warning",
    },
    {
      key: "exports",
      labelKey: "settings.storage.exports",
      icon: "folder-open",
      bytes: usage?.exports ?? 0,
      tone: "neutral",
    },
  ];
});

const total = computed(() => categories.value.reduce((acc, c) => acc + c.bytes, 0));

function share(bytes: number): number {
  return total.value > 0 ? (bytes / total.value) * 100 : 0;
}

async function cleanRuntime(): Promise<void> {
  if (!desktopApi?.removeWslRuntime || runtimeCleaning.value) return;
  runtimeCleaning.value = true;
  runtimeError.value = "";
  runtimeNotice.value = "";
  try {
    const result = await desktopApi.removeWslRuntime();
    if (!result.ok) {
      runtimeError.value = t(result.error?.messageKey ?? "settings.storage.runtimeCleanupFailed");
      return;
    }
    runtimeCleanOpen.value = false;
    runtimeNotice.value = t(
      result.removed ? "settings.storage.runtimeRemoved" : "settings.storage.runtimeNotInstalled",
    );
  } catch {
    runtimeError.value = t("settings.storage.runtimeCleanupFailed");
  } finally {
    runtimeCleaning.value = false;
  }
}

async function cleanCategory(): Promise<void> {
  if (!cleanTarget.value || cleanBusy.value) return;
  const category = cleanTarget.value.key;
  if (category !== "cache" && category !== "models") return;
  cleanBusy.value = true;
  cleanNotice.value = "";
  cleanError.value = "";
  try {
    const result = await api.cleanupStorage("cache");
    cleanNotice.value = t("settings.storage.cleanDone", {
      bytes: formatBytes(result.freedBytes),
    });
    cleanTarget.value = null;
    await refresh();
  } catch {
    cleanError.value = t("settings.storage.cleanFailed");
  } finally {
    cleanBusy.value = false;
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
  if (!caps.storage) void refresh();
});
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.storage.title')"
      :description="t('settings.storage.desc')"
      icon="disk"
    />

    <YmNotice v-if="cleanNotice" tone="success">{{ cleanNotice }}</YmNotice>
    <YmNotice v-if="cleanError" tone="error">{{ cleanError }}</YmNotice>
    <YmNotice v-if="runtimeNotice" tone="success">{{ runtimeNotice }}</YmNotice>
    <YmNotice v-if="runtimeError" tone="error">{{ runtimeError }}</YmNotice>

    <YmCard>
      <div class="usage">
        <div class="usage__head">
          <div>
            <p class="usage__label">{{ t("settings.storage.total") }}</p>
            <p class="usage__total mono"><bdi dir="ltr">{{ formatBytes(total) }}</bdi></p>
          </div>
          <YmButton
            size="sm"
            variant="ghost"
            icon="refresh"
            :loading="loading"
            @click="refresh"
          >
            {{ t("settings.storage.refresh") }}
          </YmButton>
        </div>

        <div class="usage__list">
          <div v-for="c in categories" :key="c.key" class="usage__row">
            <div class="usage__row-head">
              <div class="usage__name">
                <YmIcon :name="c.icon" :size="16" />
                <span>{{ t(c.labelKey) }}</span>
                <span class="usage__share mono">
                  {{ t("settings.storage.share", { percent: formatPercent(share(c.bytes)) }) }}
                </span>
              </div>
              <div class="usage__row-end">
                <span class="usage__bytes mono"><bdi dir="ltr">{{ formatBytes(c.bytes) }}</bdi></span>
                <YmButton
                  v-if="c.key === 'cache' || c.key === 'models'"
                  size="sm"
                  variant="ghost"
                  icon="trash"
                  @click="cleanTarget = c"
                >
                  {{ t("settings.storage.clean") }}
                </YmButton>
              </div>
            </div>
            <YmProgress :value="share(c.bytes)" :tone="c.tone" />
          </div>
        </div>

        <p class="hint">{{ t("settings.storage.diskHint") }}</p>
      </div>
    </YmCard>

    <YmCard v-if="isWindows && desktopApi?.removeWslRuntime">
      <template #header>
        <div class="usage__head">
          <div>
            <p class="usage__label">{{ t("settings.storage.advancedTitle") }}</p>
            <p class="hint">{{ t("settings.storage.advancedDesc") }}</p>
          </div>
        </div>
      </template>
      <div class="usage__row">
        <div class="usage__row-head">
          <div class="usage__name">
            <YmIcon name="layers" :size="16" />
            <span>{{ t("settings.storage.runtimeTitle") }}</span>
          </div>
          <div class="usage__row-end">
            <YmButton size="sm" variant="secondary" icon="trash" @click="runtimeCleanOpen = true">
              {{ t("settings.storage.runtimeClean") }}
            </YmButton>
          </div>
        </div>
        <p class="hint">{{ t("settings.storage.runtimeDesc") }}</p>
      </div>
    </YmCard>

    <YmDialog
      :model-value="runtimeCleanOpen"
      :title="t('settings.storage.runtimeConfirmTitle')"
      @update:model-value="runtimeCleanOpen = $event"
    >
      <p>{{ t("settings.storage.runtimeConfirmBody") }}</p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.cancel") }}</YmButton>
        <YmButton
          variant="danger"
          :loading="runtimeCleaning"
          @click="cleanRuntime"
        >
          {{ t("settings.storage.runtimeConfirm") }}
        </YmButton>
      </template>
    </YmDialog>

    <YmDialog
      :model-value="cleanTarget !== null"
      :title="
        cleanTarget
          ? t('settings.storage.cleanTitle', { category: t(cleanTarget.labelKey) })
          : ''
      "
      @update:model-value="cleanTarget = null"
    >
      <p>{{ t("settings.storage.cleanBody") }}</p>
      <p v-if="cleanTarget" class="dialog-meta mono">
        {{ t(cleanTarget.labelKey) }} · <bdi dir="ltr">{{ formatBytes(cleanTarget.bytes) }}</bdi>
      </p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.cancel") }}</YmButton>
        <YmButton
          variant="danger"
          :loading="cleanBusy"
          @click="cleanCategory"
        >
          {{ t("settings.storage.clean") }}
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
  max-width: 1240px;
}
.page__title {
  font-size: var(--font-size-xl);
}
.page__desc {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
}
.usage {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.usage__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.usage__label {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.usage__total {
  margin-top: 2px;
  font-size: var(--font-size-xl);
  font-weight: 600;
}
.usage__list {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.usage__row {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.usage__row-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
}
.usage__name {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}
.usage__share {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.usage__row-end {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
}
.usage__bytes {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
  min-width: 72px;
  text-align: right;
}
.hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.dialog-meta {
  margin-top: var(--space-2);
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
</style>

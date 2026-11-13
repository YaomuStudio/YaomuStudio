<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { formatBytes, formatPercent, t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmProgress from "@/components/ui/YmProgress.vue";
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
const loading = ref(false);
const cleanTarget = ref<StorageCategory | null>(null);

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

    <YmDialog
      :model-value="cleanTarget !== null"
      :title="
        cleanTarget
          ? t('settings.storage.cleanTitle', { category: t(cleanTarget.labelKey) })
          : ''
      "
      :confirm-text="t('common.close')"
      @update:model-value="cleanTarget = null"
    >
      <p>{{ t("settings.storage.cleanBody") }}</p>
      <p v-if="cleanTarget" class="dialog-meta mono">
        {{ t(cleanTarget.labelKey) }} · <bdi dir="ltr">{{ formatBytes(cleanTarget.bytes) }}</bdi>
      </p>
      <template #footer="{ close }">
        <YmButton variant="secondary" @click="close">{{ t("common.close") }}</YmButton>
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

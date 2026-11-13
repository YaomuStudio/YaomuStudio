<script setup lang="ts">
import { t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import { useSettingsStore } from "@/stores/settings";

const settings = useSettingsStore();
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.notifications.title')"
      :description="t('settings.notifications.desc')"
      icon="bell"
    />

    <YmCard>
      <div class="rows">
        <div class="row">
          <div class="row__text">
            <p class="row__label">{{ t("settings.notifications.taskDone") }}</p>
            <p class="row__hint">{{ t("settings.notifications.taskDoneHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.notifications.taskDone')"
            :model-value="settings.notifications.taskDone"
            @update:model-value="settings.notifications.taskDone = $event"
          />
        </div>

        <div class="row">
          <div class="row__text">
            <p class="row__label">{{ t("settings.notifications.taskFailed") }}</p>
            <p class="row__hint">{{ t("settings.notifications.taskFailedHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.notifications.taskFailed')"
            :model-value="settings.notifications.taskFailed"
            @update:model-value="settings.notifications.taskFailed = $event"
          />
        </div>

        <div class="row">
          <div class="row__text">
            <p class="row__label">{{ t("settings.notifications.downloadDone") }}</p>
            <p class="row__hint">{{ t("settings.notifications.downloadDoneHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.notifications.downloadDone')"
            :model-value="settings.notifications.downloadDone"
            @update:model-value="settings.notifications.downloadDone = $event"
          />
        </div>

        <div class="row">
          <div class="row__text">
            <p class="row__label">{{ t("settings.notifications.updateAvailable") }}</p>
            <p class="row__hint">{{ t("settings.notifications.updateAvailableHint") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.notifications.updateAvailable')"
            :model-value="settings.notifications.updateAvailable"
            @update:model-value="settings.notifications.updateAvailable = $event"
          />
        </div>
      </div>
    </YmCard>

    <p class="note">{{ t("settings.notifications.permissionNote") }}</p>
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
.rows {
  display: flex;
  flex-direction: column;
}
.row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--color-border);
}
.row:first-child {
  padding-top: 0;
}
.row:last-child {
  padding-bottom: 0;
  border-bottom: none;
}
.row__text {
  min-width: 0;
}
.row__label {
  font-size: var(--font-size-md);
  font-weight: 500;
}
.row__hint {
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.note {
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
</style>

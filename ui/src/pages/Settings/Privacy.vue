<script setup lang="ts">
import { ref } from "vue";
import { t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmSwitch from "@/components/ui/YmSwitch.vue";
import { useSettingsStore } from "@/stores/settings";

const settings = useSettingsStore();
const telemetryDialog = ref(false);

function onTelemetry(value: boolean): void {
  if (value) {
    telemetryDialog.value = true;
  } else {
    settings.telemetry = false;
  }
}

function confirmTelemetry(): void {
  settings.telemetry = true;
  telemetryDialog.value = false;
}
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.privacy.title')"
      :description="t('settings.privacy.desc')"
      icon="shield"
    />

    <YmCard>
      <div class="statements">
        <div class="statement">
          <span class="statement__icon"><YmIcon name="shield" :size="18" /></span>
          <div>
            <p class="statement__title">{{ t("settings.privacy.localTitle") }}</p>
            <p class="statement__body">{{ t("settings.privacy.localBody") }}</p>
          </div>
        </div>
        <div class="statement">
          <span class="statement__icon"><YmIcon name="mic" :size="18" /></span>
          <div>
            <p class="statement__title">{{ t("settings.privacy.consentTitle") }}</p>
            <p class="statement__body">{{ t("settings.privacy.consentBody") }}</p>
          </div>
        </div>
      </div>
    </YmCard>

    <YmCard>
      <div class="telemetry">
        <div class="telemetry__head">
          <div class="telemetry__text">
            <div class="telemetry__title-row">
              <p class="statement__title">{{ t("settings.privacy.telemetryTitle") }}</p>
              <YmBadge :tone="settings.telemetry ? 'accent' : 'neutral'">
                {{
                  settings.telemetry
                    ? t("settings.privacy.telemetryStatusOn")
                    : t("settings.privacy.telemetryStatusOff")
                }}
              </YmBadge>
            </div>
            <p class="statement__body">{{ t("settings.privacy.telemetryBody") }}</p>
          </div>
          <YmSwitch
            :aria-label="t('settings.privacy.telemetryTitle')"
            :model-value="settings.telemetry"
            @update:model-value="onTelemetry"
          />
        </div>

        <div class="scope">
          <p class="scope__title">{{ t("settings.privacy.telemetryScopeTitle") }}</p>
          <ul class="scope__list">
            <li>{{ t("settings.privacy.telemetryScope1") }}</li>
            <li>{{ t("settings.privacy.telemetryScope2") }}</li>
            <li>{{ t("settings.privacy.telemetryScope3") }}</li>
          </ul>
        </div>
      </div>
    </YmCard>

    <YmCard>
      <div class="statement">
        <span class="statement__icon"><YmIcon name="list" :size="18" /></span>
        <div>
          <p class="statement__title">{{ t("settings.privacy.logsTitle") }}</p>
          <p class="statement__body">{{ t("settings.privacy.logsBody") }}</p>
        </div>
      </div>
    </YmCard>

    <YmDialog
      v-model="telemetryDialog"
      :title="t('settings.privacy.telemetryTitle')"
      :confirm-text="t('settings.privacy.telemetryEnable')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmTelemetry"
    >
      <p>{{ t("settings.privacy.telemetryBody") }}</p>
      <ul class="scope__list dialog-list">
        <li>{{ t("settings.privacy.telemetryScope1") }}</li>
        <li>{{ t("settings.privacy.telemetryScope2") }}</li>
        <li>{{ t("settings.privacy.telemetryScope3") }}</li>
      </ul>
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
.statements {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.statement {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
}
.statement__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  border-radius: var(--radius-md);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.statement__title {
  font-size: var(--font-size-md);
  font-weight: 600;
}
.statement__body {
  margin-top: 2px;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.telemetry {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.telemetry__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-5);
}
.telemetry__text {
  min-width: 0;
}
.telemetry__title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.scope {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.scope__title {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.scope__list {
  margin: var(--space-2) 0 0;
  padding-left: var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.dialog-list {
  margin-top: var(--space-3);
  color: var(--color-text-muted);
}
</style>

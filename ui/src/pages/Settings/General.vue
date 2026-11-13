<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import PageHeader from "@/components/layout/PageHeader.vue";
import YmCard from "@/components/ui/YmCard.vue";
import YmField from "@/components/ui/YmField.vue";
import YmRadioGroup from "@/components/ui/YmRadioGroup.vue";
import { useSettingsStore } from "@/stores/settings";

const settings = useSettingsStore();

const startupOptions = computed(() => [
  { value: "workbench", label: t("settings.general.startup.workbench") },
  { value: "lastProject", label: t("settings.general.startup.lastProject") },
]);

const closeOptions = computed(() => [
  { value: "quit", label: t("settings.general.close.quit") },
  { value: "minimize", label: t("settings.general.close.minimize") },
]);

function setStartup(value: string): void {
  if (value === "workbench" || value === "lastProject") settings.startup = value;
}

function setCloseBehavior(value: string): void {
  if (value === "quit" || value === "minimize") settings.closeBehavior = value;
}
</script>

<template>
  <div class="page">
    <PageHeader
      :title="t('settings.general.title')"
      :description="t('settings.general.desc')"
      icon="gear"
    />

    <YmCard>
      <div class="stack">
        <YmField :label="t('settings.general.startup')" :hint="t('settings.general.startupHint')">
          <YmRadioGroup
            :model-value="settings.startup"
            :options="startupOptions"
            @update:model-value="setStartup"
          />
        </YmField>

        <YmField :label="t('settings.general.close')" :hint="t('settings.general.closeHint')">
          <YmRadioGroup
            :model-value="settings.closeBehavior"
            :options="closeOptions"
            @update:model-value="setCloseBehavior"
          />
        </YmField>
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
.stack {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
</style>

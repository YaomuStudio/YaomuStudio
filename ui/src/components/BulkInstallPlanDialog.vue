<script setup lang="ts">
import { computed } from "vue";
import { formatBytes, t } from "@/i18n";
import { CAPABILITY_SLOTS, visibleModelSlots } from "@/domain/capabilities";
import { planCoverage } from "@/domain/installations";
import type { CapabilitySlotKey, InstallationPlan } from "@/domain/types";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    plans: InstallationPlan[];
    loading?: boolean;
  }>(),
  { loading: false },
);

const emit = defineEmits<{
  (e: "update:modelValue", v: boolean): void;
  (e: "confirm"): void;
}>();

function slotLabel(slot: CapabilitySlotKey): string {
  const meta = CAPABILITY_SLOTS.find((entry) => entry.key === slot);
  return meta ? t(meta.nameKey) : slot;
}

const valid = computed(() => props.plans.length > 0 && props.plans.every((plan) => planCoverage(plan).valid));
const totalDownload = computed(() => props.plans.reduce((sum, plan) => sum + plan.downloadBytes, 0));
const totalCache = computed(() => props.plans.reduce((sum, plan) => sum + plan.cacheReuseBytes, 0));
const totalInstall = computed(() => props.plans.reduce((sum, plan) => sum + plan.installBytes, 0));
const peak = computed(() => Math.max(0, ...props.plans.map((plan) => plan.peakStagingBytes ?? 0)));
const permissions = computed(() => {
  const values = new Set<string>();
  for (const plan of props.plans) {
    for (const permission of plan.permissions) values.add(permission);
  }
  return values.size ? [...values].join(", ") : t("install.plan.none");
});
const licenses = computed(() => {
  const values = new Set<string>();
  for (const plan of props.plans) {
    const license = plan.license;
    if (!license) continue;
    values.add(
      license.noticeKeys && license.noticeKeys.length > 0
        ? `${license.licenseName} · ${license.noticeKeys.join(", ")}`
        : license.licenseName,
    );
  }
  return values.size ? [...values].join(" · ") : "—";
});
const sharedNote = computed(() =>
  props.plans.some((plan) => visibleModelSlots(planCoverage(plan).slots).length > 1),
);
</script>

<template>
  <YmDialog
    :model-value="modelValue"
    :title="t('onboarding.models.bulkTitle')"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="bulk__body">{{ t("onboarding.models.bulkBody") }}</p>
    <p v-if="sharedNote" class="bulk__note">{{ t("onboarding.models.bulkShared") }}</p>
    <ul class="bulk__list">
      <li v-for="plan in plans" :key="plan.planId" class="bulk__item">
        <span class="bulk__item-main">
          <b>{{ plan.release.version }}</b>
          <span>{{ visibleModelSlots(planCoverage(plan).slots).map(slotLabel).join(", ") }}</span>
        </span>
        <span class="bulk__item-size mono">{{ formatBytes(plan.downloadBytes) }}</span>
      </li>
    </ul>
    <dl class="bulk__totals">
      <div>
        <dt>{{ t("onboarding.models.bulkTotalDownload") }}</dt>
        <dd class="mono">{{ formatBytes(totalDownload) }}</dd>
      </div>
      <div v-if="totalCache > 0">
        <dt>{{ t("install.plan.cache") }}</dt>
        <dd class="mono">{{ formatBytes(totalCache) }}</dd>
      </div>
      <div>
        <dt>{{ t("onboarding.models.bulkTotalInstall") }}</dt>
        <dd class="mono">{{ formatBytes(totalInstall) }}</dd>
      </div>
      <div v-if="peak > 0">
        <dt>{{ t("onboarding.models.bulkPeak") }}</dt>
        <dd class="mono">{{ formatBytes(peak) }}</dd>
      </div>
      <div>
        <dt>{{ t("install.plan.permissions") }}</dt>
        <dd>{{ permissions }}</dd>
      </div>
      <div>
        <dt>{{ t("install.plan.license") }}</dt>
        <dd>{{ licenses }}</dd>
      </div>
    </dl>
    <template #footer="{ close: closeDialog }">
      <YmButton variant="secondary" @click="closeDialog">{{ t("common.cancel") }}</YmButton>
      <YmButton variant="primary" :loading="loading" :disabled="!valid" @click="emit('confirm')">
        {{ t("onboarding.models.bulkConfirm") }}
      </YmButton>
    </template>
  </YmDialog>
</template>

<style scoped>
.bulk__body {
  margin-bottom: var(--space-3);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.bulk__note {
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-info-soft);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.bulk__list {
  display: flex;
  flex-direction: column;
  margin: 0 0 var(--space-4);
  padding: 0;
  list-style: none;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.bulk__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  font-size: var(--font-size-sm);
}
.bulk__item + .bulk__item {
  border-top: 1px solid var(--color-border);
}
.bulk__item-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.bulk__item-main span {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.bulk__item-size {
  flex: none;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.bulk__totals {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
}
.bulk__totals div {
  display: grid;
  grid-template-columns: 130px minmax(0, 1fr);
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.bulk__totals dt {
  color: var(--color-text-faint);
}
.bulk__totals dd {
  margin: 0;
  color: var(--color-text-muted);
  word-break: break-word;
}
</style>

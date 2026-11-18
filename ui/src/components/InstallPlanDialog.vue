<script setup lang="ts">
// 安装计划确认（FR-MODEL-02）：下载/权限范围必须由用户确认后才执行。
// 只展示 Core 返回的计划事实，不在 UI 侧扩大下载或权限范围。
// 一份物理安装可覆盖多个槽位：确认前列出完整覆盖范围，体积只计一次。
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
    plan: InstallationPlan | null;
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

/**
 * 旧 Core 缺覆盖字段时按原单槽语义展示；显式空数组、未知槽位或不含发起槽
 * 的范围视为无效，确认按钮禁用，不能隐藏异常后仍让用户同意。
 */
const coverage = computed(() => planCoverage(props.plan));
/** 隐藏槽（与语音克隆同包）不参与文案，也不单独触发共享说明。 */
const visibleSlots = computed(() => visibleModelSlots(coverage.value.slots));
const coverageLabel = computed(() => visibleSlots.value.map(slotLabel).join(", "));
const shared = computed(() => visibleSlots.value.length > 1);
const reuseVerified = computed(
  () => (props.plan?.downloadBytes ?? 0) === 0 && (props.plan?.cacheReuseBytes ?? 0) > 0,
);

const permissions = computed(() =>
  props.plan && props.plan.permissions.length > 0
    ? props.plan.permissions.join(", ")
    : t("install.plan.none"),
);

const licenseLabel = computed(() => {
  const license = props.plan?.license;
  if (!license) return "—";
  return license.noticeKeys && license.noticeKeys.length > 0
    ? `${license.licenseName} · ${license.noticeKeys.join(", ")}`
    : license.licenseName;
});

function confirm(): void {
  if (!coverage.value.valid) return;
  emit("confirm");
}
</script>

<template>
  <YmDialog
    size="lg"
    :model-value="modelValue"
    :title="t('install.plan.title')"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="plan__body">{{ t("install.plan.body") }}</p>
    <p v-if="plan && !coverage.valid" class="plan__error" role="alert">
      {{ t("install.plan.invalidRange") }}
    </p>
    <p v-else-if="shared" class="plan__note">{{ t("install.plan.sharedNote") }}</p>
    <dl v-if="plan" class="plan">
      <div v-if="coverage.valid" class="plan__row">
        <dt>{{ t("install.plan.covered") }}</dt>
        <dd class="plan__covered">{{ coverageLabel }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.release") }}</dt>
        <dd class="mono">{{ plan.release.version }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.digest") }}</dt>
        <dd class="mono plan__digest">{{ plan.release.digest }}</dd>
      </div>
      <div v-if="plan.reuseInstallationId" class="plan__row">
        <dt>{{ t("install.plan.reuse") }}</dt>
        <dd>
          {{ reuseVerified ? t("install.plan.reuseVerified") : t("install.plan.reuseJoin") }}
        </dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.download") }}</dt>
        <dd class="mono">{{ formatBytes(plan.downloadBytes) }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.cache") }}</dt>
        <dd class="mono">{{ formatBytes(plan.cacheReuseBytes) }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.install") }}</dt>
        <dd class="mono">{{ formatBytes(plan.installBytes) }}</dd>
      </div>
      <div v-if="plan.peakStagingBytes !== undefined" class="plan__row">
        <dt>{{ t("install.plan.peak") }}</dt>
        <dd class="mono">{{ formatBytes(plan.peakStagingBytes) }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.permissions") }}</dt>
        <dd>{{ permissions }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.license") }}</dt>
        <dd>{{ licenseLabel }}</dd>
      </div>
      <div v-if="plan.requiresRestart" class="plan__row">
        <dt>{{ t("install.plan.restart") }}</dt>
        <dd>{{ t("install.plan.required") }}</dd>
      </div>
      <div class="plan__row">
        <dt>{{ t("install.plan.hash") }}</dt>
        <dd class="mono plan__digest">{{ plan.planHash }}</dd>
      </div>
    </dl>
    <template #footer="{ close: closeDialog }">
      <YmButton variant="secondary" @click="closeDialog">{{ t("common.cancel") }}</YmButton>
      <YmButton
        variant="primary"
        :loading="loading"
        :disabled="!coverage.valid"
        @click="confirm"
      >
        {{ t("install.plan.confirm") }}
      </YmButton>
    </template>
  </YmDialog>
</template>

<style scoped>
.plan__body {
  margin-bottom: var(--space-3);
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.plan__note {
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--color-info-soft);
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.plan__error {
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-error);
  border-radius: var(--radius-md);
  background: var(--color-error-soft);
  font-size: var(--font-size-sm);
}
.plan__covered {
  font-weight: 500;
}
.plan {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
}
.plan__row {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.plan__row dt {
  color: var(--color-text-faint);
}
.plan__row dd {
  margin: 0;
  color: var(--color-text-muted);
  word-break: break-word;
}
.plan__digest {
  font-size: var(--font-size-xs);
}
</style>

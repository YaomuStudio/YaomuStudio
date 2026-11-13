<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import { CAPABILITY_SLOTS, DISPLAY_MODEL_SLOTS } from "@/domain/capabilities";
import type { Capability } from "@/domain/types";
import { useCapabilitiesStore } from "@/stores/capabilities";
import YmIcon from "@/components/ui/YmIcon.vue";

const props = defineProps<{ collapsed: boolean }>();
const caps = useCapabilitiesStore();

const MODEL_SLOTS = DISPLAY_MODEL_SLOTS.map((key) =>
  CAPABILITY_SLOTS.find((meta) => meta.key === key),
).filter((meta): meta is (typeof CAPABILITY_SLOTS)[number] => meta !== undefined);

/** 未准入/未验证的槽位不进入分母；缺失记录也不伪造“已发布”。 */
function isPublished(cap: Capability | undefined): boolean {
  return !!cap && cap.state !== "UNSUPPORTED" && cap.state !== "UNVERIFIED";
}

const published = computed(() =>
  MODEL_SLOTS.filter((meta) => isPublished(caps.slotState(meta.key))),
);
const ready = computed(
  () => published.value.filter((meta) => caps.slotState(meta.key)?.state === "READY").length,
);
const total = computed(() => published.value.length);
const loaded = computed(() => caps.slots.length > 0);

const label = computed(() => {
  if (!loaded.value) return t("sidebar.models.loading");
  if (total.value === 0) return t("sidebar.models.unconfigured");
  return t("sidebar.models.ready", { ready: ready.value, total: total.value });
});

const compact = computed(() => {
  if (!loaded.value || total.value === 0) return "—";
  return `${ready.value}/${total.value}`;
});

const tone = computed(() => {
  if (!loaded.value || total.value === 0) return "neutral";
  if (ready.value === total.value) return "success";
  return ready.value > 0 ? "warning" : "neutral";
});
</script>

<template>
  <RouterLink
    to="/settings/models"
    class="sidebar-models"
    :class="{ 'sidebar-models--collapsed': props.collapsed }"
    data-testid="sidebar-model-summary"
    :aria-label="label"
    :title="label"
  >
    <YmIcon class="sidebar-models__icon" name="cpu" :size="15" />
    <span v-if="!props.collapsed" class="sidebar-models__label">{{ label }}</span>
    <span v-else class="sidebar-models__compact">{{ compact }}</span>
    <span class="sidebar-models__dot" :class="`is-${tone}`" aria-hidden="true"></span>
  </RouterLink>
</template>

<style scoped>
.sidebar-models {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
  margin-top: 8px;
  padding: 10px 12px 12px;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  font-weight: 500;
  text-decoration: none;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap);
}
.sidebar-models:hover,
.sidebar-models:focus-visible {
  background: var(--color-surface-2);
  color: var(--color-text);
  text-decoration: none;
}
.sidebar-models__icon {
  flex: none;
  color: var(--color-text-faint);
}
.sidebar-models__label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
.sidebar-models__compact {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
}
.sidebar-models__dot {
  width: 6px;
  height: 6px;
  flex: none;
  border-radius: 50%;
  background: var(--color-text-faint);
}
.sidebar-models__dot.is-success {
  background: var(--color-success);
}
.sidebar-models__dot.is-warning {
  background: var(--color-warning);
}
.sidebar-models--collapsed {
  justify-content: center;
  gap: 4px;
  padding: 9px 0 10px;
  font-size: var(--font-size-2xs);
}
.sidebar-models--collapsed .sidebar-models__compact {
  margin-left: 0;
}
@media (prefers-reduced-motion: reduce) {
  .sidebar-models {
    transition: none;
  }
}
</style>

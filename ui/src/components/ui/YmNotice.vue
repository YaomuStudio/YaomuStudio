<script setup lang="ts">
import { computed } from "vue";
import YmIcon from "./YmIcon.vue";

type Tone = "info" | "success" | "warning" | "error";

const props = withDefaults(
  defineProps<{
    tone?: Tone;
    title?: string;
    icon?: string;
    role?: "alert" | "status";
  }>(),
  { tone: "info" },
);

const defaultIcons: Record<Tone, string> = {
  info: "info",
  success: "check",
  warning: "alert",
  error: "x",
};

const iconName = computed(() => props.icon ?? defaultIcons[props.tone]);
const role = computed(() => props.role ?? (props.tone === "error" ? "alert" : "status"));
</script>

<template>
  <section class="ym-notice" :class="`is-${tone}`" :role="role">
    <span class="ym-notice__icon" aria-hidden="true">
      <YmIcon :name="iconName" :size="13" />
    </span>
    <div class="ym-notice__body">
      <p v-if="title" class="ym-notice__title">{{ title }}</p>
      <div class="ym-notice__content"><slot /></div>
    </div>
    <div v-if="$slots.actions" class="ym-notice__actions">
      <slot name="actions" />
    </div>
  </section>
</template>

<style scoped>
.ym-notice {
  --notice-color: var(--color-info-strong);
  --notice-soft: var(--color-info-soft);
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr);
  align-items: start;
  column-gap: 9px;
  row-gap: 8px;
  padding: 10px 12px;
  border: 1px solid color-mix(in srgb, var(--notice-color) 22%, var(--color-border));
  border-radius: var(--radius-control);
  background: color-mix(in srgb, var(--notice-soft) 72%, var(--color-surface));
}
.ym-notice.is-success {
  --notice-color: var(--color-success-strong);
  --notice-soft: var(--color-success-soft);
}
.ym-notice.is-warning {
  --notice-color: var(--color-warning-strong);
  --notice-soft: var(--color-warning-soft);
}
.ym-notice.is-error {
  --notice-color: var(--color-error-strong);
  --notice-soft: var(--color-error-soft);
}
.ym-notice__icon {
  grid-column: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  flex: none;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--notice-color) 12%, transparent);
  color: var(--notice-color);
}
.ym-notice__body {
  grid-column: 2;
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  padding-top: 2px;
}
.ym-notice__title {
  color: var(--notice-color);
  font-size: var(--font-size-sm);
  font-weight: 600;
  line-height: 1.35;
}
.ym-notice__content {
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: var(--line-height-normal);
  overflow-wrap: anywhere;
}
.ym-notice__actions {
  grid-column: 2;
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
</style>

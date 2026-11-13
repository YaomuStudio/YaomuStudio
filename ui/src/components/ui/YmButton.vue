<script setup lang="ts">
import YmIcon from "./YmIcon.vue";
import YmSpinner from "./YmSpinner.vue";

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md" | "lg";

withDefaults(
  defineProps<{
    variant?: Variant;
    size?: Size;
    icon?: string;
    disabled?: boolean;
    loading?: boolean;
    block?: boolean;
  }>(),
  { variant: "secondary", size: "md", disabled: false, loading: false, block: false },
);
</script>

<template>
  <button
    class="ym-btn"
    :class="[`ym-btn--${variant}`, `ym-btn--${size}`, { 'is-block': block }]"
    :disabled="disabled || loading"
    type="button"
  >
    <YmSpinner v-if="loading" :size="size === 'sm' ? 12 : 14" />
    <YmIcon v-else-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" />
    <slot />
  </button>
</template>

<style scoped>
.ym-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  font-weight: 500;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  transform-origin: center;
  transition:
    background var(--motion-duration-base) var(--motion-ease-out),
    border-color var(--motion-duration-base) var(--motion-ease-out),
    color var(--motion-duration-base) var(--motion-ease-out),
    box-shadow var(--motion-duration-base) var(--motion-ease-out),
    filter var(--motion-duration-base) var(--motion-ease-out),
    transform var(--motion-duration-fast) var(--motion-ease-snap);
  white-space: nowrap;
}
.ym-btn:active:not(:disabled) {
  transform: scale(0.97);
}
.ym-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.ym-btn.is-block {
  width: 100%;
}
.ym-btn--sm {
  height: 26px;
  padding: 0 10px;
  font-size: var(--font-size-sm);
}
.ym-btn--md {
  height: 32px;
  padding: 0 14px;
  font-size: var(--font-size-md);
}
.ym-btn--lg {
  height: 38px;
  padding: 0 18px;
  font-size: var(--font-size-lg);
}
.ym-btn--primary {
  background: var(--color-primary-action);
  color: var(--color-on-primary-action);
  box-shadow: var(--shadow-primary);
}
.ym-btn--primary:hover:not(:disabled) {
  background: var(--color-primary-action-hover);
  filter: brightness(1.06);
  box-shadow: var(--shadow-md);
}
.ym-btn--primary:active:not(:disabled) {
  background: var(--color-primary-active);
  filter: brightness(0.96);
  box-shadow: var(--shadow-sm);
}
.ym-btn--secondary {
  background: var(--color-surface);
  border-color: var(--color-border-strong);
  color: var(--color-text);
}
.ym-btn--secondary:hover:not(:disabled) {
  background: var(--color-surface-2);
  border-color: var(--color-text-faint);
}
.ym-btn--ghost {
  background: transparent;
  color: var(--color-text-muted);
}
.ym-btn--ghost:hover:not(:disabled) {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.ym-btn--danger {
  background: var(--color-error);
  color: var(--color-on-danger);
}
.ym-btn--danger:hover:not(:disabled) {
  background: var(--color-error);
  filter: brightness(0.92);
}
</style>

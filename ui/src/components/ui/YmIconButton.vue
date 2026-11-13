<script setup lang="ts">
import YmIcon from "./YmIcon.vue";

withDefaults(
  defineProps<{
    name: string;
    label: string;
    size?: number;
    disabled?: boolean;
    variant?: "ghost" | "secondary" | "danger";
  }>(),
  { size: 18, disabled: false, variant: "ghost" },
);
</script>

<template>
  <button
    class="ym-icon-btn"
    :class="`ym-icon-btn--${variant}`"
    :disabled="disabled"
    type="button"
    :aria-label="label"
    :title="label"
  >
    <YmIcon :name="name" :size="size" />
  </button>
</template>

<style scoped>
.ym-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  transform-origin: center;
  transition:
    background var(--motion-duration-base) var(--motion-ease-out),
    color var(--motion-duration-base) var(--motion-ease-out),
    transform var(--motion-duration-fast) var(--motion-ease-snap);
}
.ym-icon-btn:active:not(:disabled) {
  transform: scale(0.94);
}
.ym-icon-btn:hover:not(:disabled) {
  background: var(--color-surface-2);
  color: var(--color-text);
}
.ym-icon-btn--danger:hover:not(:disabled) {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.ym-icon-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
</style>

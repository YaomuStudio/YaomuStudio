<script setup lang="ts">
import type { Tone } from "@/domain/status";

withDefaults(defineProps<{ tone?: Tone; solid?: boolean }>(), { tone: "neutral", solid: false });
</script>

<template>
  <span class="ym-badge" :class="[`ym-badge--${tone}`, { 'is-solid': solid }]"><slot /></span>
</template>

<style scoped>
.ym-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 20px;
  padding: 0 8px;
  font-size: var(--font-size-xs);
  font-weight: 500;
  border-radius: var(--radius-pill);
  white-space: nowrap;
}
.ym-badge--neutral {
  background: var(--color-surface-3);
  color: var(--color-text-muted);
}
.ym-badge--success {
  background: var(--color-success-soft);
  color: var(--color-success-strong);
}
.ym-badge--warning {
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
}
.ym-badge--error {
  background: var(--color-error-soft);
  color: var(--color-error-strong);
}
.ym-badge--info {
  background: var(--color-info-soft);
  color: var(--color-info-strong);
}
.ym-badge--accent {
  background: var(--color-accent-soft);
  color: var(--color-accent-strong);
}
.ym-badge.is-solid {
  height: 22px;
  padding: 0 10px;
  background: var(--badge-bg, var(--color-primary));
  color: var(--badge-text, var(--color-on-primary));
  font-weight: 600;
  text-shadow: 0 1px 1px rgba(0, 0, 0, 0.14);
  box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.16), 0 1px 2px rgba(16, 20, 30, 0.12);
}
.ym-badge--neutral.is-solid {
  --badge-bg: var(--badge-neutral-bg);
  --badge-text: var(--badge-neutral-text);
}
.ym-badge--success.is-solid {
  --badge-bg: var(--badge-success-bg);
  --badge-text: var(--badge-success-text);
}
.ym-badge--warning.is-solid {
  --badge-bg: var(--badge-warning-bg);
  --badge-text: var(--badge-warning-text);
}
.ym-badge--error.is-solid {
  --badge-bg: var(--badge-error-bg);
  --badge-text: var(--badge-error-text);
}
.ym-badge--info.is-solid {
  --badge-bg: var(--badge-info-bg);
  --badge-text: var(--badge-info-text);
}
.ym-badge--accent.is-solid {
  --badge-bg: var(--badge-accent-bg);
  --badge-text: var(--badge-accent-text);
}
</style>

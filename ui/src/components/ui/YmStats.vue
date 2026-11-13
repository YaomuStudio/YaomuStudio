<script setup lang="ts">
import YmSkeleton from "./YmSkeleton.vue";

export interface StatItem {
  value: string | number;
  label: string;
  /** 协议式数值（时长/字节/ID）按 LTR 隔离，普通计数不需要。 */
  dir?: "ltr" | "auto";
}

withDefaults(
  defineProps<{
    items: StatItem[];
    variant?: "inline" | "card";
    loading?: boolean;
  }>(),
  { variant: "inline", loading: false },
);
</script>

<template>
  <div class="ym-stats" :class="`ym-stats--${variant}`">
    <div v-for="item in items" :key="item.label" class="ym-stats__item">
      <span class="ym-stats__value">
        <YmSkeleton v-if="loading" width="26px" height="20px" radius="6px" />
        <bdi v-else-if="item.dir" :dir="item.dir">{{ item.value }}</bdi>
        <template v-else>{{ item.value }}</template>
      </span>
      <span class="ym-stats__label">{{ item.label }}</span>
    </div>
  </div>
</template>

<style scoped>
.ym-stats {
  display: flex;
  align-items: stretch;
  min-width: 0;
}
.ym-stats--inline {
  gap: 24px;
}
.ym-stats--card {
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  box-shadow: var(--shadow-sm);
}
.ym-stats__item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ym-stats--inline .ym-stats__item + .ym-stats__item {
  padding-inline-start: 24px;
  border-inline-start: 1px solid var(--color-border);
}
.ym-stats--card .ym-stats__item {
  flex: 1;
  padding: 12px 18px;
}
.ym-stats--card .ym-stats__item + .ym-stats__item {
  border-inline-start: 1px solid var(--color-border);
}
.ym-stats__value {
  display: inline-flex;
  align-items: center;
  min-height: 26px;
  color: var(--color-text);
  font-size: var(--font-size-xl);
  font-weight: 700;
  letter-spacing: -0.01em;
  font-variant-numeric: tabular-nums;
}
.ym-stats__label {
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
@media (max-width: 760px) {
  .ym-stats--card {
    flex-wrap: wrap;
  }
  .ym-stats--card .ym-stats__item {
    flex: 1 1 50%;
  }
}
</style>

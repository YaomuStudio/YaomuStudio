<script setup lang="ts">
import YmIcon from "./YmIcon.vue";

withDefaults(
  defineProps<{ title?: string; description?: string; icon?: string; fill?: boolean }>(),
  { icon: "box", fill: false },
);
</script>

<template>
  <div class="ym-empty" :class="{ 'is-fill': fill }">
    <span v-if="icon" class="ym-empty__icon">
      <YmIcon :name="icon" :size="24" />
    </span>
    <p v-if="title" class="ym-empty__title">{{ title }}</p>
    <p v-if="description" class="ym-empty__desc">{{ description }}</p>
    <div v-if="$slots.action" class="ym-empty__action">
      <slot name="action" />
    </div>
  </div>
</template>

<style scoped>
.ym-empty.is-fill {
  flex: 1;
  width: 100%;
  min-height: 0;
}
.ym-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-7);
  text-align: center;
  color: var(--color-text-faint);
}
.ym-empty__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 64px;
  height: 64px;
  margin-bottom: var(--space-1);
  border: 1px solid color-mix(in srgb, var(--color-primary) 18%, transparent);
  border-radius: var(--radius-xl);
  background: var(--gradient-brand-soft);
  color: var(--color-primary);
}
.ym-empty__title {
  font-size: var(--font-size-lg);
  font-weight: 500;
  color: var(--color-text-muted);
}
.ym-empty__desc {
  font-size: var(--font-size-sm);
  max-width: 320px;
}
.ym-empty__action {
  margin-top: var(--space-2);
}
</style>

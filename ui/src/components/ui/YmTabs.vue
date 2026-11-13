<script setup lang="ts">
export interface Tab {
  value: string;
  label: string;
}

defineProps<{ modelValue: string; tabs: Tab[] }>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
</script>

<template>
  <div class="ym-tabs" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.value"
      type="button"
      role="tab"
      class="ym-tabs__item"
      :class="{ 'is-active': modelValue === t.value }"
      :aria-selected="modelValue === t.value"
      @click="emit('update:modelValue', t.value)"
    >
      {{ t.label }}
    </button>
  </div>
</template>

<style scoped>
.ym-tabs {
  display: inline-flex;
  gap: var(--space-1);
  border-bottom: 1px solid var(--color-border);
}
.ym-tabs__item {
  padding: var(--space-2) var(--space-3);
  font-size: var(--font-size-md);
  color: var(--color-text-muted);
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  transition:
    color var(--motion-duration-snap) var(--motion-ease-snap),
    border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.ym-tabs__item.is-active {
  color: var(--color-text);
  border-bottom-color: var(--color-primary);
  font-weight: 500;
}
</style>

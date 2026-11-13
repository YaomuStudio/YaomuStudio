<script setup lang="ts">
export interface SegmentOption {
  value: string;
  label: string;
}

defineProps<{ modelValue: string; options: SegmentOption[] }>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
</script>

<template>
  <div class="ym-segmented" role="tablist">
    <button
      v-for="o in options"
      :key="o.value"
      type="button"
      class="ym-segmented__item"
      :class="{ 'is-active': modelValue === o.value }"
      role="tab"
      :aria-selected="modelValue === o.value"
      @click="emit('update:modelValue', o.value)"
    >
      {{ o.label }}
    </button>
  </div>
</template>

<style scoped>
.ym-segmented {
  display: inline-flex;
  padding: 2px;
  gap: 2px;
  background: var(--color-surface-2);
  border-radius: var(--radius-md);
}
.ym-segmented__item {
  padding: 4px 12px;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
  border-radius: var(--radius-sm);
  white-space: nowrap;
  transform-origin: center;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap),
    box-shadow var(--motion-duration-snap) var(--motion-ease-snap),
    transform var(--motion-duration-fast) var(--motion-ease-snap);
}
.ym-segmented__item:active {
  transform: scale(0.97);
}
.ym-segmented__item.is-active {
  background: var(--color-surface);
  color: var(--color-text);
  box-shadow: var(--shadow-sm);
  font-weight: 500;
}
</style>

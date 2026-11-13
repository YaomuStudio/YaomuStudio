<script setup lang="ts">
import { t } from "@/i18n";

type Quality = "standard" | "fine";

defineProps<{ modelValue: Quality; disabled?: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", value: Quality): void }>();

const options: { value: Quality; titleKey: string; hintKey: string }[] = [
  { value: "standard", titleKey: "ocr.quality.standard", hintKey: "ocr.quality.standardHint" },
  { value: "fine", titleKey: "ocr.quality.fine", hintKey: "ocr.quality.fineHint" },
];
</script>

<template>
  <div class="ocr-quality" role="radiogroup" :aria-label="t('ocr.quality.label')">
    <label
      v-for="option in options"
      :key="option.value"
      class="ocr-quality__card"
      :class="{ 'is-selected': modelValue === option.value, 'is-disabled': disabled }"
    >
      <input
        type="radio"
        class="ocr-quality__radio"
        :name="`ocr-quality-${option.value}`"
        :value="option.value"
        :checked="modelValue === option.value"
        :disabled="disabled"
        @change="emit('update:modelValue', option.value)"
      />
      <span class="ocr-quality__body">
        <span class="ocr-quality__title">{{ t(option.titleKey) }}</span>
        <span class="ocr-quality__hint">{{ t(option.hintKey) }}</span>
      </span>
    </label>
  </div>
</template>

<style scoped>
.ocr-quality {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2);
}
.ocr-quality__card {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-width: 0;
  padding: 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  cursor: pointer;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-out),
    border-color var(--motion-duration-snap) var(--motion-ease-out),
    box-shadow var(--motion-duration-snap) var(--motion-ease-out);
}
.ocr-quality__card:hover:not(.is-disabled) {
  border-color: var(--color-border-strong);
  background: var(--color-surface-2);
}
.ocr-quality__card.is-selected {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}
.ocr-quality__card.is-disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.ocr-quality__radio {
  flex: none;
  margin-top: 2px;
  accent-color: var(--color-primary);
}
.ocr-quality__body {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.ocr-quality__title {
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 600;
}
.ocr-quality__card.is-selected .ocr-quality__title {
  color: var(--color-primary);
}
.ocr-quality__hint {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
  line-height: 1.4;
}
</style>

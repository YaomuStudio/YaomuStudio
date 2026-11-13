<script setup lang="ts">
withDefaults(
  defineProps<{ modelValue: string; rows?: number; placeholder?: string; disabled?: boolean }>(),
  { rows: 4, disabled: false, placeholder: "" },
);
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

function onInput(e: Event): void {
  emit("update:modelValue", (e.target as HTMLTextAreaElement).value);
}
</script>

<template>
  <textarea
    class="ym-textarea"
    :value="modelValue"
    :rows="rows"
    :placeholder="placeholder"
    :disabled="disabled"
    @input="onInput"
  ></textarea>
</template>

<style scoped>
.ym-textarea {
  width: 100%;
  padding: 8px 10px;
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  color: var(--color-text);
  resize: vertical;
  line-height: var(--line-height-normal);
}
.ym-textarea::placeholder {
  color: var(--color-text-faint);
}
.ym-textarea:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.ym-textarea:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
</style>

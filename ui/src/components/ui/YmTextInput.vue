<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: string;
    placeholder?: string;
    disabled?: boolean;
    type?: string;
  }>(),
  { type: "text", disabled: false, placeholder: "" },
);
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

function onInput(e: Event): void {
  emit("update:modelValue", (e.target as HTMLInputElement).value);
}
</script>

<template>
  <input
    class="ym-input"
    :type="type"
    :value="modelValue"
    :placeholder="placeholder"
    :disabled="disabled"
    @input="onInput"
  />
</template>

<style scoped>
.ym-input {
  width: 100%;
  height: 32px;
  padding: 0 10px;
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  color: var(--color-text);
}
.ym-input::placeholder {
  color: var(--color-text-faint);
}
.ym-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: var(--focus-ring);
}
.ym-input:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
</style>

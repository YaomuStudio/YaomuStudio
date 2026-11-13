<script setup lang="ts">
export interface RadioOption {
  value: string;
  label: string;
  disabled?: boolean;
}

withDefaults(
  defineProps<{ modelValue: string; options: RadioOption[]; disabled?: boolean }>(),
  { disabled: false },
);
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
</script>

<template>
  <div class="ym-radio" role="radiogroup">
    <label
      v-for="o in options"
      :key="o.value"
      class="ym-radio__item"
      :class="{ 'ym-radio__item--disabled': disabled || o.disabled }"
    >
      <input
        type="radio"
        class="ym-radio__input"
        :value="o.value"
        :checked="modelValue === o.value"
        :disabled="disabled || o.disabled"
        :name="`radio-${Math.random().toString(36).slice(2)}`"
        @change="emit('update:modelValue', o.value)"
      />
      <span class="ym-radio__dot" aria-hidden="true"></span>
      <span class="ym-radio__label">{{ o.label }}</span>
    </label>
  </div>
</template>

<style scoped>
.ym-radio {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.ym-radio__item {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
}
.ym-radio__item--disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.ym-radio__input {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
  border: 0;
}
.ym-radio__dot {
  width: 18px;
  height: 18px;
  flex: none;
  border: 1.5px solid var(--color-border-strong);
  border-radius: 50%;
  position: relative;
  transition: border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.ym-radio__dot::after {
  content: "";
  position: absolute;
  inset: 4px;
  border-radius: 50%;
  background: var(--color-primary);
  transform: scale(0);
  transition: transform var(--motion-duration-base) var(--motion-ease-overshoot);
}
.ym-radio__input:checked + .ym-radio__dot {
  border-color: var(--color-primary);
}
.ym-radio__input:checked + .ym-radio__dot::after {
  transform: scale(1);
}
.ym-radio__input:focus-visible + .ym-radio__dot {
  box-shadow: var(--focus-ring);
}
.ym-radio__label {
  font-size: var(--font-size-md);
}
</style>

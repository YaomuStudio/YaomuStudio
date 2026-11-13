<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: boolean;
    label?: string;
    /** 视觉标题在外部时的可访问名称；未传时沿用可见 label。 */
    ariaLabel?: string;
    disabled?: boolean;
  }>(),
  { disabled: false, label: "", ariaLabel: "" },
);
const emit = defineEmits<{ (e: "update:modelValue", v: boolean): void }>();
</script>

<template>
  <label class="ym-switch" :class="{ 'is-disabled': disabled }">
    <input
      type="checkbox"
      class="ym-switch__input"
      :checked="modelValue"
      :disabled="disabled"
      :aria-label="ariaLabel || undefined"
      @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
    />
    <span class="ym-switch__track" aria-hidden="true"><span class="ym-switch__thumb"></span></span>
    <span v-if="label" class="ym-switch__label">{{ label }}</span>
  </label>
</template>

<style scoped>
.ym-switch {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
}
.ym-switch__input {
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
.ym-switch__track {
  position: relative;
  width: 36px;
  height: 20px;
  flex: none;
  background: var(--color-surface-3);
  border-radius: var(--radius-pill);
  transition: background var(--motion-duration-snap) var(--motion-ease-snap);
}
.ym-switch__thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  background: var(--color-on-primary);
  border-radius: 50%;
  transition: transform var(--motion-duration-base) var(--motion-ease-overshoot);
  box-shadow: var(--shadow-sm);
}
.ym-switch__input:checked + .ym-switch__track {
  background: var(--color-primary);
}
.ym-switch__input:checked + .ym-switch__track .ym-switch__thumb {
  transform: translateX(16px);
}
.ym-switch__input:focus-visible + .ym-switch__track {
  box-shadow: var(--focus-ring);
}
.ym-switch__label {
  font-size: var(--font-size-md);
}
.ym-switch.is-disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>

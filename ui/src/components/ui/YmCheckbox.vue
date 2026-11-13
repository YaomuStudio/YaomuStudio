<script setup lang="ts">
import { ref } from "vue";

const props = withDefaults(
  defineProps<{ modelValue: boolean; label?: string; disabled?: boolean }>(),
  { disabled: false, label: "" },
);
const emit = defineEmits<{ (e: "update:modelValue", v: boolean): void }>();
const input = ref<HTMLInputElement | null>(null);

/**
 * 受控复选框：浏览器原生 toggle 先改 DOM。若父级拒绝或异步确认尚未完成，
 * 立即恢复为当前 model，避免“UI 看似已确认、实际未通过”的假同步。
 * 父级接受后 prop 更新，Vue 再把 checked 设为 true，无需额外状态。
 */
function onChange(event: Event): void {
  const target = event.target as HTMLInputElement;
  emit("update:modelValue", target.checked);
  target.checked = props.modelValue;
}
</script>

<template>
  <label class="ym-checkbox" :class="{ 'is-disabled': disabled }">
    <input
      ref="input"
      type="checkbox"
      class="ym-checkbox__input"
      :checked="modelValue"
      :disabled="disabled"
      @change="onChange"
    />
    <span class="ym-checkbox__box" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <path d="M5 13l4 4 10-11" />
      </svg>
    </span>
    <span v-if="label" class="ym-checkbox__label">{{ label }}</span>
  </label>
</template>

<style scoped>
.ym-checkbox {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
}
.ym-checkbox__input {
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
.ym-checkbox__box {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  flex: none;
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-sm);
  color: transparent;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    border-color var(--motion-duration-snap) var(--motion-ease-snap);
}
.ym-checkbox__box svg {
  opacity: 0;
  transform: scale(0.6);
  transition:
    opacity var(--motion-duration-fast) var(--motion-ease-out),
    transform var(--motion-duration-base) var(--motion-ease-overshoot);
}
.ym-checkbox__input:checked + .ym-checkbox__box {
  background: var(--color-primary);
  border-color: var(--color-primary);
  color: var(--color-on-primary);
}
.ym-checkbox__input:checked + .ym-checkbox__box svg {
  opacity: 1;
  transform: scale(1);
}
.ym-checkbox__input:focus-visible + .ym-checkbox__box {
  box-shadow: var(--focus-ring);
}
.ym-checkbox__label {
  font-size: var(--font-size-md);
}
.ym-checkbox.is-disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>

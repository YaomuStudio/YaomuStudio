<script setup lang="ts">
export interface Step {
  key: string;
  label: string;
}

withDefaults(defineProps<{ steps: Step[]; current: string; disabled?: boolean }>(), { disabled: false });
const emit = defineEmits<{ (e: "update:current", v: string): void }>();
</script>

<template>
  <ol class="ym-stepper">
    <li
      v-for="(s, i) in steps"
      :key="s.key"
      class="ym-stepper__step"
      :class="{
        'is-active': s.key === current,
        'is-done': steps.findIndex((x) => x.key === current) > i,
      }"
    >
      <button type="button" class="ym-stepper__btn" :disabled="disabled" @click="emit('update:current', s.key)">
        <span class="ym-stepper__idx">{{ i + 1 }}</span>
        <span class="ym-stepper__label">{{ s.label }}</span>
      </button>
      <span v-if="i < steps.length - 1" class="ym-stepper__line" aria-hidden="true"></span>
    </li>
  </ol>
</template>

<style scoped>
.ym-stepper {
  display: flex;
  align-items: center;
  gap: 0;
  list-style: none;
  margin: 0;
  padding: 0;
}
.ym-stepper__step {
  display: flex;
  align-items: center;
}
.ym-stepper__btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0;
}
.ym-stepper__idx {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--color-surface-3);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  font-weight: 600;
  transition:
    background var(--motion-duration-snap) var(--motion-ease-snap),
    color var(--motion-duration-snap) var(--motion-ease-snap),
    box-shadow var(--motion-duration-base) var(--motion-ease-overshoot);
}
.ym-stepper__label {
  font-size: var(--font-size-md);
  color: var(--color-text-muted);
}
.ym-stepper__step.is-active .ym-stepper__idx {
  background: var(--gradient-primary);
  color: var(--color-on-primary);
  box-shadow: 0 0 0 4px var(--color-primary-soft);
}
.ym-stepper__step.is-active .ym-stepper__label {
  color: var(--color-text);
  font-weight: 600;
}
.ym-stepper__step.is-done .ym-stepper__idx {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.ym-stepper__line {
  width: 40px;
  height: 2px;
  margin: 0 var(--space-2);
  background: var(--color-border);
}
.ym-stepper__step.is-done .ym-stepper__line {
  background: var(--gradient-primary);
}
</style>

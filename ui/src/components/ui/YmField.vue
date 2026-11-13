<script setup lang="ts">
defineProps<{ label?: string; hint?: string; error?: string; required?: boolean }>();
</script>

<template>
  <div class="ym-field">
    <label v-if="label" class="ym-field__label">
      {{ label }}<span v-if="required" class="ym-field__req" aria-hidden="true">*</span>
    </label>
    <slot />
    <p v-if="hint && !error" class="ym-field__hint">{{ hint }}</p>
    <p v-if="error" class="ym-field__error" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.ym-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ym-field__label {
  font-size: var(--font-size-sm);
  font-weight: 500;
  color: var(--color-text-muted);
}
.ym-field__req {
  color: var(--color-error-strong);
  margin-left: 2px;
}
.ym-field__hint {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.ym-field__error {
  font-size: var(--font-size-xs);
  color: var(--color-error-strong);
}
</style>

<template>
  <div class="ym-card">
    <div v-if="$slots.header" class="ym-card__header">
      <slot name="header" />
    </div>
    <div class="ym-card__body">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.ym-card {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.ym-card__header {
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--color-border);
  font-weight: 500;
}
.ym-card__body {
  padding: var(--space-4);
}
</style>

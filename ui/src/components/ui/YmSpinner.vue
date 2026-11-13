<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 16 });
</script>

<template>
  <span
    class="ym-spinner"
    role="status"
    aria-label="loading"
    :style="{ width: `${size}px`, height: `${size}px` }"
  ></span>
</template>

<style scoped>
.ym-spinner {
  display: inline-block;
  border: 2px solid var(--color-border-strong);
  border-top-color: currentColor;
  border-radius: 50%;
  animation: ym-spin var(--motion-duration-spin) linear infinite;
}
@keyframes ym-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .ym-spinner {
    animation: none;
    border-top-color: var(--color-text-muted);
  }
}
</style>

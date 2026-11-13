<script setup lang="ts">
withDefaults(
  defineProps<{
    width?: string;
    height?: string;
    radius?: string;
    circle?: boolean;
  }>(),
  { width: "100%", height: "14px", radius: "", circle: false },
);
</script>

<template>
  <span
    class="ym-skeleton"
    :class="{ 'is-circle': circle }"
    :style="{
      width,
      height,
      borderRadius: circle ? '50%' : radius || undefined,
    }"
    aria-hidden="true"
  />
</template>

<style scoped>
.ym-skeleton {
  display: block;
  flex: none;
  background: var(--color-surface-3);
  border-radius: var(--radius-sm);
  animation: ym-skeleton-pulse var(--motion-duration-ambient) var(--motion-ease-out) infinite;
}
.ym-skeleton.is-circle {
  border-radius: 50%;
}
@keyframes ym-skeleton-pulse {
  0%,
  100% {
    opacity: 0.55;
  }
  50% {
    opacity: 1;
  }
}
@media (prefers-reduced-motion: reduce) {
  .ym-skeleton {
    animation: none;
    opacity: 0.75;
  }
}
</style>

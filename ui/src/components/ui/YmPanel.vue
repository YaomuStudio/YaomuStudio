<script setup lang="ts">
import { ref } from "vue";
import YmIcon from "./YmIcon.vue";

const props = withDefaults(
  defineProps<{ title: string; collapsible?: boolean; defaultOpen?: boolean }>(),
  { collapsible: false, defaultOpen: true },
);
const open = ref(props.defaultOpen);
</script>

<template>
  <section class="ym-panel">
    <header class="ym-panel__header" :class="{ 'is-collapsible': collapsible }" @click="collapsible && (open = !open)">
      <h3 class="ym-panel__title">{{ title }}</h3>
      <YmIcon v-if="collapsible" :name="open ? 'chevron-down' : 'chevron-right'" :size="16" />
    </header>
    <div v-show="open" class="ym-panel__body">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.ym-panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  overflow: hidden;
}
.ym-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-3) var(--space-4);
}
.ym-panel__header.is-collapsible {
  cursor: pointer;
  user-select: none;
}
.ym-panel__title {
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--color-text);
}
.ym-panel__body {
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--color-border);
}
</style>

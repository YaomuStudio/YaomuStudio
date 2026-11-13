<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import YmIcon from "./YmIcon.vue";

// 纯展示返回按钮：只发出 click，由页面决定路由或返回动作。
const props = withDefaults(defineProps<{ label?: string; text?: string }>(), {
  label: undefined,
  text: undefined,
});
const emit = defineEmits<{ (e: "click"): void }>();

const accessibleLabel = computed(() => props.label || t("common.back"));
</script>

<template>
  <button
    type="button"
    class="ym-back-button"
    :class="{ 'has-text': text }"
    :aria-label="accessibleLabel"
    :title="accessibleLabel"
    @click="emit('click')"
  >
    <YmIcon name="chevron-left" :size="text ? 16 : 18" />
    <span v-if="text" class="ym-back-button__text">{{ text }}</span>
  </button>
</template>

<style scoped>
.ym-back-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  flex: none;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-surface);
  color: var(--color-text-muted);
  transition:
    background var(--motion-duration-snap) var(--motion-ease-out),
    border-color var(--motion-duration-snap) var(--motion-ease-out),
    color var(--motion-duration-snap) var(--motion-ease-out);
}
.ym-back-button.has-text {
  width: auto;
  padding-inline: 12px;
  gap: 6px;
  font-size: var(--font-size-md);
  font-weight: 500;
}
.ym-back-button__text {
  white-space: nowrap;
}
.ym-back-button:hover {
  border-color: color-mix(in srgb, var(--color-primary) 32%, var(--color-border));
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.ym-back-button:focus-visible {
  outline: none;
  box-shadow: var(--focus-ring);
}
</style>

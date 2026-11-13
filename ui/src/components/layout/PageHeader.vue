<script setup lang="ts">
import YmIcon from "@/components/ui/YmIcon.vue";

// 纯展示页头：标题/说明/动作区由页面传入，不读取 API 或业务状态。
withDefaults(defineProps<{ title: string; description?: string; icon?: string }>(), {
  icon: "box",
});
</script>

<template>
  <header class="page-header" data-testid="page-header">
    <div class="page-header__main">
      <slot name="leading">
        <span class="page-header__icon" aria-hidden="true">
          <YmIcon :name="icon" :size="19" />
        </span>
      </slot>
      <div class="page-header__text">
        <h1 class="page-header__title">{{ title }}</h1>
        <p v-if="description || $slots.description" class="page-header__desc">
          <slot name="description">{{ description }}</slot>
        </p>
        <div v-if="$slots.meta" class="page-header__meta">
          <slot name="meta" />
        </div>
      </div>
    </div>
    <div v-if="$slots.actions" class="page-header__aside">
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--space-3) var(--space-5);
  padding-block-end: 16px;
  border-block-end: 1px solid var(--color-border);
}
.page-header__main {
  display: flex;
  flex: 1 1 320px;
  align-items: flex-start;
  gap: 12px;
  min-width: 0;
}
.page-header__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  margin-block-start: -5px;
  border-radius: var(--radius-control);
  border: 1px solid color-mix(in srgb, var(--color-primary) 16%, transparent);
  background: var(--gradient-brand-soft);
  color: var(--color-primary);
}
.page-header__text {
  flex: 1;
  min-width: 0;
}
.page-header__meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: 6px;
}
.page-header__title {
  font-size: var(--font-size-page-title);
  font-weight: 600;
  letter-spacing: -0.01em;
  line-height: 1.25;
  overflow-wrap: anywhere;
}
.page-header__desc {
  max-width: 980px;
  margin-block-start: 3px;
  font-size: var(--font-size-md);
  line-height: 1.5;
  color: var(--color-text-muted);
}
.page-header__aside {
  display: flex;
  flex: none;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-3);
}
@media (max-width: 900px) {
  .page-header {
    align-items: flex-start;
  }
}
</style>

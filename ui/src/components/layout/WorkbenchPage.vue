<script setup lang="ts">
import { onBeforeUnmount, onMounted } from "vue";
import { t } from "@/i18n";
import YmButton from "@/components/ui/YmButton.vue";
import PageHeader from "./PageHeader.vue";

// 生产类页面脚手架：默认保持既有紧凑页头 + 左配置/右结果两列（窄窗堆叠）。
// scaffold 为显式 opt-in：启用后使用共享页头、可选 subheader 与吸底 footer；
// 既有消费者在没有传 scaffold 时不改变默认 CTA/actions 行为。
const props = withDefaults(
  defineProps<{
    title: string;
    subtitle?: string;
    icon?: string;
    primaryLabel?: string;
    primaryIcon?: string;
    primaryDisabled?: boolean;
    primaryLoading?: boolean;
    primaryShortcut?: boolean;
    scaffold?: boolean;
  }>(),
  {
    icon: "box",
    primaryDisabled: false,
    primaryLoading: false,
    primaryShortcut: false,
    scaffold: false,
  },
);
const emit = defineEmits<{ (e: "primary"): void }>();

function onShortcut(event: KeyboardEvent): void {
  if (!props.primaryShortcut || props.primaryDisabled || props.primaryLoading) return;
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
    event.preventDefault();
    emit("primary");
  }
}

onMounted(() => window.addEventListener("keydown", onShortcut));
onBeforeUnmount(() => window.removeEventListener("keydown", onShortcut));
</script>

<template>
  <div class="wb-page" :class="{ 'wb-page--scaffold': scaffold }">
    <div v-if="scaffold" class="wb-page__head">
      <PageHeader :title="title" :description="subtitle" :icon="icon">
        <template v-if="$slots.leading" #leading>
          <slot name="leading" />
        </template>
        <template v-if="$slots.actions" #actions>
          <slot name="actions" />
        </template>
      </PageHeader>
    </div>
    <header v-else class="wb-page__head">
      <h1 class="wb-page__title">{{ title }}</h1>
      <div class="wb-page__actions">
        <slot name="actions" />
        <YmButton
          v-if="primaryLabel"
          variant="primary"
          :icon="primaryIcon"
          :disabled="primaryDisabled"
          :loading="primaryLoading"
          @click="emit('primary')"
        >
          {{ primaryLabel }}
        </YmButton>
      </div>
    </header>
    <div v-if="scaffold && $slots.subheader" class="wb-page__subheader">
      <slot name="subheader" />
    </div>
    <div class="wb-page__cols">
      <section class="wb-page__col wb-page__config">
        <slot name="config" />
      </section>
      <section class="wb-page__col wb-page__results">
        <slot name="results" />
      </section>
    </div>
    <footer
      v-if="scaffold && (primaryLabel || $slots.footer || $slots['footer-actions'])"
      class="wb-page__footer"
    >
      <div class="wb-page__footer-status">
        <slot name="footer" />
      </div>
      <div class="wb-page__footer-actions">
        <slot name="footer-actions" />
        <YmButton
          v-if="primaryLabel"
          variant="primary"
          size="lg"
          :icon="primaryIcon"
          :disabled="primaryDisabled"
          :loading="primaryLoading"
          :title="primaryShortcut ? t('creation.footer.shortcut') : undefined"
          @click="emit('primary')"
        >
          {{ primaryLabel }}
        </YmButton>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.wb-page {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-height: 0;
}
.wb-page__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  row-gap: var(--space-2);
  padding: var(--space-5) var(--space-6) var(--space-3);
  flex: none;
}
.wb-page__title {
  font-size: var(--font-size-xl);
}
.wb-page__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  row-gap: var(--space-2);
  gap: var(--space-2);
}
.wb-page__cols {
  display: grid;
  grid-template-columns: minmax(300px, 380px) minmax(0, 1fr);
  gap: var(--space-4);
  padding: 0 var(--space-6) var(--space-6);
  flex: 1;
  min-height: 0;
}
.wb-page__col {
  min-width: 0;
  min-height: 0;
  overflow: auto;
}
.wb-page__col.wb-page__results {
  display: flex;
  flex-direction: column;
}
.wb-page__results :deep(.results-stack) {
  /* 结果列自身滚动；栈和卡片不得被压缩，否则日志/节点会被卡片 overflow 裁掉。 */
  flex: 1 0 auto;
  min-height: 0;
}
.wb-page__results :deep(.results-stack > *) {
  flex: none;
}
.wb-page__results :deep(.results-empty) {
  display: flex;
  flex: 1;
  min-height: 0;
}
.wb-page__results :deep(.results-empty > .ym-empty) {
  flex: 1;
}
.wb-page__results :deep(.card--fill) {
  flex: 1;
  min-height: 0;
}
.wb-page__actions :deep(.ym-stepper) {
  flex-wrap: wrap;
  row-gap: var(--space-1);
}

/* 显式 scaffold 版式：共享页头 + 可选 subheader + 吸底 footer。 */
.wb-page--scaffold .wb-page__head,
.wb-page--scaffold .wb-page__subheader,
.wb-page--scaffold .wb-page__cols {
  width: 100%;
  max-width: 1240px;
  margin-inline: auto;
}
.wb-page--scaffold .wb-page__head {
  display: block;
  padding: var(--space-6) var(--page-gutter) 0;
}
.wb-page--scaffold .wb-page__subheader {
  padding: 14px var(--page-gutter) 0;
  flex: none;
}
.wb-page--scaffold .wb-page__subheader + .wb-page__cols {
  padding-block-start: 18px;
}
.wb-page--scaffold .wb-page__cols {
  padding: 24px var(--page-gutter) var(--space-6);
}
.wb-page--scaffold .wb-page__footer {
  position: sticky;
  bottom: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  flex: none;
  padding: var(--space-3) var(--page-gutter);
  background: color-mix(in srgb, var(--color-surface) 88%, transparent);
  backdrop-filter: blur(10px);
  border-block-start: 1px solid var(--color-border);
}
.wb-page__footer-status {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.wb-page__footer-actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
}
@media (max-width: 1000px) {
  .wb-page--scaffold .wb-page__head {
    padding-block-start: var(--space-5);
  }
  .wb-page--scaffold .wb-page__subheader {
    padding-block-start: var(--space-3);
  }
  .wb-page--scaffold .wb-page__cols {
    grid-template-columns: minmax(0, 1fr);
    padding-block: var(--space-4);
  }
  .wb-page--scaffold .wb-page__footer {
    flex-wrap: wrap;
  }
}
</style>

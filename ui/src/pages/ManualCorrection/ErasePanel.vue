<script setup lang="ts">
// 擦除校正面板（只读）：契约只允许擦除块随 revision 存取，真实擦除执行
// 等待 05/04 能力，因此这里只展示已存块，不提供绘制、编辑或批量执行控件。
import { t, tCount } from "@/i18n";
import type { EraseBlock } from "@/domain/types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmIcon from "@/components/ui/YmIcon.vue";

defineProps<{
  blocks: EraseBlock[];
}>();
</script>

<template>
  <section class="erase-panel">
    <header class="erase-panel__head">
      <div class="erase-panel__headline">
        <h2 class="erase-panel__title">{{ t("mcs.erase.title") }}</h2>
        <YmBadge tone="info">{{ t("mcs.erase.readOnly") }}</YmBadge>
      </div>
      <p class="erase-panel__tip">
        <YmIcon name="info" :size="15" />
        <span>{{ t("mcs.erase.pending") }}</span>
      </p>
    </header>

    <p v-if="blocks.length > 0" class="erase-panel__count">
      {{ tCount("mcs.erase.blocks", blocks.length) }}
    </p>
    <p v-else class="erase-panel__count">{{ t("mcs.erase.noBlocks") }}</p>

    <ul v-if="blocks.length > 0" class="erase-panel__list">
      <li
        v-for="(block, index) in blocks"
        :key="block.id ?? index"
        class="erase-panel__block"
      >
        <div class="erase-panel__block-head">
          <span class="erase-panel__block-name">{{ block.id ?? `#${index + 1}` }}</span>
          <span class="erase-panel__block-source">
            {{ block.source === "source" ? t("mcs.stage.source") : t("mcs.stage.clean") }}
          </span>
        </div>
        <dl class="erase-panel__facts">
          <div class="erase-panel__fact">
            <dt>{{ t("pipeline.editor.start") }}–{{ t("pipeline.editor.end") }}</dt>
            <dd dir="ltr">{{ block.startMs }}–{{ block.endMs }} ms</dd>
          </div>
        </dl>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.erase-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.erase-panel__head {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--color-border);
}
.erase-panel__headline {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.erase-panel__title {
  margin: 0;
  font-size: var(--font-size-lg);
  font-weight: 600;
  color: var(--color-text);
}
.erase-panel__tip {
  display: flex;
  gap: var(--space-2);
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid color-mix(in srgb, var(--color-primary) 14%, var(--color-border));
  border-radius: var(--radius-control);
  background: var(--gradient-brand-soft);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  line-height: 1.5;
}
.erase-panel__tip svg {
  flex: none;
  margin-top: 1px;
  color: var(--color-primary);
}
.erase-panel__count {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.erase-panel__list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.erase-panel__block {
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-surface);
}
.erase-panel__block-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.erase-panel__block-name {
  min-width: 0;
  overflow-wrap: anywhere;
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--color-text);
}
.erase-panel__block-source {
  margin-inline-start: auto;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  background: var(--color-surface-3);
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
}
.erase-panel__facts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  margin: var(--space-2) 0 0;
}
.erase-panel__fact {
  display: flex;
  align-items: baseline;
  gap: var(--space-1);
  min-width: 0;
  margin: 0;
}
.erase-panel__fact dt {
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.erase-panel__fact dd {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-xs);
  font-variant-numeric: tabular-nums;
}
</style>

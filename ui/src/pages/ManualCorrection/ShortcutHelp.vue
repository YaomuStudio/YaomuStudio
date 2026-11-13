<script setup lang="ts">
// 快捷键帮助：只列出本页真实接线的快捷键，不描述未实现的编辑/擦除操作。
import { computed } from "vue";
import { t } from "@/i18n";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";

defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", value: boolean): void }>();

/** 文案在 computed 内用字面量键解析：切换语言时重新翻译。 */
const rows = computed(() => [
  { keys: ["⌘/Ctrl", "S"], label: t("mcs.save") },
  { keys: ["⌘/Ctrl", "Enter"], label: t("mcs.remix.title") },
  { keys: ["⌘/Ctrl", "Z"], label: t("mcs.undo") },
  { keys: ["⌘/Ctrl", "Shift", "Z"], label: t("mcs.redo") },
  { keys: ["1", "2", "3"], label: t("mcs.sc.tabs") },
  { keys: ["?"], label: t("mcs.sc.help") },
]);
</script>

<template>
  <YmDialog
    :model-value="modelValue"
    :title="t('mcs.shortcuts')"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="sc">
      <div v-for="row in rows" :key="row.label" class="sc__row">
        <span class="sc__keys">
          <template v-for="(key, index) in row.keys" :key="key">
            <i v-if="index > 0">+</i>
            <kbd>{{ key }}</kbd>
          </template>
        </span>
        <span class="sc__desc">{{ row.label }}</span>
      </div>
    </div>
    <template #footer="{ close }">
      <YmButton variant="primary" @click="close">{{ t("common.close") }}</YmButton>
    </template>
  </YmDialog>
</template>

<style scoped>
.sc {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sc__row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  min-height: 34px;
  padding: 3px 6px;
  border-radius: var(--radius-md);
}
.sc__row:hover {
  background: var(--color-surface-2);
}
.sc__keys {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: none;
  width: 200px;
}
.sc__keys i {
  color: var(--color-text-faint);
  font-style: normal;
  font-size: var(--font-size-xs);
}
.sc__desc {
  min-width: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
kbd {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 26px;
  height: 26px;
  padding: 0 8px;
  border: 1px solid var(--color-border-strong);
  border-bottom-width: 2px;
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: var(--font-size-xs);
  line-height: 1;
  white-space: nowrap;
}
</style>

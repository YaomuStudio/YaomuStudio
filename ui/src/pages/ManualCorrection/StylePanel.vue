<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import type { SubtitleStyle } from "@/domain/types";
import YmSelect from "@/components/ui/YmSelect.vue";

const props = withDefaults(
  defineProps<{
    modelValue: SubtitleStyle;
    enabled: boolean;
    /** 创建页只编辑自定义样式，不展示“沿用任务样式”开关；Boolean prop 缺省会被 Vue 转成 false，必须显式默认 true。 */
    showInherit?: boolean;
    /** 覆盖标题文案；默认是人工校正的“当前版本的字幕样式”。 */
    title?: string;
  }>(),
  { showInherit: true },
);

const emit = defineEmits<{
  "update:modelValue": [value: SubtitleStyle];
  "update:enabled": [value: boolean];
}>();

function update(patch: Partial<SubtitleStyle>): void {
  emit("update:modelValue", { ...props.modelValue, ...patch });
}

function numberValue(event: Event): number | undefined {
  const raw = (event.target as HTMLInputElement).value.trim();
  if (raw === "") return undefined;
  const value = Number(raw);
  return Number.isFinite(value) ? value : undefined;
}

function onNumber(field: keyof SubtitleStyle, event: Event): void {
  update({ [field]: numberValue(event) } as Partial<SubtitleStyle>);
}

function onText(field: keyof SubtitleStyle, event: Event): void {
  const value = (event.target as HTMLInputElement).value.trim();
  update({ [field]: value === "" ? undefined : value } as Partial<SubtitleStyle>);
}

function onBoolean(field: keyof SubtitleStyle, event: Event): void {
  update({ [field]: (event.target as HTMLInputElement).checked } as Partial<SubtitleStyle>);
}

const alignOptions = computed(() => [
  { value: "", label: t("mcs.style.alignDefault") },
  { value: "left", label: t("mcs.style.alignLeft") },
  { value: "center", label: t("mcs.style.alignCenter") },
  { value: "right", label: t("mcs.style.alignRight") },
]);

function onAlign(value: string): void {
  update({ align: value === "" ? undefined : (value as SubtitleStyle["align"]) });
}

function updateOutline(patch: { color?: string; width?: number }): void {
  update({ outline: { ...(props.modelValue.outline ?? {}), ...patch } });
}

function updateShadow(patch: { color?: string; blur?: number; x?: number; y?: number }): void {
  update({ shadow: { ...(props.modelValue.shadow ?? {}), ...patch } });
}
</script>

<template>
  <section class="style-panel" :class="{ 'is-disabled': !enabled }">
    <h2 class="style-panel__title">{{ title ?? t("mcs.style.title") }}</h2>
    <label v-if="showInherit !== false" class="style-panel__toggle">
      <input
        type="checkbox"
        :checked="enabled"
        @change="emit('update:enabled', ($event.target as HTMLInputElement).checked)"
      />
      <span>{{ t("mcs.style.inherit") }}</span>
    </label>
    <p v-if="showInherit !== false && !enabled" class="style-panel__hint">
      {{ t("mcs.style.inherited") }}
    </p>

    <div class="style-grid">
      <label>
        <span>{{ t("mcs.style.x") }}</span>
        <input
          type="number"
          step="0.01"
          min="0"
          :disabled="!enabled"
          :value="modelValue.x ?? ''"
          @input="onNumber('x', $event)"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.y") }}</span>
        <input
          type="number"
          step="0.01"
          min="0"
          :disabled="!enabled"
          :value="modelValue.y ?? ''"
          @input="onNumber('y', $event)"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.fontSize") }}</span>
        <input
          type="number"
          step="1"
          min="6"
          max="200"
          :disabled="!enabled"
          :value="modelValue.fontSize ?? ''"
          @input="onNumber('fontSize', $event)"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.lineSpacing") }}</span>
        <input
          type="number"
          step="0.01"
          min="0"
          :disabled="!enabled"
          :value="modelValue.lineSpacing ?? ''"
          @input="onNumber('lineSpacing', $event)"
        />
      </label>
      <label class="style-grid__wide">
        <span>{{ t("mcs.style.font") }}</span>
        <input
          type="text"
          maxlength="64"
          :disabled="!enabled"
          :value="modelValue.font ?? ''"
          @input="onText('font', $event)"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.align") }}</span>
        <YmSelect
          :disabled="!enabled"
          :model-value="modelValue.align ?? ''"
          :options="alignOptions"
          @update:model-value="onAlign"
        />
      </label>
      <label class="style-panel__check">
        <input
          type="checkbox"
          :disabled="!enabled"
          :checked="Boolean(modelValue.bold)"
          @change="onBoolean('bold', $event)"
        />
        <span>{{ t("mcs.style.bold") }}</span>
      </label>
      <label class="style-grid__wide">
        <span>{{ t("mcs.style.background") }}</span>
        <input
          type="text"
          :disabled="!enabled"
          :value="modelValue.background ?? ''"
          @input="onText('background', $event)"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.maxLines") }}</span>
        <input
          type="number"
          step="1"
          min="1"
          max="4"
          :disabled="!enabled"
          :value="modelValue.maxTranslationLines ?? ''"
          @input="onNumber('maxTranslationLines', $event)"
        />
      </label>
    </div>

    <h3 class="style-panel__heading">{{ t("mcs.style.outline") }}</h3>
    <div class="style-grid">
      <label>
        <span>{{ t("mcs.style.outlineColor") }}</span>
        <input
          type="text"
          :disabled="!enabled"
          :value="modelValue.outline?.color ?? ''"
          @input="updateOutline({ color: ($event.target as HTMLInputElement).value || undefined })"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.outlineWidth") }}</span>
        <input
          type="number"
          step="0.01"
          min="0"
          :disabled="!enabled"
          :value="modelValue.outline?.width ?? ''"
          @input="updateOutline({ width: numberValue($event) })"
        />
      </label>
    </div>

    <h3 class="style-panel__heading">{{ t("mcs.style.shadow") }}</h3>
    <div class="style-grid">
      <label>
        <span>{{ t("mcs.style.shadowColor") }}</span>
        <input
          type="text"
          :disabled="!enabled"
          :value="modelValue.shadow?.color ?? ''"
          @input="updateShadow({ color: ($event.target as HTMLInputElement).value || undefined })"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.shadowBlur") }}</span>
        <input
          type="number"
          step="0.01"
          min="0"
          :disabled="!enabled"
          :value="modelValue.shadow?.blur ?? ''"
          @input="updateShadow({ blur: numberValue($event) })"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.shadowX") }}</span>
        <input
          type="number"
          step="0.01"
          :disabled="!enabled"
          :value="modelValue.shadow?.x ?? ''"
          @input="updateShadow({ x: numberValue($event) })"
        />
      </label>
      <label>
        <span>{{ t("mcs.style.shadowY") }}</span>
        <input
          type="number"
          step="0.01"
          :disabled="!enabled"
          :value="modelValue.shadow?.y ?? ''"
          @input="updateShadow({ y: numberValue($event) })"
        />
      </label>
    </div>
  </section>
</template>

<style scoped>
.style-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}
.style-panel.is-disabled {
  opacity: 0.72;
}
.style-panel__title {
  margin: 0;
  font-size: var(--font-size-md);
  color: var(--color-text);
}
.style-panel__toggle {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-size-sm);
}
.style-panel__hint {
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.style-panel__heading {
  margin: var(--space-2) 0 0;
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.style-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2) var(--space-3);
}
.style-grid label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}
.style-grid__wide {
  grid-column: 1 / -1;
}
.style-grid input {
  min-width: 0;
  padding: 6px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg);
  color: var(--color-text);
  font-size: var(--font-size-sm);
}
.style-panel__check {
  flex-direction: row !important;
  align-items: center;
  gap: var(--space-2) !important;
}
</style>

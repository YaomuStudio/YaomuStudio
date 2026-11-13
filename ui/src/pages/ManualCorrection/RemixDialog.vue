<script setup lang="ts">
// 重新合成弹窗（参考 UI 分支的弹窗视觉）。
//
// 只确认当前语种：保存 dirty 草稿 → 精确匹配当前 revision 的 translation 审核绑定
// （携带 currentBinding 交给 Core 复核）→ 轮询 07/08 → 等待人工交付审核。
// 多语批量、双语开关与模拟合成不在本轮范围；宿主 Index 负责全部真实逻辑。
import { t } from "@/i18n";
import YmButton from "@/components/ui/YmButton.vue";
import YmDialog from "@/components/ui/YmDialog.vue";
import YmSpinner from "@/components/ui/YmSpinner.vue";

defineProps<{
  modelValue: boolean;
  remixing: boolean;
  done: boolean;
  subtitleChanges: number;
  styleChanged: boolean;
  /** 当前源 revision 与译文记录的 input_revision_id 不一致。 */
  sourceStale?: boolean;
  /** 旧译文没有 input_revision_id，无法确认基于哪版源文。 */
  sourceUnknown?: boolean;
  languageLabel: string;
  canSubmit: boolean;
  statusText: string;
  errorText: string;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", v: boolean): void;
  (e: "confirm"): void;
  (e: "go-delivery"): void;
}>();
</script>

<template>
  <YmDialog
    :model-value="modelValue"
    :title="t('mcs.remix.title')"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="remix">
      <p class="remix__summary">
        <span>{{ t("mcs.remix.language", { language: languageLabel }) }}</span>
        <span class="remix__sep" aria-hidden="true">·</span>
        <span>{{ t("mcs.remix.changedSegments", { count: subtitleChanges }) }}</span>
        <template v-if="styleChanged">
          <span class="remix__sep" aria-hidden="true">·</span>
          <span>{{ t("mcs.remix.styleChanged") }}</span>
        </template>
      </p>
      <p class="remix__consequence">{{ t("mcs.remix.consequence") }}</p>
      <p v-if="sourceStale || sourceUnknown" class="remix__warning" role="alert">
        {{ sourceStale ? t("mcs.remix.sourceChanged") : t("mcs.remix.sourceUnknown") }}
      </p>
      <p v-if="remixing" class="remix__running" role="status">
        <YmSpinner :size="14" />
        <bdi>{{ statusText || t("mcs.remix.started") }}</bdi>
      </p>
      <p v-else-if="statusText" class="remix__status" role="status">
        <bdi>{{ statusText }}</bdi>
      </p>
      <p v-if="errorText" class="remix__error" role="alert">
        <bdi>{{ errorText }}</bdi>
      </p>
    </div>
    <template #footer>
      <YmButton v-if="done" variant="primary" @click="emit('go-delivery')">
        {{ t("mcs.remix.goDelivery") }}
      </YmButton>
      <YmButton
        v-else-if="!remixing"
        variant="secondary"
        @click="emit('update:modelValue', false)"
      >
        {{ t("mcs.remix.cancel") }}
      </YmButton>
      <YmButton
        v-if="!done"
        variant="primary"
        :loading="remixing"
        :disabled="remixing || !canSubmit"
        @click="emit('confirm')"
      >
        {{ t("mcs.remix.confirm") }}
      </YmButton>
    </template>
  </YmDialog>
</template>

<style scoped>
.remix {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.remix__summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: 1.6;
}
.remix__sep {
  color: var(--color-text-faint);
}
.remix__consequence {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
  line-height: 1.7;
}
.remix__warning {
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--radius-md);
  background: var(--color-warning-soft);
  color: var(--color-warning-strong);
  font-size: var(--font-size-sm);
  line-height: 1.6;
}
.remix__running {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  color: var(--color-primary-ink);
  font-size: var(--font-size-sm);
}
.remix__status {
  margin: 0;
  padding: var(--space-2);
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.remix__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
</style>

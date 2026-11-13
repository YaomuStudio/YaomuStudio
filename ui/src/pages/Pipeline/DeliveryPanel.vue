<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import type { NodeStatus } from "@/domain/types";
import { nodeStatusLabelKey, nodeStatusTone } from "@/domain/status";
import type { SelectOption, TrackChoice } from "./types";
import YmBadge from "@/components/ui/YmBadge.vue";
import YmButton from "@/components/ui/YmButton.vue";
import YmCheckbox from "@/components/ui/YmCheckbox.vue";
import YmIcon from "@/components/ui/YmIcon.vue";
import YmSegmented from "@/components/ui/YmSegmented.vue";

const props = defineProps<{
  trackChoice: TrackChoice;
  confirmed: boolean;
  reviewStatus: NodeStatus;
  /** 当前语言是否已有 08 预览资产；没有时明确空态，不拿源媒体冒充。 */
  previewAvailable: boolean;
  previewLoading: boolean;
  /** 预览播放器当前是否在播放（用于 play/pause 文案）。 */
  playing: boolean;
  /** 交付确认的本地阻断原因（未保存/保存中/非法草稿/未知投影）；存在时禁用确认。 */
  blockedReason?: string;
  /** 当前语种当前交付绑定的聚合提示；不冒充已审或自动批量批准。 */
  reviewHint?: string;
}>();

const emit = defineEmits<{
  (e: "update:trackChoice", v: string): void;
  (e: "update:confirmed", v: boolean): void;
  (e: "preview"): void;
}>();

// 只有“原声 vs 最终混音”两项；独立配音轨/三轨 A/B 没有后端契约，不做假选项。
const trackOptions = computed<SelectOption[]>(() => [
  { value: "original", label: t("pipeline.delivery.trackOriginal") },
  { value: "preview", label: t("pipeline.delivery.trackPreview") },
]);

const reviewLabel = computed(() => t(nodeStatusLabelKey(props.reviewStatus)));
const reviewTone = computed(() => nodeStatusTone(props.reviewStatus));

const playLabel = computed(() => {
  if (props.playing) return t("pipeline.playback.pause");
  return t(
    props.trackChoice === "preview"
      ? "pipeline.delivery.playPreview"
      : "pipeline.delivery.playOriginal",
  );
});

const playDisabled = computed(
  () => props.previewLoading || (props.trackChoice === "preview" && !props.previewAvailable),
);

function onTrack(v: string): void {
  emit("update:trackChoice", v);
}
function onConfirmed(v: boolean): void {
  emit("update:confirmed", v);
}
function onPreview(): void {
  emit("preview");
}
</script>

<template>
  <section class="delivery">
    <header class="delivery__head">
      <div class="delivery__titles">
        <h2 class="delivery__title">{{ t("pipeline.delivery.title") }}</h2>
        <p class="delivery__desc">{{ t("pipeline.delivery.desc") }}</p>
      </div>
      <div class="delivery__review">
        <span class="delivery__review-label">{{ t("pipeline.delivery.reviewStatus") }}</span>
        <YmBadge :tone="reviewTone">{{ reviewLabel }}</YmBadge>
        <span
          v-if="reviewHint"
          class="delivery__review-hint"
          data-testid="delivery-review-hint"
        >
          {{ reviewHint }}
        </span>
      </div>
    </header>

    <div class="delivery__ab">
      <YmSegmented
        :model-value="trackChoice"
        :options="trackOptions"
        @update:model-value="onTrack"
      />
    </div>

    <div class="preview">
      <slot name="player">
        <YmIcon name="film" :size="40" />
        <p class="preview__text">{{ t("pipeline.delivery.previewHint") }}</p>
      </slot>
      <p v-if="previewLoading" class="preview__note">
        {{ t("pipeline.delivery.previewLoading") }}
      </p>
      <p v-else-if="!previewAvailable" class="preview__empty">
        {{ t("pipeline.delivery.noPreview") }}
      </p>
      <YmButton
        variant="secondary"
        size="sm"
        :icon="playing ? 'pause' : 'play'"
        :disabled="playDisabled"
        @click="onPreview"
      >
        {{ playLabel }}
      </YmButton>
    </div>

    <p class="delivery__pending">{{ t("pipeline.delivery.previewPending") }}</p>
    <p class="delivery__blocked">{{ t("pipeline.delivery.modelsBlocked") }}</p>

    <div class="delivery__confirm">
      <YmCheckbox
        :model-value="confirmed"
        :label="t('pipeline.delivery.confirm')"
        :disabled="Boolean(blockedReason) && !confirmed"
        @update:model-value="onConfirmed"
      />
      <p v-if="blockedReason" class="delivery__blocked-reason">
        <bdi>{{ blockedReason }}</bdi>
      </p>
      <p v-else class="delivery__confirm-hint">{{ t("pipeline.delivery.confirmHint") }}</p>
    </div>
  </section>
</template>

<style scoped>
.delivery {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}
.delivery__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.delivery__titles {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.delivery__title {
  font-size: var(--font-size-lg);
}
.delivery__desc {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}
.delivery__review {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
  flex: none;
}
.delivery__review-label {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.delivery__review-hint {
  max-width: 220px;
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
  text-align: end;
}
.delivery__ab {
  display: flex;
}
.preview {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  min-height: 180px;
  padding: var(--space-5);
  background: var(--color-media);
  color: var(--color-media-text);
  border-radius: var(--radius-md);
}
.preview__text {
  font-size: var(--font-size-sm);
  opacity: 0.75;
}
.preview__note,
.preview__empty {
  font-size: var(--font-size-xs);
  opacity: 0.75;
  text-align: center;
}
.delivery__pending,
.delivery__blocked {
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.delivery__confirm {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.delivery__confirm-hint {
  padding-left: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-text-faint);
}
.delivery__blocked-reason {
  padding-inline-start: 26px;
  font-size: var(--font-size-xs);
  color: var(--color-warning, var(--color-text-faint));
  line-height: var(--line-height-normal);
}
</style>

<script setup lang="ts">
// 校正舞台：源/成品媒体预览 + 字幕定位条。
//
// 媒体一律经桌面桥 assetUrl 生成的 PlayerSource 播放（由页面持有单一播放状态），
// 组件不自拼资产 URL、不伪造播放；没有可用资产或没有桌面桥时显示明确空态。
// 擦除绘制与帧级时间轴不在本轮范围。
import { computed } from "vue";
import { formatTimestamp, t, tCount } from "@/i18n";
import type { PlayerSource } from "@/components/media/types";
import AssetPlayer from "@/components/media/AssetPlayer.vue";
import YmIcon from "@/components/ui/YmIcon.vue";

interface StageSegment {
  id: string;
  startMs: number;
  endMs: number;
  text: string;
}

const props = defineProps<{
  source: PlayerSource | null;
  playing: boolean;
  mode: "source" | "final";
  /** 成品资产是否存在；不存在时成品页签禁用并在舞台说明原因。 */
  finalAvailable: boolean;
  finalUnavailableLabel: string;
  sourceLabel: string;
  finalLabel: string;
  emptyTitle: string;
  emptyDescription: string;
  segments: StageSegment[];
  selectedId: string | null;
  currentMs: number;
  durationMs: number;
  subtitleText: string;
  subtitleStyle: Record<string, string>;
  auditionLabel: string;
  languageLabel: string;
  durationLabel: string;
  segmentCount: number;
}>();

const emit = defineEmits<{
  (e: "update:playing", value: boolean): void;
  (e: "update:mode", value: "source" | "final"): void;
  (e: "update:currentMs", value: number): void;
  (e: "seekSegment", id: string): void;
  (e: "error", messageKey: string): void;
}>();

const hasVideo = computed(() => props.source?.kind === "video");
const hasAudio = computed(() => props.source?.kind === "audio");
const activeSegment = computed(() =>
  props.segments.find(
    (segment) => props.currentMs >= segment.startMs && props.currentMs <= segment.endMs,
  ),
);
const showSubtitle = computed(
  () =>
    props.mode === "final" &&
    hasVideo.value &&
    !hasAudio.value &&
    Boolean(activeSegment.value) &&
    props.subtitleText.trim() !== "",
);

function offsetPercent(ms: number): number {
  if (props.durationMs <= 0) return 0;
  return Math.max(0, Math.min(100, (ms / props.durationMs) * 100));
}

function segmentStyle(segment: StageSegment): Record<string, string> {
  const left = offsetPercent(segment.startMs);
  const width = offsetPercent(segment.endMs) - left;
  return {
    left: `${left}%`,
    width: `${Math.max(0.5, width)}%`,
  };
}

function isActive(segment: StageSegment): boolean {
  return props.currentMs >= segment.startMs && props.currentMs <= segment.endMs;
}

function setMode(mode: "source" | "final"): void {
  // 试听候选时舞台由单一播放状态接管音频；再点当前页签应回到该视频资产。
  if (mode === props.mode && !hasAudio.value) return;
  if (mode === "final" && !props.finalAvailable) return;
  emit("update:mode", mode);
}

function onPlaying(value: boolean): void {
  emit("update:playing", value);
}
function onCurrentMs(value: number): void {
  emit("update:currentMs", value);
}
function onError(messageKey: string): void {
  emit("update:playing", false);
  emit("error", messageKey);
}
</script>

<template>
  <div class="stage">
    <header class="stage__head">
      <div class="stage__modes" role="group" :aria-label="t('mcs.preview')">
        <button
          type="button"
          class="stage__mode"
          :class="{ 'is-active': mode === 'source' }"
          :aria-pressed="mode === 'source'"
          @click="setMode('source')"
        >
          {{ sourceLabel }}
        </button>
        <button
          type="button"
          class="stage__mode"
          :class="{ 'is-active': mode === 'final' }"
          :aria-pressed="mode === 'final'"
          :disabled="!finalAvailable"
          :title="!finalAvailable ? finalUnavailableLabel : undefined"
          @click="setMode('final')"
        >
          {{ finalLabel }}
        </button>
      </div>
      <span v-if="source" class="stage__time mono" dir="ltr">
        {{ formatTimestamp(currentMs) }} / {{ formatTimestamp(durationMs) }}
      </span>
    </header>

    <div class="stage__viewport">
      <AssetPlayer
        v-if="source"
        :source="source"
        :playing="playing"
        visible
        :aria-label="t('mcs.preview')"
        @update:playing="onPlaying"
        @update:currentMs="onCurrentMs"
        @error="onError"
      />
      <div v-else class="stage__empty">
        <YmIcon name="film" :size="40" />
        <p class="stage__empty-title">{{ emptyTitle }}</p>
        <p class="stage__empty-desc">{{ emptyDescription }}</p>
      </div>

      <p v-if="hasAudio" class="stage__audio-label">
        <YmIcon name="volume" :size="14" />
        <bdi>{{ auditionLabel || t("mcs.preview") }}</bdi>
      </p>

      <p v-if="showSubtitle" class="stage__subtitle" :style="subtitleStyle" dir="auto">
        {{ subtitleText }}
      </p>
    </div>

    <footer class="stage__foot">
      <div class="mcs-meta">
        <span>{{ t("mcs.source") }}：<bdi>{{ languageLabel }}</bdi></span>
        <span>{{ t("mcs.duration") }}：<bdi dir="ltr">{{ durationLabel }}</bdi></span>
        <span><bdi>{{ tCount("mcs.segments", segmentCount) }}</bdi></span>
      </div>
      <div class="stage__locator" :aria-label="t('mcs.preview')">
        <button
          v-for="(segment, index) in segments"
          :key="segment.id"
          type="button"
          class="stage__loc"
          :class="{
            'is-active': isActive(segment),
            'is-selected': selectedId === segment.id,
          }"
          :style="segmentStyle(segment)"
          :title="segment.text"
          :aria-label="t('pipeline.editor.rowLabel', { index: index + 1 })"
          @click="emit('seekSegment', segment.id)"
        ></button>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.stage {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  height: 100%;
  min-height: 0;
  padding: var(--space-4);
}
.stage__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  flex: none;
}
.stage__modes {
  display: inline-flex;
  gap: 3px;
  padding: 3px;
  border-radius: var(--radius-md);
  background: var(--color-surface-2);
}
.stage__mode {
  padding: 5px 12px;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.stage__mode.is-active {
  background: var(--color-surface);
  color: var(--color-text);
  font-weight: 500;
  box-shadow: var(--shadow-sm);
}
.stage__mode:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.stage__time {
  flex: none;
  color: var(--color-text-faint);
  font-size: var(--font-size-xs);
}
.stage__viewport {
  position: relative;
  flex: 1;
  min-height: 260px;
  display: flex;
  align-items: center;
  border-radius: var(--radius-control);
  overflow: hidden;
  background: var(--color-media);
  color: var(--color-media-text);
}
.stage__viewport :deep(.asset-player) {
  height: 100%;
  justify-content: center;
}
.stage__viewport :deep(video),
.stage__viewport :deep(audio) {
  width: 100%;
}
.stage__viewport :deep(video) {
  height: 100%;
  max-height: none;
  border-radius: 0;
  object-fit: contain;
}
.stage__empty {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-5);
  text-align: center;
}
.stage__empty-title {
  margin: 0;
  font-size: var(--font-size-md);
  color: var(--color-media-text);
}
.stage__empty-desc {
  margin: 0;
  max-width: 320px;
  /* 媒体区是深色底：使用媒体前景色的降级混合，保证小字号仍达 WCAG AA。 */
  color: color-mix(in srgb, var(--color-media-text) 78%, transparent);
  font-size: var(--font-size-sm);
  line-height: 1.5;
}
.stage__audio-label {
  position: absolute;
  inset-inline: 0;
  top: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  margin: 0;
  padding: 6px var(--space-3);
  background: color-mix(in srgb, var(--color-media) 82%, transparent);
  color: var(--color-media-text);
  font-size: var(--font-size-xs);
  pointer-events: none;
}
.stage__subtitle {
  position: absolute;
  left: 8%;
  right: 8%;
  bottom: 12%;
  margin: 0;
  line-height: 1.4;
  pointer-events: none;
  text-shadow: 0 1px 3px rgba(0, 0, 0, 0.75);
  white-space: pre-wrap;
}
.stage__foot {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  flex: none;
}
.mcs-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}
.stage__locator {
  position: relative;
  height: 12px;
  border-radius: var(--radius-sm);
  background: var(--color-surface-2);
  overflow: hidden;
}
.stage__loc {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-primary) 32%, transparent);
}
.stage__loc.is-selected {
  background: color-mix(in srgb, var(--color-primary) 55%, transparent);
}
.stage__loc.is-active {
  background: var(--color-primary);
}
</style>

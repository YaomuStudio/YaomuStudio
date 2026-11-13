<script setup lang="ts">
import { ref } from "vue";
import YmIcon from "./YmIcon.vue";

withDefaults(
  defineProps<{
    name: string;
    size?: string;
    icon?: string;
    thumb?: string;
    video?: string;
    mediaKind?: "video" | "audio" | "other";
    thumbAlt?: string;
  }>(),
  { icon: "file", thumbAlt: "", mediaKind: "other" },
);

const videoEl = ref<HTMLVideoElement | null>(null);
const videoFailed = ref(false);

/**
 * 部分容器首帧未解码，轻微 seek 后浏览器才会绘制缩略图。
 * 失败时静默保留视频占位，不影响上传主流程。
 */
function showFirstFrame(): void {
  const video = videoEl.value;
  if (!video) return;
  try {
    const duration = Number.isFinite(video.duration) ? video.duration : 0;
    video.currentTime = duration > 0 ? Math.min(0.05, duration / 2) : 0;
  } catch {
    // 忽略 seek 失败。
  }
}
</script>

<template>
  <div class="ym-file-row">
    <slot name="leading">
      <video
        v-if="video && !videoFailed"
        ref="videoEl"
        class="ym-file-row__thumb ym-file-row__thumb--video"
        :src="video"
        muted
        playsinline
        preload="metadata"
        @loadedmetadata="showFirstFrame"
        @error="videoFailed = true"
      ></video>
      <span
        v-else-if="mediaKind === 'audio'"
        class="ym-file-row__thumb ym-file-row__thumb--audio"
        aria-hidden="true"
      >
        <YmIcon name="wave" :size="26" />
      </span>
      <img v-else-if="thumb" class="ym-file-row__thumb" :src="thumb" :alt="thumbAlt" />
      <span v-else class="ym-file-row__icon">
        <YmIcon :name="icon" :size="18" />
      </span>
    </slot>
    <span class="ym-file-row__main">
      <span class="ym-file-row__name" :title="name"><bdi dir="auto">{{ name }}</bdi></span>
      <span v-if="size" class="ym-file-row__size">{{ size }}</span>
    </span>
    <slot name="between" />
    <slot name="actions" />
  </div>
</template>

<style scoped>
.ym-file-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  min-height: 66px;
  padding: 10px 12px;
  background: color-mix(in srgb, var(--color-primary) 4%, var(--color-surface));
  border: 1px solid color-mix(in srgb, var(--color-primary) 22%, var(--color-border));
  border-radius: var(--radius-control);
}
/* 视频首帧按真实比例展示：横屏约 120×68，竖屏自动收窄。 */
.ym-file-row__thumb {
  height: 68px;
  width: auto;
  max-width: 120px;
  min-width: 36px;
  flex: none;
  border-radius: var(--radius-md);
  object-fit: contain;
  background: var(--color-media);
}
.ym-file-row__thumb--video {
  display: block;
}
.ym-file-row__thumb--audio {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 120px;
  color: var(--color-primary);
  background:
    radial-gradient(120% 120% at 50% 0%, var(--color-primary-soft), transparent 70%),
    var(--color-surface-2);
  border: 1px solid var(--color-border);
}
.ym-file-row__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  border-radius: var(--radius-md);
  background: var(--color-primary-soft);
  color: var(--color-primary);
}
.ym-file-row__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ym-file-row__name {
  display: -webkit-box;
  min-width: 0;
  overflow: hidden;
  color: var(--color-text);
  font-size: var(--font-size-sm);
  font-weight: 500;
  line-height: 1.45;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}
.ym-file-row__size {
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  white-space: nowrap;
}
</style>

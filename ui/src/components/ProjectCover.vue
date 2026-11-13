<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { apiMode } from "@/api";
import defaultCover from "@/assets/workbench/cover-default.png";

const props = defineProps<{ assetId?: string }>();

const poster = ref("");
const failed = ref(false);
const videoEl = ref<HTMLVideoElement | null>(null);

const canUseAssetUrl = apiMode === "bridge";
const src = computed(() =>
  canUseAssetUrl && props.assetId
    ? `yaomu-asset://asset/${encodeURIComponent(props.assetId)}`
    : "",
);

watch(
  () => props.assetId,
  () => {
    poster.value = "";
    failed.value = false;
  },
);

onBeforeUnmount(() => {
  const video = videoEl.value;
  if (video) {
    video.pause();
    video.removeAttribute("src");
    video.load();
  }
});

function onLoadedMetadata(): void {
  const video = videoEl.value;
  if (!video) return;
  try {
    // 轻微 seek 避开部分容器首帧未解码的情况；失败时仍由 loadeddata 兜底。
    video.currentTime = Math.min(0.05, Math.max(0, video.duration || 0));
  } catch {
    // 忽略 seek 失败，等待 loadeddata。
  }
}

function drawFrame(): void {
  if (poster.value || failed.value) return;
  const video = videoEl.value;
  if (!video || video.readyState < 2) return;
  const vw = video.videoWidth;
  const vh = video.videoHeight;
  if (!vw || !vh) return;

  const targetRatio = 16 / 9;
  let sx = 0;
  let sy = 0;
  let sw = vw;
  let sh = vh;
  if (vw / vh > targetRatio) {
    sw = vh * targetRatio;
    sx = (vw - sw) / 2;
  } else {
    sh = vw / targetRatio;
    sy = (vh - sh) / 2;
  }

  const canvas = document.createElement("canvas");
  canvas.width = 480;
  canvas.height = 270;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    failed.value = true;
    return;
  }
  try {
    ctx.drawImage(video, sx, sy, sw, sh, 0, 0, canvas.width, canvas.height);
    poster.value = canvas.toDataURL("image/jpeg", 0.78);
    video.pause();
  } catch {
    // 自定义协议未注册或跨域取帧失败时，保留默认封面。
    failed.value = true;
  }
}
</script>

<template>
  <div class="project-cover">
    <img
      v-if="poster"
      class="project-cover__media"
      :src="poster"
      alt=""
      aria-hidden="true"
    />
    <img
      v-else
      class="project-cover__media project-cover__media--default"
      :src="defaultCover"
      alt=""
      aria-hidden="true"
    />
    <video
      v-if="canUseAssetUrl && assetId"
      ref="videoEl"
      class="project-cover__video"
      :src="src"
      muted
      playsinline
      preload="auto"
      crossorigin="anonymous"
      @loadedmetadata="onLoadedMetadata"
      @loadeddata="drawFrame"
      @seeked="drawFrame"
      @error="failed = true"
    ></video>
    <slot />
  </div>
</template>

<style scoped>
.project-cover {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 9;
  overflow: hidden;
  background: var(--color-surface-2);
}
.project-cover__media,
.project-cover__video {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.project-cover__media--default {
  object-position: center;
}
.project-cover__video {
  opacity: 0;
  pointer-events: none;
}
</style>

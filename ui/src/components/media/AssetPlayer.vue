<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { PlayerSource } from "./types";

/**
 * 单一媒体播放器：同一时刻只渲染一个 <audio>/<video>，由父级控制 source/playing。
 *
 * - 06/07 传 visible=false，用隐藏元素播放（视频源也能只出声）。
 * - 08 传 visible=true，显示原生 controls 的视频/音频。
 * - 时间窗：startMs 起播，endMs 处暂停（timeupdate + 50ms ticker 降低过冲）。
 * - A/B 切换通过 seekMs 保留时间位置；预览更短时由 duration clamp。
 * - src 只来自 preload 的 assetUrl，组件不拼接 URL，也不接触令牌。
 */
const props = defineProps<{
  source: PlayerSource | null;
  playing: boolean;
  visible?: boolean;
  ariaLabel?: string;
}>();

const emit = defineEmits<{
  (e: "update:playing", value: boolean): void;
  (e: "update:currentMs", value: number): void;
  (e: "error", messageKey: string): void;
}>();

const audioEl = ref<HTMLAudioElement | null>(null);
const videoEl = ref<HTMLVideoElement | null>(null);
const pendingAutoPlay = ref(false);
let pendingSeekMs: number | null = null;
let ticker: ReturnType<typeof setInterval> | null = null;
/** 已完成 loadedmetadata 的 src；同一地址只改时间窗时不会重新触发元数据事件。 */
let appliedSrc: string | null = null;

const kind = computed(() => props.source?.kind ?? "audio");
const src = computed(() => props.source?.src ?? undefined);
const sourceSignature = computed(
  () =>
    [
      props.source?.key ?? "",
      props.source?.src ?? "",
      props.source?.kind ?? "",
      props.source?.startMs ?? null,
      props.source?.endMs ?? null,
      props.source?.seekMs ?? null,
    ] as const,
);

function mediaEl(): HTMLMediaElement | null {
  return kind.value === "video" ? videoEl.value : audioEl.value;
}

/** audio↔video 切换时旧元素会被卸载；Chromium 的脱离 DOM 媒体不会自动停，必须显式暂停。 */
function pauseAll(): void {
  audioEl.value?.pause();
  videoEl.value?.pause();
}

function stopTicker(): void {
  if (ticker) {
    clearInterval(ticker);
    ticker = null;
  }
}

function emitCurrent(): void {
  const el = mediaEl();
  if (el) emit("update:currentMs", Math.round(el.currentTime * 1000));
}

/** 到达 endMs 时暂停并夹到窗口末端，避免 timeupdate 的 ~250ms 过冲。 */
function clampWindow(): void {
  const el = mediaEl();
  const source = props.source;
  if (!el || !source || source.endMs === undefined) return;
  if (el.currentTime * 1000 < source.endMs) return;
  el.pause();
  try {
    el.currentTime = source.endMs / 1000;
  } catch {
    // 元数据不可用时忽略定位失败；暂停已生效。
  }
  emit("update:currentMs", source.endMs);
  emit("update:playing", false);
  stopTicker();
}

function startTicker(): void {
  stopTicker();
  ticker = setInterval(() => {
    emitCurrent();
    clampWindow();
  }, 50);
}

async function playElement(): Promise<void> {
  const el = mediaEl();
  const source = props.source;
  if (!el || !source) return;
  if (el.readyState < 1) {
    // 元数据未就绪：等 loadedmetadata 后自动播放。
    pendingAutoPlay.value = true;
    return;
  }
  // 06 窗口播完后再次播放要从 startMs 重来；07 整段播完后从 0 重播。
  if (el.ended || (source.endMs !== undefined && el.currentTime * 1000 >= source.endMs - 50)) {
    try {
      el.currentTime = (source.startMs ?? 0) / 1000;
    } catch {
      // 定位失败时交给 play() 的默认行为。
    }
  }
  try {
    await el.play();
    pendingAutoPlay.value = false;
    startTicker();
  } catch {
    emit("update:playing", false);
    emit("error", "pipeline.playback.failed");
  }
}

function onLoadedMetadata(): void {
  appliedSrc = props.source?.src ?? null;
  applyPendingWindow();
  if (pendingAutoPlay.value || props.playing) void playElement();
}

/** 应用待定位的时间窗（06 startMs/endMs、08 的 seekMs），并处理比 preview 更短时的 clamp。 */
function applyPendingWindow(): void {
  const el = mediaEl();
  const source = props.source;
  if (!el || !source) return;
  let target = pendingSeekMs ?? source.startMs ?? 0;
  pendingSeekMs = null;
  const durationMs = Number.isFinite(el.duration) ? el.duration * 1000 : 0;
  if (source.endMs !== undefined) target = Math.min(target, source.endMs);
  if (durationMs > 0) {
    // 06 有明确时间窗，按窗 clamp；08 A/B 保留位置时预览可能更短，
    // clamp 到末端而不是停在 duration 后被 playElement 从 0 重播。
    target =
      source.endMs !== undefined
        ? Math.min(target, durationMs)
        : Math.min(target, Math.max(0, durationMs - 50));
  }
  target = Math.max(0, target);
  try {
    el.currentTime = target / 1000;
  } catch {
    // 部分容器不允许立即 seek；保持在 0 并继续播放。
  }
  emit("update:currentMs", Math.round(target));
}

function onTimeUpdate(): void {
  emitCurrent();
  clampWindow();
}

function onEnded(): void {
  stopTicker();
  const el = mediaEl();
  if (el) emit("update:currentMs", Math.round(el.currentTime * 1000));
  emit("update:playing", false);
}

function onMediaError(): void {
  // 停止/卸载时清空 src 也可能触发 error；没有活动源时不再打扰用户。
  if (!props.source) return;
  stopTicker();
  emit("update:playing", false);
  emit("error", "pipeline.playback.failed");
}

watch(sourceSignature, () => {
  stopTicker();
  // 切换音源（含 audio↔video 换元素）前先停旧元素，避免切换瞬间双声/残留播放。
  pauseAll();
  pendingAutoPlay.value = Boolean(props.source && props.playing);
  if (!props.source) {
    pendingSeekMs = null;
    for (const el of [audioEl.value, videoEl.value]) {
      if (!el) continue;
      el.pause();
      el.removeAttribute("src");
      el.load();
    }
    return;
  }
  pendingSeekMs = props.source.seekMs ?? props.source.startMs ?? 0;
  const el = mediaEl();
  // 06 同一源素材换句只改时间窗，src 不变、不会重新触发 loadedmetadata；
  // 此时必须立即应用新窗口，否则播放会停在上一句之后不再继续。
  if (el && el.readyState >= 1 && appliedSrc === props.source.src) {
    applyPendingWindow();
    if (props.playing) void playElement();
  }
});

watch(
  () => props.playing,
  (playing) => {
    if (playing) {
      void playElement();
      return;
    }
    // 用户暂停后即使元数据稍后才就绪，也不能再自动播放。
    pendingAutoPlay.value = false;
    stopTicker();
    pauseAll();
  },
);

onBeforeUnmount(() => {
  stopTicker();
  pauseAll();
  for (const el of [audioEl.value, videoEl.value]) {
    if (!el) continue;
    el.pause();
    el.removeAttribute("src");
    el.load();
  }
});
</script>

<template>
  <div class="asset-player" :class="{ 'asset-player--hidden': !visible }">
    <audio
      v-if="kind === 'audio'"
      ref="audioEl"
      :src="src"
      :controls="visible"
      preload="metadata"
      :aria-label="ariaLabel"
      @loadedmetadata="onLoadedMetadata"
      @timeupdate="onTimeUpdate"
      @ended="onEnded"
      @error="onMediaError"
    />
    <video
      v-else
      ref="videoEl"
      :src="src"
      :controls="visible"
      playsinline
      preload="metadata"
      :aria-label="ariaLabel"
      @loadedmetadata="onLoadedMetadata"
      @timeupdate="onTimeUpdate"
      @ended="onEnded"
      @error="onMediaError"
    />
  </div>
</template>

<style scoped>
.asset-player {
  display: flex;
  flex-direction: column;
  width: 100%;
  min-width: 0;
}
.asset-player audio {
  width: 100%;
}
.asset-player video {
  width: 100%;
  max-height: 360px;
  border-radius: var(--radius-md);
  background: #000;
}
/* 隐藏元素仍可播放音频（06/07 不展示控件）；不用 display:none 以兼容各平台媒体管线。 */
.asset-player--hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  opacity: 0;
  pointer-events: none;
}
</style>

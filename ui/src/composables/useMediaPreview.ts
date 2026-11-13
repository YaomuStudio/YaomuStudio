// 新建页上传预览：视频用对象 URL 供首帧缩略图使用；音频/其他文件走默认图标。
import { onBeforeUnmount, ref, watch, type Ref } from "vue";

export type MediaKind = "video" | "audio" | "other";

const VIDEO_EXTENSIONS = [".mp4", ".mov", ".mkv", ".m4v", ".webm", ".avi", ".m3u8"];
const AUDIO_EXTENSIONS = [".mp3", ".wav", ".aac", ".flac", ".m4a", ".ogg", ".opus"];

/** 优先按 MIME，其次按扩展名；无法判断时返回 other，页面回退默认图标。 */
export function mediaKindOf(file: File | null | undefined): MediaKind {
  if (!file) return "other";
  const type = (file.type || "").toLowerCase();
  if (type.startsWith("video/")) return "video";
  if (type.startsWith("audio/")) return "audio";
  const name = file.name.toLowerCase();
  if (VIDEO_EXTENSIONS.some((ext) => name.endsWith(ext))) return "video";
  if (AUDIO_EXTENSIONS.some((ext) => name.endsWith(ext))) return "audio";
  return "other";
}

/**
 * 上传文件预览状态。只在视频时创建对象 URL（用于首帧缩略图），
 * 文件替换/移除与组件卸载时立即回收，避免长会话泄漏。
 */
export function useMediaPreview(file: Ref<File | null>) {
  const kind = ref<MediaKind>("other");
  const url = ref("");

  function reset(): void {
    if (url.value) URL.revokeObjectURL(url.value);
    url.value = "";
    kind.value = "other";
  }

  watch(
    file,
    (next) => {
      reset();
      kind.value = mediaKindOf(next);
      if (next && kind.value === "video" && typeof URL.createObjectURL === "function") {
        try {
          url.value = URL.createObjectURL(next);
        } catch {
          url.value = "";
        }
      }
    },
    { immediate: true },
  );

  onBeforeUnmount(reset);

  return { kind, url };
}

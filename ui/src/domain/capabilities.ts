// 能力目录与八节点管线常量（对齐 docs/product-scope.md §2、docs/workflow-and-review.md §2）。
import type { CapabilitySlotKey, ModelSlotKey, PipelineNodeKey } from "./types";

export interface SlotMeta {
  key: CapabilitySlotKey;
  nameKey: string;
  /** 六个模型槽位 vs 辅助依赖（OCR/FFmpeg）。 */
  isModelSlot: boolean;
}

/** 六槽位 + OCR + FFmpeg。OCR 是辅助依赖，不占公开模型槽（FR：不新增第七槽）。 */
export const CAPABILITY_SLOTS: SlotMeta[] = [
  { key: "asr", nameKey: "slot.asr", isModelSlot: true },
  { key: "translate", nameKey: "slot.translate", isModelSlot: true },
  { key: "tts", nameKey: "slot.tts", isModelSlot: true },
  { key: "voice-clone", nameKey: "slot.voiceClone", isModelSlot: true },
  { key: "subtitle-erase", nameKey: "slot.subtitleErase", isModelSlot: true },
  { key: "separation", nameKey: "slot.separation", isModelSlot: true },
  { key: "ocr", nameKey: "slot.ocr", isModelSlot: false },
  { key: "ffmpeg", nameKey: "slot.ffmpeg", isModelSlot: false },
];

/**
 * 产品展示顺序：六个能力入口（含 OCR 字幕提取，不含 TTS）。
 * TTS 与克隆共包，设备配置仍使用 MODEL_SLOTS。
 */
export const DISPLAY_MODEL_SLOTS: CapabilitySlotKey[] = [
  "separation",
  "asr",
  "ocr",
  "subtitle-erase",
  "translate",
  "voice-clone",
];

export const MODEL_SLOTS: ModelSlotKey[] = [
  "asr",
  "translate",
  "tts",
  "voice-clone",
  "subtitle-erase",
  "separation",
];

export const isModelSlot = (slot: CapabilitySlotKey): boolean =>
  CAPABILITY_SLOTS.find((s) => s.key === slot)?.isModelSlot ?? false;

/** 八节点：用户可见流程的语义顺序（01导入…08混合成片）。 */
export const PIPELINE_NODE_KEYS: PipelineNodeKey[] = [
  "01_import",
  "02_analyze",
  "03_separate",
  "04_extract",
  "05_erase",
  "06_translate",
  "07_clone",
  "08_mix",
];

/** 需要人工审核的节点（06 翻译、08 交付）。 */
export const REVIEW_NODE_KEYS: PipelineNodeKey[] = ["06_translate", "08_mix"];

/**
 * 与语音克隆共用同一物理模型包、且不参与链路准入的槽位：
 * 不在模型页/引导页单独展示，避免同一个包出现两张卡。
 */
export const HIDDEN_MODEL_SLOTS: CapabilitySlotKey[] = ["tts"];

/** 过滤仅用于展示的槽位；过滤后为空时保留原始列表，避免出现空文案。 */
export function visibleModelSlots(slots: CapabilitySlotKey[]): CapabilitySlotKey[] {
  const visible = slots.filter((slot) => !HIDDEN_MODEL_SLOTS.includes(slot));
  return visible.length > 0 ? visible : slots;
}

// 译制工作区页面局部类型（不新增领域/API 契约字段）。
import type { Segment } from "@/domain/types";

export type StepKey = "import" | "config" | "export";

export type ResultTabKey = "editor" | "candidates" | "delivery";

/** 提取方式（FR-PIPE-01 / FR-TOOL-04 的 UI 侧三选一）。 */
export type ExtractMode = "asr" | "ocr" | "both";

/** 字幕擦除档位（FR-PIPE-02）。 */
export type EraseMode = "dialog_only" | "dialog_and_art";

/**
 * 节点 08 A/B 预览选项：原声 vs 最终混音。
 *
 * Core 只产出一个预览媒体资产，没有独立的“配音轨/混合轨”契约；
 * 不再保留 dubbed/mixed 两个没有后端依据的选项。
 */
export type TrackChoice = "original" | "preview";

/** 字幕校正器的逐句视图：原文按时间重叠匹配，多句原文合并展示。 */
export interface EditorRow {
  translation: Segment;
  original?: Segment;
}

/** 合并/拆分按钮的禁用原因（已翻译文案；存在即禁用）。 */
export interface SegmentActionState {
  mergeDisabledReason?: string;
  splitDisabledReason?: string;
}

/** 时间戳输入草稿（保留用户输入串，便于显示格式错误）。 */
export interface TimeDraft {
  start: string;
  end: string;
}

export interface SelectOption {
  value: string;
  label: string;
}

// 发布准入 → UI 入口/模式可见性。只读取现有 Capability 状态，不新增 Core 字段。
// 规则（docs/product-scope.md §3）：未准入隐藏创建入口；已准入但未就绪保留入口并提示。
import { CAPABILITY_SLOTS } from "./capabilities";
import type {
  Capability,
  CapabilitySlotKey,
  CustomPipelineFeature,
  SlotState,
} from "./types";

/** 受发布准入控制的入口。工作台/项目/任务/设置/模型管理不在此列。 */
export type AccessKey =
  | "pipeline"
  | "voice-clone"
  | "tool-erase"
  | "tool-separate"
  | "tool-translate"
  | "tool-extract"
  | "tool-custom";

/** hidden = 未准入，入口隐藏；setup = 已准入但需安装/修复/重启；ready = 可用。 */
export type AccessLevel = "hidden" | "setup" | "ready";

/** 单个槽位的准入判定。 */
export type SlotAccess = "unadmitted" | "setup" | "usable";

export interface AccessRequirement {
  /** 全部槽位都必须非未准入。 */
  allOf?: CapabilitySlotKey[];
  /** 至少一个分支满足（每个分支自身可含 allOf）。 */
  anyOf?: AccessRequirement[];
}

export interface AccessResult {
  level: AccessLevel;
  /** 路径上明确未准入的槽位（UNSUPPORTED / UNVERIFIED）。 */
  missing: CapabilitySlotKey[];
  /** 路径上已准入但未就绪的槽位。 */
  setup: CapabilitySlotKey[];
  /** 评估涉及的槽位（用于展示与诊断）。 */
  required: CapabilitySlotKey[];
  /** 能力目录是否已加载；false 时默认可见，不因未知状态隐藏。 */
  loaded: boolean;
}

const single = (slot: CapabilitySlotKey): AccessRequirement => ({ allOf: [slot] });

/**
 * 专属链路功能 → 所需槽位。媒体处理始终需要 FFmpeg，因此计入每个功能；
 * 这是入口/功能可见性的最小闭包，Core 预检仍会按导入轨等输入做最终判定。
 */
export const CUSTOM_FEATURE_REQUIREMENTS: Record<
  CustomPipelineFeature,
  AccessRequirement
> = {
  separation: { allOf: ["separation", "ffmpeg"] },
  erase: { allOf: ["ocr", "subtitle-erase", "ffmpeg"] },
  translate: { allOf: ["translate", "ffmpeg"] },
  // 克隆模型自身包含合成，不要求独立 TTS 槽。
  clone: { allOf: ["voice-clone", "ffmpeg"] },
  mix: { allOf: ["ffmpeg"] },
};

/**
 * 入口 → 所需槽位。与 Core `requiredSlots`（app-server/internal/service/runs.go）
 * 对齐：完整链路按实际节点槽位（克隆自带合成，不要求 TTS）；工具按模板所需；
 * 专属链路按功能闭包。
 */
export const ACCESS_REQUIREMENTS: Record<AccessKey, AccessRequirement> = {
  pipeline: {
    // 克隆模型自带合成，TTS 槽不参与链路准入；仍独立展示与安装。
    allOf: [
      "ffmpeg",
      "translate",
      "voice-clone",
      "asr",
      "ocr",
      "subtitle-erase",
      "separation",
    ],
  },
  "voice-clone": { allOf: ["voice-clone"] },
  "tool-erase": { allOf: ["ffmpeg", "ocr", "subtitle-erase"] },
  "tool-separate": { allOf: ["ffmpeg", "separation"] },
  "tool-translate": { allOf: ["translate"] },
  // 提取需要 FFmpeg，且 ASR/OCR 至少有一个已准入。
  "tool-extract": {
    allOf: ["ffmpeg"],
    anyOf: [single("asr"), single("ocr")],
  },
  // 至少一个功能路径所需槽位全部已准入才显示专属链路入口；
  // 与页面功能勾选使用同一份映射，避免入口可见但所有功能不可选。
  "tool-custom": {
    anyOf: Object.values(CUSTOM_FEATURE_REQUIREMENTS),
  },
};

/** 槽位状态 → 准入级别。未加载的槽位按 setup 处理，避免误隐藏。 */
export function slotAccessOf(state: SlotState | undefined): SlotAccess {
  if (state === "UNSUPPORTED" || state === "UNVERIFIED") return "unadmitted";
  if (state === "READY" || state === "DEGRADED") return "usable";
  return "setup";
}

function slotAccess(slots: Capability[], slot: CapabilitySlotKey): SlotAccess {
  const capability = slots.find((item) => item.slot === slot);
  // 能力目录未返回该槽位时不伪造准入，也不据此隐藏入口。
  return capability ? slotAccessOf(capability.state) : "setup";
}

/** 槽位是否已准入（非 UNSUPPORTED/UNVERIFIED；未知默认已准入）。 */
export function isSlotAdmitted(slots: Capability[], slot: CapabilitySlotKey): boolean {
  return slotAccess(slots, slot) !== "unadmitted";
}

/** 槽位展示文案键，供页面/导航列出缺失或未就绪项。 */
export function slotLabelKey(slot: CapabilitySlotKey): string {
  return CAPABILITY_SLOTS.find((item) => item.key === slot)?.nameKey ?? slot;
}

interface EvaluatedRequirement {
  level: AccessLevel;
  missing: Set<CapabilitySlotKey>;
  setup: Set<CapabilitySlotKey>;
  required: Set<CapabilitySlotKey>;
}

function evaluateRequirement(
  slots: Capability[],
  requirement: AccessRequirement,
): EvaluatedRequirement {
  const missing = new Set<CapabilitySlotKey>();
  const setup = new Set<CapabilitySlotKey>();
  const required = new Set<CapabilitySlotKey>();
  let level: AccessLevel = "ready";

  for (const slot of requirement.allOf ?? []) {
    required.add(slot);
    const access = slotAccess(slots, slot);
    if (access === "unadmitted") {
      missing.add(slot);
      level = "hidden";
    } else if (access === "setup") {
      setup.add(slot);
      if (level !== "hidden") level = "setup";
    }
  }

  if (requirement.anyOf?.length) {
    const branches = requirement.anyOf.map((branch) =>
      evaluateRequirement(slots, branch),
    );
    const visible = branches.filter((branch) => branch.level !== "hidden");
    if (visible.length === 0) {
      level = "hidden";
      for (const branch of branches) {
        branch.missing.forEach((slot) => missing.add(slot));
        branch.required.forEach((slot) => required.add(slot));
      }
    } else {
      const best = visible.find((branch) => branch.level === "ready") ?? visible[0];
      best.required.forEach((slot) => required.add(slot));
      best.setup.forEach((slot) => setup.add(slot));
      if (level !== "hidden" && best.level === "setup") level = "setup";
    }
  }

  return { level, missing, setup, required };
}

function toResult(
  slots: Capability[],
  requirement: AccessRequirement,
): AccessResult {
  const evaluated = evaluateRequirement(slots, requirement);
  return {
    level: evaluated.level,
    missing: [...evaluated.missing],
    setup: [...evaluated.setup],
    required: [...evaluated.required],
    loaded: slots.length > 0,
  };
}

export function evaluateAccess(slots: Capability[], key: AccessKey): AccessResult {
  return toResult(slots, ACCESS_REQUIREMENTS[key]);
}

export function evaluateFeatureAccess(
  slots: Capability[],
  feature: CustomPipelineFeature,
): AccessResult {
  return toResult(slots, CUSTOM_FEATURE_REQUIREMENTS[feature]);
}

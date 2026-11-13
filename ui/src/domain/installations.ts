// 安装实体/槽位引用的只读投影规则（contracts/shared-installations.md）。
//
// 一份物理安装可被多个槽位引用；`slot` 只是发起入口，不是同意范围。
// 字段缺失仅代表旧 Core（保持原单槽语义）；显式空数组表示“无引用”，
// 绝不能回退成发起 slot，否则已解绑的槽位会被旧回执复活。
import { CAPABILITY_SLOTS } from "./capabilities";
import type {
  CapabilitySlotKey,
  Installation,
  InstallationPhase,
  InstallationPlan,
} from "./types";

const ACTIVE_PHASES: ReadonlySet<InstallationPhase> = new Set<InstallationPhase>([
  "planning",
  "downloading",
  "verifying",
  "installing",
]);

const SLOT_KEYS: ReadonlySet<string> = new Set(CAPABILITY_SLOTS.map((meta) => meta.key));

export function isCapabilitySlotKey(value: unknown): value is CapabilitySlotKey {
  return typeof value === "string" && SLOT_KEYS.has(value);
}

export function isInstallationActive(installation: Installation): boolean {
  return ACTIVE_PHASES.has(installation.phase);
}

/** 仍引用该物理记录的槽位；旧 Core 缺字段时只按发起槽单槽处理。 */
export function installationBoundSlots(installation: Installation): CapabilitySlotKey[] {
  return installation.boundSlots ?? [installation.slot];
}

/** 原始同意覆盖范围；旧 Core 缺字段时只按发起槽单槽处理。 */
export function installationCoveredSlots(installation: Installation): CapabilitySlotKey[] {
  return installation.coveredSlots ?? [installation.slot];
}

/**
 * 当前选中该实体（Done）的槽位。显式空数组保持空；旧 Core 缺字段时
 * 按单槽语义显示发起槽。仅用于展示/解绑说明，不用于派发准入。
 */
export function installationCurrentSlots(installation: Installation): CapabilitySlotKey[] {
  if (installation.currentSlots !== undefined) return installation.currentSlots;
  return installation.phase === "done" ? [installation.slot] : [];
}

/** 该记录是否仍被该槽位引用（活动中/保留历史）。 */
export function installationCoversSlot(
  installation: Installation,
  slot: CapabilitySlotKey,
): boolean {
  return installationBoundSlots(installation).includes(slot);
}

/** 该槽位当前进行中的物理安装；多个槽位命中时返回同一个实体/进度。 */
export function activeInstallationForSlot(
  installations: Installation[],
  slot: CapabilitySlotKey,
): Installation | undefined {
  return installations.find(
    (installation) =>
      isInstallationActive(installation) && installationCoversSlot(installation, slot),
  );
}

/** 该槽位最近一条仍引用它的记录（含 Done/失败历史），用于重试入口。 */
export function latestInstallationForSlot(
  installations: Installation[],
  slot: CapabilitySlotKey,
): Installation | undefined {
  return installations.find((installation) => installationCoversSlot(installation, slot));
}

/** 该槽位当前选中的 Done 实体；显式空数组时不回退发起槽。 */
export function currentInstallationForSlot(
  installations: Installation[],
  slot: CapabilitySlotKey,
): Installation | undefined {
  return installations.find(
    (installation) =>
      installation.phase === "done" &&
      installationCurrentSlots(installation).includes(slot),
  );
}

/** 同一次 Done 安装还服务哪些槽位（删除说明用；旧 Core 单槽不显示共享）。 */
export function siblingCurrentSlots(
  installations: Installation[],
  slot: CapabilitySlotKey,
): CapabilitySlotKey[] {
  const current = currentInstallationForSlot(installations, slot);
  if (!current) return [];
  return installationCurrentSlots(current).filter((entry) => entry !== slot);
}

export interface PlanCoverage {
  /** 用户确认前必须展示的完整覆盖范围。 */
  slots: CapabilitySlotKey[];
  /** true 仅表示旧 Core 缺字段，按原单槽语义展示。 */
  legacy: boolean;
  /** 显式范围是否合法（非空、唯一、已知槽位且包含发起槽）。 */
  valid: boolean;
}

/**
 * 计划覆盖范围。字段缺失=旧 Core 单槽；显式数组必须完整且包含请求槽，
 * 否则 valid=false，界面不得允许确认安装。
 */
export function planCoverage(plan: InstallationPlan | null): PlanCoverage {
  if (!plan) return { slots: [], legacy: false, valid: false };
  if (plan.coveredSlots === undefined) {
    return { slots: [plan.slot], legacy: true, valid: true };
  }
  const slots = plan.coveredSlots;
  const valid =
    Array.isArray(slots) &&
    slots.length > 0 &&
    new Set(slots).size === slots.length &&
    slots.every(isCapabilitySlotKey) &&
    slots.includes(plan.slot);
  return { slots: valid ? [...slots] : [], legacy: false, valid };
}

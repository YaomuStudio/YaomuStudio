import { t } from "@/i18n";
import type { RunParams } from "@/domain/types";

export type RunOptionKey = "validateTimestamps" | "adjustDuration";

export interface RunOptionRow {
  key: RunOptionKey;
  labelKey: string;
  valueKey: string;
  warning: boolean;
}

const LABEL_KEYS: Record<RunOptionKey, string> = {
  validateTimestamps: "runOptions.validateTimestamps",
  adjustDuration: "runOptions.adjustDuration",
};

/**
 * 只翻译 Run.params 的冻结值：
 * - undefined 表示 Core 未记录，不按 false 推断；
 * - adjustDuration=true 是历史记录，当前构建不生效，必须显式说明。
 */
export function runOptionValueKey(
  key: RunOptionKey,
  value: boolean | undefined,
): string {
  if (value === undefined) return "runOptions.notRecorded";
  if (key === "adjustDuration" && value) return "runOptions.adjustRecordedNotApplied";
  return value ? "runOptions.on" : "runOptions.off";
}

export function runOptionValueText(
  key: RunOptionKey,
  value: boolean | undefined,
): string {
  return t(runOptionValueKey(key, value));
}

/**
 * 只读摘要行。params 缺失（旧 Run 未记录任何选项）时返回空，不展示伪造默认值；
 * 单个字段缺失仍显示“未记录”，不猜成 false。
 */
export function runOptionRows(params: RunParams | undefined): RunOptionRow[] {
  if (!params) return [];
  return (Object.keys(LABEL_KEYS) as RunOptionKey[]).map((key) => ({
    key,
    labelKey: LABEL_KEYS[key],
    valueKey: runOptionValueKey(key, params[key]),
    warning: key === "adjustDuration" && params[key] === true,
  }));
}

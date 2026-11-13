// 审核门槛的只读当前绑定选择。
//
// Core 在列表/详情里派生 current_bindings；UI 只按 (run, language, source_kind,
// source_id) 精确匹配，不计算摘要、不按列表顺序猜测、不把历史 approved 当当前。
// 缺省投影（旧 Core）必须 fail closed；空数组表示历史记录。
import type { ID, ReviewCurrentBinding, ReviewGate, ReviewStatus } from "@/domain/types";

export interface CurrentGateBinding {
  gate: ReviewGate;
  binding: ReviewCurrentBinding;
}

export interface TranslationSourceRef {
  runId?: ID;
  language: string;
  revisionId?: ID;
}

export interface TranslationReviewView {
  current?: CurrentGateBinding;
  /** 旧 Core 未提供 current_bindings：不可放行，也不能沿用旧 approved。 */
  projectionUnknown: boolean;
  /** 绑定旧 revision 或已无当前绑定；只展示为历史，不提供 Approve。 */
  historicalGates: ReviewGate[];
}

export interface DeliverySourceRef {
  runId?: ID;
  language: string;
  exportKind: "video" | "audio" | "subtitle";
  /** 字幕导出实际冻结的译文 revision ID。 */
  subtitleRevisionId?: ID;
}

export interface DeliveryBindingRow {
  gate: ReviewGate;
  binding: ReviewCurrentBinding;
  status: ReviewStatus;
}

export interface DeliveryReviewView {
  /** 当前导出语种下所有相关当前绑定；同一 tuple 的重复记录已确定性收敛。 */
  rows: DeliveryBindingRow[];
  /**
   * 本次导出实际授权范围（Core exports.currentDeliveryDigests 同口径）：
   * 该语种 latest mix + 当前选中的字幕 revision。其他同语当前 track 仅逐条展示。
   */
  relevantRows: DeliveryBindingRow[];
  /** 与当前导出实际内容匹配的绑定（字幕 revision 或最终混音）。 */
  primary?: DeliveryBindingRow;
  /** 所有 current rows 的状态（用于展示真实历史）。 */
  aggregate: "none" | "pending" | "approved";
  /** 本次导出相关 binding 的状态（用于确认/导出门禁）。 */
  relevantAggregate: "none" | "pending" | "approved";
  projectionUnknown: boolean;
  historicalGates: ReviewGate[];
  /** 该 Run 是否存在 delivery 门槛（含历史/其他语种）；用于区分"尚未生成门槛"与"没有当前绑定"。 */
  hasGates: boolean;
  /** 有当前绑定但没有任何一条匹配当前导出内容：不得猜测或批量放行。 */
  selectionMatched: boolean;
}

function statusRank(status: ReviewStatus): number {
  return status === "pending" ? 0 : 1;
}

function bindingKey(binding: ReviewCurrentBinding): string {
  return [binding.language, binding.sourceKind, binding.sourceId].join("\u0000");
}

function pickTranslationBinding(
  gates: readonly ReviewGate[],
  language: string,
  revisionId: string,
): CurrentGateBinding | undefined {
  const matches: CurrentGateBinding[] = [];
  for (const gate of gates) {
    for (const binding of gate.currentBindings ?? []) {
      if (
        binding.sourceKind === "translation_revision" &&
        binding.language === language &&
        binding.sourceId === revisionId
      ) {
        matches.push({ gate, binding });
      }
    }
  }
  // 语义相同的 tuple 只按状态/ID 做确定性选择，不合并其他来源。
  matches.sort(
    (a, b) =>
      statusRank(a.gate.status) - statusRank(b.gate.status) ||
      a.gate.id.localeCompare(b.gate.id),
  );
  return matches[0];
}

/** 06 翻译门槛：Run、可见语种与可见译文 revision 必须同时命中当前绑定。 */
export function translationReviewView(
  reviews: readonly ReviewGate[],
  source: TranslationSourceRef,
): TranslationReviewView {
  if (!source.runId || !source.language) {
    return { projectionUnknown: false, historicalGates: [] };
  }
  const gates = reviews.filter(
    (gate) => gate.type === "translation" && gate.runId === source.runId,
  );
  const projectionUnknown = gates.some((gate) => gate.currentBindings === undefined);
  const current =
    source.revisionId && source.revisionId.length > 0
      ? pickTranslationBinding(gates, source.language, source.revisionId)
      : undefined;
  const historicalGates = gates.filter((gate) => {
    const bindings = gate.currentBindings;
    if (bindings === undefined) return false;
    if (bindings.length === 0) return true;
    const sameLanguage = bindings.filter((binding) => binding.language === source.language);
    if (sameLanguage.length === 0) return false;
    return !sameLanguage.some(
      (binding) =>
        binding.sourceKind === "translation_revision" &&
        binding.sourceId === source.revisionId,
    );
  });
  return { current, projectionUnknown, historicalGates };
}

function pickPrimaryDeliveryRow(
  rows: readonly DeliveryBindingRow[],
  source: DeliverySourceRef,
): DeliveryBindingRow | undefined {
  if (source.exportKind === "subtitle") {
    if (source.subtitleRevisionId) {
      const revisionRow = rows.find(
        (row) =>
          row.binding.sourceKind === "delivery_revision" &&
          row.binding.sourceId === source.subtitleRevisionId,
      );
      if (revisionRow) return revisionRow;
    }
    // Core 也接受匹配当前 revision 的 08 摘要放行字幕导出。
    return rows.find((row) => row.binding.sourceKind === "delivery_mix");
  }
  // 视频/音频只接受当前 revision 的成功 08；字幕 revision 级绑定不能放行。
  return rows.find((row) => row.binding.sourceKind === "delivery_mix");
}

/** 该绑定是否属于本次导出类型的有效放行来源（与 Core currentDeliveryDigests 同口径）。 */
function deliveryBindingRelevant(
  row: DeliveryBindingRow,
  source: DeliverySourceRef,
): boolean {
  if (source.exportKind !== "subtitle") {
    return row.binding.sourceKind === "delivery_mix";
  }
  return (
    row.binding.sourceKind === "delivery_mix" ||
    (row.binding.sourceKind === "delivery_revision" &&
      !!source.subtitleRevisionId &&
      row.binding.sourceId === source.subtitleRevisionId)
  );
}

/**
 * 08 交付门槛：按导出语种收集所有相关当前绑定。
 * 字幕导出优先匹配实际 revision；视频/音频优先匹配最终混音；其他当前 pending
 * 仍逐条保留为可放行动作，但不会因一次批准被标记为总体已审。
 */
export function deliveryReviewView(
  reviews: readonly ReviewGate[],
  source: DeliverySourceRef,
): DeliveryReviewView {
  if (!source.runId || !source.language) {
    return {
      rows: [],
      relevantRows: [],
      aggregate: "none",
      relevantAggregate: "none",
      projectionUnknown: false,
      historicalGates: [],
      hasGates: false,
      selectionMatched: true,
    };
  }
  const gates = reviews.filter(
    (gate) => gate.type === "delivery" && gate.runId === source.runId,
  );
  const projectionUnknown = gates.some((gate) => gate.currentBindings === undefined);

  const grouped = new Map<string, DeliveryBindingRow[]>();
  for (const gate of gates) {
    for (const binding of gate.currentBindings ?? []) {
      if (binding.language !== source.language) continue;
      if (binding.sourceKind !== "delivery_revision" && binding.sourceKind !== "delivery_mix") {
        continue;
      }
      const key = bindingKey(binding);
      const group = grouped.get(key) ?? [];
      group.push({ gate, binding, status: gate.status });
      grouped.set(key, group);
    }
  }

  const rows: DeliveryBindingRow[] = [...grouped.keys()].sort().map((key) => {
    const group = grouped.get(key) ?? [];
    const representative = [...group].sort(
      (a, b) =>
        statusRank(a.status) - statusRank(b.status) || a.gate.id.localeCompare(b.gate.id),
    )[0];
    const pending = group.some((row) => row.status === "pending");
    return { ...representative, status: pending ? "pending" : "approved" };
  });

  const pending = rows.some((row) => row.status === "pending");
  const aggregate: DeliveryReviewView["aggregate"] =
    rows.length === 0 ? "none" : pending ? "pending" : "approved";

  // Core 一次导出只校验该语种 latest mix + 本次实际选中的字幕 revision；
  // 同语其他当前 track 仍逐条可批准，但不阻塞本次已审导出。
  const relevantRows = rows.filter((row) => deliveryBindingRelevant(row, source));
  const relevantAggregate: DeliveryReviewView["relevantAggregate"] =
    relevantRows.length === 0
      ? "none"
      : relevantRows.some((row) => row.status === "pending")
        ? "pending"
        : "approved";
  const primary = pickPrimaryDeliveryRow(rows, source);

  const historicalGates = gates.filter((gate) => {
    const bindings = gate.currentBindings;
    if (bindings === undefined) return false;
    if (bindings.length === 0) return true;
    // 空 language 由 Core 按精确摘要解析到具体语种；绑定到其他语种不算历史。
    if (!gate.language) return false;
    if (gate.language !== source.language) return false;
    return !bindings.some((binding) => binding.language === source.language);
  });

  return {
    rows,
    relevantRows,
    primary,
    aggregate,
    relevantAggregate,
    projectionUnknown,
    historicalGates,
    hasGates: gates.length > 0,
    // 没有任何当前绑定时不阻止旧的无门槛本地确认流程；有绑定却不匹配必须提示。
    selectionMatched: rows.length === 0 || primary !== undefined,
  };
}

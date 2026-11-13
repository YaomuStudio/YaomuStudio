// 状态 → 徽标色调 + 文案键 的统一映射（TaskCenter / Pipeline / Models 共用）。
// 状态机对齐 contracts/openapi.yaml 0.2（SlotState / TaskStatus / RunStatus）。
import type { NodeStatus, RunStatus, SlotState, TaskStatus } from "./types";

export type Tone = "neutral" | "success" | "warning" | "error" | "info" | "accent";

const SLOT_LABEL: Record<SlotState, string> = {
  UNKNOWN: "state.unknown",
  UNSUPPORTED: "state.unsupported",
  UNVERIFIED: "state.unverified",
  NEEDS_SETUP: "state.needsSetup",
  INSTALLING: "state.installing",
  VERIFYING: "state.verifying",
  READY: "state.ready",
  NEEDS_ACTION: "state.needsAction",
  RESTART_REQUIRED: "state.restartRequired",
  FAILED: "state.failed",
  DEGRADED: "state.degraded",
};

const SLOT_TONE: Record<SlotState, Tone> = {
  UNKNOWN: "neutral",
  UNSUPPORTED: "neutral",
  UNVERIFIED: "warning",
  NEEDS_SETUP: "neutral",
  INSTALLING: "info",
  VERIFYING: "info",
  READY: "accent",
  NEEDS_ACTION: "warning",
  RESTART_REQUIRED: "warning",
  FAILED: "error",
  DEGRADED: "warning",
};

const TASK_LABEL: Record<TaskStatus, string> = {
  QUEUED: "state.queued",
  WAITING_RESOURCE: "state.waitingResource",
  RUNNING: "state.running",
  PAUSE_REQUESTED: "state.pauseRequested",
  PAUSED: "state.paused",
  SUCCEEDED: "state.succeeded",
  FAILED: "state.failed",
  CANCEL_REQUESTED: "state.cancelRequested",
  CANCELED: "state.canceled",
  INTERRUPTED: "state.interrupted",
};

const TASK_TONE: Record<TaskStatus, Tone> = {
  QUEUED: "neutral",
  WAITING_RESOURCE: "neutral",
  RUNNING: "info",
  PAUSE_REQUESTED: "warning",
  PAUSED: "warning",
  SUCCEEDED: "success",
  FAILED: "error",
  CANCEL_REQUESTED: "warning",
  CANCELED: "neutral",
  INTERRUPTED: "warning",
};

const RUN_LABEL: Record<RunStatus, string> = {
  DRAFT: "state.queued",
  QUEUED: "state.queued",
  RUNNING: "state.running",
  PAUSE_REQUESTED: "state.pauseRequested",
  PAUSED: "state.paused",
  // 用户界面不展示“待审核”：内部 WAITING_REVIEW 仍用于审核门禁/下游派发，
  // 显示层统一投影为“已完成”；人工校正作为操作入口，不作为状态。
  WAITING_REVIEW: "state.succeeded",
  SUCCEEDED: "state.succeeded",
  PARTIAL: "state.partial",
  FAILED: "state.failed",
  CANCELED: "state.canceled",
};

const RUN_TONE: Record<RunStatus, Tone> = {
  DRAFT: "neutral",
  QUEUED: "neutral",
  RUNNING: "info",
  PAUSE_REQUESTED: "warning",
  PAUSED: "warning",
  WAITING_REVIEW: "success",
  SUCCEEDED: "success",
  PARTIAL: "warning",
  FAILED: "error",
  CANCELED: "neutral",
};

const NODE_LABEL: Record<NodeStatus, string> = {
  pending: "state.pending",
  running: "state.running",
  // 节点级同样投影为已完成；审核门禁仍由 Core 控制。
  waiting_review: "state.succeeded",
  done: "state.succeeded",
  skipped: "state.skipped",
  reused: "state.reused",
  failed: "state.failed",
};

const NODE_TONE: Record<NodeStatus, Tone> = {
  pending: "neutral",
  running: "info",
  waiting_review: "success",
  done: "success",
  skipped: "neutral",
  reused: "accent",
  failed: "error",
};

export const slotStateLabelKey = (s: SlotState): string => SLOT_LABEL[s];
export const slotStateTone = (s: SlotState): Tone => SLOT_TONE[s];
export const taskStatusLabelKey = (s: TaskStatus): string => TASK_LABEL[s];
export const taskStatusTone = (s: TaskStatus): Tone => TASK_TONE[s];
export const runStatusLabelKey = (s: RunStatus): string => RUN_LABEL[s];
export const runStatusTone = (s: RunStatus): Tone => RUN_TONE[s];
export const nodeStatusLabelKey = (s: NodeStatus): string => NODE_LABEL[s];
export const nodeStatusTone = (s: NodeStatus): Tone => NODE_TONE[s];

/** 项目卡片只展示四个业务状态：待制作、制作中、已完成、制作失败。 */
const PROJECT_STATUS_LABEL: Record<RunStatus, string> = {
  DRAFT: "state.queued",
  QUEUED: "state.queued",
  RUNNING: "state.running",
  PAUSE_REQUESTED: "state.running",
  PAUSED: "state.running",
  WAITING_REVIEW: "state.succeeded",
  SUCCEEDED: "state.succeeded",
  PARTIAL: "state.failed",
  FAILED: "state.failed",
  CANCELED: "state.failed",
};

const PROJECT_STATUS_TONE: Record<RunStatus, Tone> = {
  DRAFT: "neutral",
  QUEUED: "neutral",
  RUNNING: "info",
  PAUSE_REQUESTED: "info",
  PAUSED: "info",
  WAITING_REVIEW: "success",
  SUCCEEDED: "success",
  PARTIAL: "error",
  FAILED: "error",
  CANCELED: "error",
};

export const projectStatusLabelKey = (s: RunStatus): string => PROJECT_STATUS_LABEL[s];
export const projectStatusTone = (s: RunStatus): Tone => PROJECT_STATUS_TONE[s];

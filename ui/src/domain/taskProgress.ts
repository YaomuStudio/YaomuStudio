// task.progress 消费边界：seq=0 的可丢弃反馈，只用于视图合并，不是持久事实。
//
// 严格按 contracts/openapi.yaml TaskProgressEvent/TaskProgressData 校验：
// schema_version=1、seq=0、project/task、正整数 state_version、正十进制 server_epoch、
// 正整数 attempt、整数 progress 0..100。身份必须匹配已载入的 RUNNING Task 及其
// RUNNING step 的 attempt_no/epoch；缺失或非法身份一律拒绝，不从标签/顺序/运行时
// generation 推断。Task DTO 不新增 state_version 字段，事件版本只保存在 store 视图元数据。
import type { DomainEvent, Task } from "./types";

const STEP_DECIMAL = /^[1-9][0-9]*$/;

export interface TaskProgressFrame {
  taskId: string;
  projectId: string;
  stepId: string;
  attempt: number;
  epoch: number;
  progress: number;
  stateVersion: number;
}

export interface TaskRunningIdentity {
  attempt: number;
  epoch: number;
}

/** 安全正整数（Core int64 经 JSON 数字进入 JS 时不得超出安全整数范围）。 */
export function isSafePositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function isProgressPercent(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 100;
}

/** 事件携带的 step 身份（只认 RUNNING step）。 */
export function runningStepIdentity(task: Task): TaskRunningIdentity | null {
  const step = task.steps?.find((item) => item.status === "RUNNING");
  if (!step) return null;
  if (!isSafePositiveInteger(step.attemptNo) || !isSafePositiveInteger(step.epoch)) {
    return null;
  }
  return { attempt: step.attemptNo, epoch: step.epoch };
}

/** 解析并校验一条 task.progress；返回 null 表示该帧必须被丢弃。 */
export function parseTaskProgress(
  event: DomainEvent,
  tasks: readonly Task[],
): TaskProgressFrame | null {
  if (event.type !== "task.progress") return null;
  if (event.schemaVersion !== 1 || event.seq !== 0) return null;

  const projectId = event.projectId;
  const taskId = event.taskId;
  if (typeof projectId !== "string" || projectId.length === 0) return null;
  if (typeof taskId !== "string" || taskId.length === 0) return null;
  const stateVersion = event.stateVersion;
  if (!isSafePositiveInteger(stateVersion)) return null;

  const data = event.data;
  if (typeof data !== "object" || data === null || Array.isArray(data)) return null;
  const raw = data as Record<string, unknown>;
  const stepId = raw.step_id;
  if (typeof stepId !== "string" || stepId.length === 0) return null;
  const attempt = raw.attempt;
  if (!isSafePositiveInteger(attempt)) return null;
  const serverEpoch = raw.server_epoch;
  if (typeof serverEpoch !== "string" || !STEP_DECIMAL.test(serverEpoch)) return null;
  const epoch = Number(serverEpoch);
  if (!isSafePositiveInteger(epoch)) return null;
  const progress = raw.progress;
  if (!isProgressPercent(progress)) return null;

  const task = tasks.find((item) => item.id === taskId);
  if (!task) return null;
  if (task.projectId !== projectId || task.status !== "RUNNING") return null;
  const step = task.steps?.find((item) => item.stepId === stepId);
  if (!step || step.status !== "RUNNING") return null;
  if (step.attemptNo !== attempt || step.epoch !== epoch) return null;

  return { taskId, projectId, stepId, attempt, epoch, progress, stateVersion };
}

/**
 * 运行中任务尚无有意义进度时显示不确定态，而不是伪造 0%/100%。
 * Core 快照里的 0 可以保留为数值，但 0 不代表执行器已知总量。
 */
export function isTaskProgressIndeterminate(task: Task): boolean {
  if (task.status !== "RUNNING") return task.progress === undefined;
  return task.progress === undefined || task.progress === 0;
}

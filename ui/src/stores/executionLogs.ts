// 执行过程日志 store：只保存可丢弃的界面反馈，不作为任务状态来源。
// 事件来自 task.execution_log（seq=0），回放来自 GET /tasks/{id}/execution-log。
import { defineStore } from "pinia";
import { ref } from "vue";
import type { DomainEvent, ExecutionLogLine, ExecutionLogStream, ID } from "@/domain/types";

const MAX_LINES = 500;
const STREAMS: readonly ExecutionLogStream[] = ["stderr", "stdout", "runner"];

interface TaskLog {
  attempt: number;
  lines: ExecutionLogLine[];
}

function asStream(value: unknown): ExecutionLogStream {
  return typeof value === "string" && (STREAMS as readonly string[]).includes(value)
    ? (value as ExecutionLogStream)
    : "runner";
}

function mergeLines(current: ExecutionLogLine[], incoming: ExecutionLogLine[]): ExecutionLogLine[] {
  const bySeq = new Map<number, ExecutionLogLine>();
  const withoutSeq: ExecutionLogLine[] = [];
  for (const line of [...current, ...incoming]) {
    if (line.seq > 0) bySeq.set(line.seq, line);
    else withoutSeq.push(line);
  }
  const merged = [...bySeq.values()].sort((a, b) => a.seq - b.seq).concat(withoutSeq);
  return merged.slice(-MAX_LINES);
}

export const useExecutionLogsStore = defineStore("executionLogs", () => {
  const byTask = ref<Record<string, TaskLog>>({});

  function handleEvent(event: DomainEvent): void {
    if (event.type !== "task.execution_log") return;
    const data = (event.data ?? {}) as Record<string, unknown>;
    const taskId = (event.taskId ?? (typeof data.task_id === "string" ? data.task_id : "")) as ID;
    const line = typeof data.line === "string" ? data.line : "";
    if (!taskId || !line) return;
    const attempt = typeof data.attempt === "number" ? data.attempt : 0;
    const seq = typeof data.seq === "number" ? data.seq : 0;
    append(taskId, attempt, {
      seq,
      stream: asStream(data.stream),
      line,
      at: typeof data.at === "string" ? data.at : undefined,
    });
  }

  function append(taskId: ID, attempt: number, line: ExecutionLogLine): void {
    const current = byTask.value[taskId];
    // 旧 attempt 的迟到事件不得覆盖新 attempt。
    if (current && attempt < current.attempt) return;
    if (!current || attempt > current.attempt) {
      byTask.value[taskId] = { attempt, lines: [line] };
      return;
    }
    if (line.seq > 0 && current.lines.some((item) => item.seq === line.seq)) return;
    const lines = [...current.lines, line];
    if (lines.length > MAX_LINES) lines.splice(0, lines.length - MAX_LINES);
    byTask.value[taskId] = { attempt, lines };
  }

  /**
   * 回放尾部与实时事件合并；同一 attempt 以回放内容为准。
   * 旧 attempt 的迟到回放（或 attempt=0 的空读）不得清掉当前新 attempt 日志。
   */
  function seed(taskId: ID, attempt: number, lines: ExecutionLogLine[]): void {
    const current = byTask.value[taskId];
    if (current && attempt < current.attempt) return;
    if (current && attempt === current.attempt && lines.length === 0) {
      byTask.value[taskId] = { attempt, lines: mergeLines(current.lines, []) };
      return;
    }
    if (!current || attempt > current.attempt) {
      if (lines.length === 0 && current) return;
      byTask.value[taskId] = { attempt, lines: lines.slice(-MAX_LINES) };
      return;
    }
    byTask.value[taskId] = { attempt, lines: mergeLines(current.lines, lines) };
  }

  function linesFor(taskId: ID): ExecutionLogLine[] {
    return byTask.value[taskId]?.lines ?? [];
  }

  function clear(taskId: ID): void {
    delete byTask.value[taskId];
  }

  return { byTask, handleEvent, seed, linesFor, clear };
});

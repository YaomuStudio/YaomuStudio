/**
 * Main-only 冒烟证据：读取本次 Core 的本地诊断日志，确认同一次启动里
 * `core.ready`（epoch/port）之后出现 `core.runner_ready` 且 `runner=model`。
 *
 * - 只读隔离数据目录下的 `logs/core.log`；有界读取尾部，不整文件载入。
 * - 不把 HTTP /health/live 或单独 hello 当执行器就绪；也不改 Core 日志名。
 * - 只返回固定 reason，不把原始日志行/环境写进结果。
 * - 该模块不依赖 electron，可在普通 Node 测试中验证。
 */
import { open, stat } from "node:fs/promises";
import path from "node:path";
import { abortError } from "./bootstrap";

export type CoreRunnerEvidenceDetail =
  | "runner-model-ready"
  | "missing-core-ready"
  | "runner-ready-missing"
  | "runner-ready-before-ready"
  | "runner-not-model"
  | "log-unavailable";

export interface CoreRunnerEvidenceResult {
  ok: boolean;
  detail: CoreRunnerEvidenceDetail;
}

export interface CoreRunnerEvidenceQuery {
  /** 当前已认证 Core 的 server_epoch（每次启动递增）。 */
  epoch: number;
  /** 当前已认证连接端口。 */
  port: number;
}

const DEFAULT_MAX_LOG_BYTES = 2 * 1024 * 1024;

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function parseLine(line: string): Record<string, unknown> | null {
  if (!line.trim()) return null;
  try {
    const value: unknown = JSON.parse(line);
    return isRecord(value) ? value : null;
  } catch {
    return null;
  }
}

/**
 * 纯解析：按日志行顺序找当前 epoch/port 的 `core.ready`，
 * 只接受它之后的 `core.runner_ready`（runner=model）。
 */
export function parseCoreRunnerEvidence(
  text: string,
  query: CoreRunnerEvidenceQuery,
): CoreRunnerEvidenceResult {
  let readySeen = false;
  let runnerAfter: "none" | "model" | "other" = "none";
  let runnerBeforeReady = false;
  for (const line of text.split(/\r?\n/)) {
    const entry = parseLine(line);
    if (!entry) continue;
    if (entry.event === "core.ready") {
      const matches = entry.epoch === query.epoch && entry.port === query.port;
      // 之后又出现别的启动：之前观察到的 Runner 成功属于已被取代的实例。
      readySeen = matches;
      if (!matches) runnerAfter = "none";
      continue;
    }
    if (entry.event !== "core.runner_ready") continue;
    if (readySeen) {
      runnerAfter = entry.runner === "model" ? "model" : "other";
    } else {
      runnerBeforeReady = true;
    }
  }

  if (readySeen && runnerAfter === "model") return { ok: true, detail: "runner-model-ready" };
  if (readySeen && runnerAfter === "other") return { ok: false, detail: "runner-not-model" };
  if (!readySeen) return { ok: false, detail: "missing-core-ready" };
  if (runnerBeforeReady) return { ok: false, detail: "runner-ready-before-ready" };
  return { ok: false, detail: "runner-ready-missing" };
}

/**
 * 读取日志尾部并解析证据。
 * 文件缺失/不可读返回 `log-unavailable`；取消抛 AbortError。
 */
export async function readCoreRunnerEvidence(options: {
  logPath: string;
  epoch: number;
  port: number;
  maxBytes?: number;
  signal?: AbortSignal;
}): Promise<CoreRunnerEvidenceResult> {
  if (options.signal?.aborted) throw abortError("Core 日志证据读取已取消");
  const maxBytes = options.maxBytes ?? DEFAULT_MAX_LOG_BYTES;
  let size: number;
  try {
    const info = await stat(options.logPath);
    if (!info.isFile()) return { ok: false, detail: "log-unavailable" };
    size = info.size;
  } catch {
    return { ok: false, detail: "log-unavailable" };
  }
  if (size <= 0) {
    return { ok: false, detail: "missing-core-ready" };
  }

  const length = Math.min(size, maxBytes);
  const start = size - length;
  let handle;
  let text: string;
  try {
    handle = await open(options.logPath, "r");
    const buffer = Buffer.alloc(length);
    const { bytesRead } = await handle.read(buffer, 0, length, start);
    if (options.signal?.aborted) throw abortError("Core 日志证据读取已取消");
    text = buffer.subarray(0, bytesRead).toString("utf8");
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") throw error;
    return { ok: false, detail: "log-unavailable" };
  } finally {
    await handle?.close().catch(() => undefined);
  }
  // 截断读取会落在半行上：丢弃第一段残行，只解析完整行。
  if (start > 0) {
    const newline = text.indexOf("\n");
    text = newline >= 0 ? text.slice(newline + 1) : "";
  }
  return parseCoreRunnerEvidence(text, { epoch: options.epoch, port: options.port });
}

export interface CoreEvidenceSource {
  dataDir: string;
  getCurrentCore: () => { epoch: number; port: number; generation: number } | null;
}

export interface ObserveCoreRunnerEvidenceOptions {
  timeoutMs: number;
  pollMs?: number;
  /** 测试注入；默认 readCoreRunnerEvidence。 */
  readEvidence?: typeof readCoreRunnerEvidence;
  /** 测试注入；默认轮询等待。 */
  sleep?: (ms: number) => Promise<void>;
}

/** 同一启动的身份锚点；epoch/port/generation 任一变化都表示已重启/换实例。 */
function sameCoreIdentity(
  a: { epoch: number; port: number; generation: number },
  b: { epoch: number; port: number; generation: number } | null,
): b is { epoch: number; port: number; generation: number } {
  return (
    b !== null && a.epoch === b.epoch && a.port === b.port && a.generation === b.generation
  );
}

/**
 * 有界等待本次已认证 Core 的模型侧 Runner 证据。
 * - 总预算覆盖每次 readEvidence 的 await；读取超预算不返回旧证据。
 * - 读取成功后必须重检同一 epoch/port/generation；期间重启/换连接不采纳旧证明。
 */
export async function observeCoreRunnerEvidence(
  source: CoreEvidenceSource,
  options: ObserveCoreRunnerEvidenceOptions,
): Promise<{ ok: boolean; detail: string }> {
  const pollMs = options.pollMs ?? 500;
  const readEvidence = options.readEvidence ?? readCoreRunnerEvidence;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const deadline = Date.now() + options.timeoutMs;
  let anchor: { epoch: number; port: number; generation: number } | null = null;
  let last: { ok: boolean; detail: string } = { ok: false, detail: "missing-core-ready" };

  const readWithinBudget = async (
    current: { epoch: number; port: number; generation: number },
  ): Promise<{ ok: boolean; detail: string } | "timeout"> => {
    const remaining = deadline - Date.now();
    if (remaining <= 0) return "timeout";
    let timer: NodeJS.Timeout | undefined;
    const budget = new Promise<"timeout">((resolve) => {
      timer = setTimeout(() => resolve("timeout"), remaining);
    });
    try {
      return await Promise.race([
        readEvidence({
          logPath: path.join(source.dataDir, "logs", "core.log"),
          epoch: current.epoch,
          port: current.port,
        }),
        budget,
      ]);
    } finally {
      if (timer) clearTimeout(timer);
    }
  };

  for (;;) {
    const current = source.getCurrentCore();
    if (!current) return { ok: false, detail: "current-core-unavailable" };
    if (anchor === null) {
      anchor = current;
    } else if (!sameCoreIdentity(anchor, current)) {
      return { ok: false, detail: "core-restarted" };
    }

    let read: { ok: boolean; detail: string } | "timeout";
    try {
      read = await readWithinBudget(current);
    } catch {
      last = { ok: false, detail: "log-unavailable" };
      const remaining = deadline - Date.now();
      if (remaining <= 0) return last;
      await sleep(Math.min(pollMs, remaining));
      continue;
    }
    if (read === "timeout") return { ok: false, detail: "evidence-timeout" };

    // 读取期间 Core 可能重启/失联：返回旧 ok 会把上一次启动当成当前证明。
    const after = source.getCurrentCore();
    if (!after) return { ok: false, detail: "current-core-unavailable" };
    if (!sameCoreIdentity(anchor, after)) return { ok: false, detail: "core-restarted" };
    if (Date.now() > deadline) return { ok: false, detail: "evidence-timeout" };

    last = read;
    if (last.ok) return last;
    const remaining = deadline - Date.now();
    if (remaining <= 0) return last;
    await sleep(Math.min(pollMs, remaining));
  }
}

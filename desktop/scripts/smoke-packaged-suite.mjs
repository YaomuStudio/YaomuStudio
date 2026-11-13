#!/usr/bin/env node
/**
 * 打包态冒烟的应用场景 transport 与编排。
 *
 * 与 hello transport 相同的拥有权语义：
 * - 只清理本模块拉起的进程树；kill ACK / error 事件不算退出证据。
 * - 超时、输出溢出、活 child error 先进入有界自有清理；无法确认退出时保留
 *   `exitUnconfirmed` 事实，调用方必须结束整轮而不是继续启动下一个应用。
 * - 不转发原始 stdout/stderr；只保留有界内容用于解析 SMOKE_RESULT，并输出固定
 *   安全摘要（字节数、退出码、清理状态）。
 * - 应用正常完成以 close 为准，确保 stdio 收齐后 SMOKE_RESULT 不因刷新竞态丢失。
 */
import { spawn } from "node:child_process";

export const DEFAULT_CLEANUP_GRACE_MS = 5_000;

/** 只终止本应用拉起的进程树（POSIX 进程组 / Windows taskkill /PID）。 */
export function killTree(child, platform = process.platform) {
  if (!child || child.pid === undefined) return;
  if (platform === "win32") {
    spawn("taskkill", ["/PID", String(child.pid), "/T", "/F"], {
      stdio: "ignore",
      windowsHide: true,
    });
    return;
  }
  try {
    process.kill(-child.pid, "SIGKILL");
  } catch {
    try {
      child.kill("SIGKILL");
    } catch {
      // 已退出。
    }
  }
}

/** 有界缓冲：保留前缀到上限，溢出标记后由调用方立即进入失败/清理路径。 */
function createSink(limit) {
  let text = "";
  let overflow = false;
  return {
    append(chunk) {
      if (overflow) return true;
      const piece = chunk.toString();
      const remaining = limit - text.length;
      if (piece.length > remaining) {
        text += piece.slice(0, Math.max(0, remaining));
        overflow = true;
        return true;
      }
      text += piece;
      return false;
    },
    overflow: () => overflow,
    text: () => text,
    size: () => text.length,
  };
}

/** 解析最后一条 SMOKE_RESULT 单行 JSON；不输出原始内容。 */
export function parseSmokeResult(stdout) {
  const lines = stdout.split(/\r?\n/).filter((line) => line.includes("SMOKE_RESULT "));
  if (lines.length === 0) return undefined;
  const last = lines[lines.length - 1];
  try {
    return JSON.parse(last.slice(last.indexOf("SMOKE_RESULT ") + "SMOKE_RESULT ".length));
  } catch {
    return undefined;
  }
}

/** 固定安全摘要；不含原始 stdout/stderr。 */
export function describeRunFacts(run) {
  return [
    `exit=${run.code ?? "null"}`,
    `signal=${run.signal ?? "null"}`,
    `timed-out=${run.timedOut === true}`,
    `output-overflow=${run.outputOverflow === true}`,
    `exit-unconfirmed=${run.exitUnconfirmed === true}`,
    `live-error=${run.liveError === true}`,
    `closed=${run.closed === true}`,
  ].join(", ");
}

/** 缺 SMOKE_RESULT 时的固定报告行；必须保留清理未确认事实。 */
export function formatMissingResult(mode, run) {
  return `${mode}: no SMOKE_RESULT emitted (${describeRunFacts(run)})`;
}

/**
 * 场景成功判定（纯结果）：断言 ok、正常 close、无活错误、code 0，
 * 且不得超时/溢出/清理未确认/带 console error。
 */
export function isScenarioPass(result, run) {
  if (!result) return false;
  const consoleErrors = Array.isArray(result.consoleErrors) ? result.consoleErrors : [];
  return (
    result.ok === true &&
    run?.code === 0 &&
    run?.closed === true &&
    run?.liveError !== true &&
    run?.timedOut !== true &&
    run?.outputOverflow !== true &&
    run?.exitUnconfirmed !== true &&
    consoleErrors.length === 0
  );
}

/**
 * 运行一次应用场景，返回运行事实（不 reject 活的 child 错误）。
 * `timeoutMs` / `cleanupGraceMs` 都是有界预算；不创建前台窗口。
 */
export function runAppScenario({
  executable,
  args = [],
  cwd,
  env,
  spawnImpl = spawn,
  timeoutMs,
  cleanupGraceMs = DEFAULT_CLEANUP_GRACE_MS,
  stdoutLimit = 4 * 1024 * 1024,
  stderrLimit = 1024 * 1024,
  platform = process.platform,
  killTreeImpl = killTree,
  log = () => undefined,
}) {
  return new Promise((resolve) => {
    const stdout = createSink(stdoutLimit);
    const stderr = createSink(stderrLimit);
    let child;
    try {
      child = spawnImpl(executable, args, {
        env,
        cwd,
        stdio: ["ignore", "pipe", "pipe"],
        detached: platform !== "win32",
      });
    } catch {
      resolve({
        code: null,
        signal: null,
        stdout: "",
        stderr: "",
        timedOut: false,
        outputOverflow: false,
        exitUnconfirmed: false,
        liveError: true,
        closed: false,
      });
      return;
    }

    let exited = child.exitCode !== null || child.signalCode !== null;
    let finalCode = child.exitCode ?? null;
    let finalSignal = child.signalCode ?? null;
    let closed = false;
    let timedOut = false;
    let outputOverflow = false;
    let exitUnconfirmed = false;
    let liveError = false;
    let settled = false;
    let cleanupStarted = false;
    let timer;
    let cleanupTimer;

    const facts = () => ({
      code: finalCode,
      signal: finalSignal,
      stdout: stdout.text(),
      stderr: stderr.text(),
      timedOut,
      outputOverflow,
      exitUnconfirmed,
      liveError,
      closed,
    });
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      clearTimeout(cleanupTimer);
      resolve(facts());
    };
    const beginCleanup = (reason) => {
      if (cleanupStarted || settled) return;
      cleanupStarted = true;
      log(`cleanup started (${reason})`);
      killTreeImpl(child, platform);
      cleanupTimer = setTimeout(() => {
        // 只有 exit/close 证据算清理完成；kill ACK 不算。
        if (!exited && !closed) exitUnconfirmed = true;
        finish();
      }, cleanupGraceMs);
    };

    timer = setTimeout(() => {
      timedOut = true;
      beginCleanup("timeout");
    }, timeoutMs);

    const bindStream = (stream, sink, kind) => {
      if (!stream) return;
      stream.setEncoding?.("utf8");
      stream.on("data", (chunk) => {
        if (sink.append(chunk)) {
          outputOverflow = true;
          beginCleanup(`${kind}-overflow`);
        }
      });
      stream.on("error", () => {
        liveError = true;
        beginCleanup(`${kind}-stream-error`);
      });
    };
    bindStream(child.stdout, stdout, "stdout");
    bindStream(child.stderr, stderr, "stderr");

    child.on("error", () => {
      // spawn 前失败（无 pid）没有自有进程；活 child error 不是退出证据。
      if (child.pid === undefined) {
        exited = true;
        finish();
        return;
      }
      liveError = true;
      beginCleanup("live-error");
    });
    child.once("exit", (code, signal) => {
      exited = true;
      finalCode = code ?? finalCode;
      finalSignal = signal ?? finalSignal;
      // 正常完成等 close 收齐 stdio；清理路径由 cleanupTimer 兜底。
    });
    child.once("close", (code, signal) => {
      closed = true;
      exited = true;
      finalCode = code ?? finalCode;
      finalSignal = signal ?? finalSignal;
      finish();
    });
  });
}

/**
 * 编排 ready → no-core。
 * 只有 ready 已确认 close 且无 exitUnconfirmed 才能继续：
 * - exitUnconfirmed：主进程退出证据缺失；
 * - close 未确认（即使 exit 已到）：stdio/后代未收齐，保守停止整轮。
 * 任一情况都不启动下一个应用或其他 transport。
 */
export async function runScenarioSequence(runReady, runNoCore) {
  const readyRun = await runReady();
  if (readyRun?.exitUnconfirmed === true) {
    return {
      readyRun,
      noCoreRun: null,
      stoppedForUnconfirmed: true,
      stopReason: "exit-unconfirmed",
    };
  }
  if (readyRun?.closed !== true) {
    return {
      readyRun,
      noCoreRun: null,
      stoppedForUnconfirmed: false,
      stopReason: "close-unconfirmed",
    };
  }
  const noCoreRun = await runNoCore();
  return { readyRun, noCoreRun, stoppedForUnconfirmed: false, stopReason: null };
}

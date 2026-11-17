/**
 * WSL 镜像（yaomu）唤醒、自检与模型侧 Runner 启动参数。
 *
 * - 默认启动名为 `yaomu` 的发行版；`YAOMU_WSL_DISTRO` 可覆盖（本机联调可用 Ubuntu）。
 * - 探测不安装、不改默认发行版、不调用 wsl --shutdown。
 * - 缺失的 yaomu 发行版由 wsl-image.ts 在探测前下载并导入。
 * - 镜像自带 ffmpeg/ffprobe/flashcli 等工具；客户端不再探测宿主 FFmpeg。
 * - 开发态把仓库 runner/ 挂到 /mnt/... 后用镜像内 python 拉起协议进程。
 * - 打包态只使用 Main 异步校验后的随包 `resources/core/runner_src` 描述；
 *   显式 YAOMU_WSL_RUNNER_* 覆盖仍兼容，但标为外部来源、不声明包内校验。
 * - 镜像需提供 Python（默认 python3；可用 YAOMU_WSL_PYTHON 覆盖）。
 * - 打包态默认入口来自 Main 校验后的 resources/core/runner_src；显式覆盖仅作兼容。
 *
 * 探测生命周期（第二批）：
 * - warm/tools/runtime 全部走受控异步 spawn，不阻塞主进程事件循环。
 * - 每个探测有固定预算；超时/取消先停止自有 transport 并等待 exit/close 确认。
 * - stdout/stderr 从读取起有界；超限按读取失败处理，不拿截断的工具列表当成功。
 * - 诊断只记录固定 reason/code，不回显原始 stderr、完整命令或环境变量。
 * - 探测 transport 只证明本进程拉起的 wsl.exe 退出，不宣称任意 Linux 后代退出。
 */
import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { app } from "electron";
import { abortError } from "./bootstrap";
import type { BundledRunnerDescriptor } from "./runner-resources";

const DEFAULT_WSL_DISTRO = "yaomu";
const CHECK_RUNTIME_PATH = "/usr/local/bin/check-runtime";
const WARM_TIMEOUT_MS = 45_000;
const TOOL_PROBE_TIMEOUT_MS = 8_000;
const RUNTIME_PROBE_TIMEOUT_MS = 30_000;
/** 每个探测流的最大保留字节；超限后丢弃内容并标记读取失败。 */
const PROBE_STREAM_LIMIT = 64 * 1024;
/** 停止自有 transport 后的确认等待；SIGTERM/SIGKILL 各等一次，始终有界。 */
const PROBE_STOP_GRACE_MS = 2_000;

const TOOL_NAMES = ["flashcli", "mlstools", "ffmpeg", "ffprobe", "python3"] as const;
type WslToolName = (typeof TOOL_NAMES)[number];

export type WslWarmResult = {
  ok: boolean;
  /** 固定 reason/code；不含原始 stderr、完整命令或环境。 */
  detail: string;
};

export type WslTools = {
  flashcli: string | null;
  mlstools: string | null;
  ffmpeg: string | null;
  ffprobe: string | null;
  python3: string | null;
};

export type WslToolsProbeResult = {
  /** 本次探测 transport 是否成功完成；工具缺失不算失败。 */
  ok: boolean;
  tools: WslTools;
  /** 固定 reason/code；成功且无异常时为 null。 */
  detail: string | null;
};

export type WslRuntimeProbe = {
  ok: boolean;
  detail: string;
};

export type WslRunnerOptions = {
  /** 调用方显式探测结果；缺省表示未知，不在此处隐式再跑探测。 */
  tools?: WslTools;
  runtimeReady?: boolean;
  /** 调用方/测试注入的平台；默认 process.platform。 */
  platform?: NodeJS.Platform;
  /** 可信打包标记；默认 app.isPackaged。 */
  packaged?: boolean;
  /** 开发态仓库根；默认 app.getAppPath()/..。 */
  repoRoot?: string | null;
  /**
   * Main 异步校验后的随包资源窄描述；只接受内部已验证值，不做文件系统探测。
   * 打包态缺少它时保持 Runner 不可用，不回退 checkout/host/mock。
   */
  bundledRunner?: BundledRunnerDescriptor | null;
  /** 读取环境变量；默认 process.env。 */
  env?: NodeJS.ProcessEnv;
};

export interface WslProbeOptions {
  /** 覆盖单个探测预算；缺省使用该探测的固定预算。 */
  timeoutMs?: number;
  /** 取消信号：停止自有 transport、确认退出后以 AbortError 失败。 */
  signal?: AbortSignal;
  /** 平台覆盖；默认 process.platform。 */
  platform?: NodeJS.Platform;
  /** 环境覆盖；默认 process.env。 */
  env?: NodeJS.ProcessEnv;
  /** spawn 注入（测试用）；生产仍只调用配置的 wsl.exe。 */
  spawnImpl?: typeof spawn;
  /** 停止后的确认等待；默认 2s。 */
  stopGraceMs?: number;
}

export interface WslPreparationOptions {
  signal?: AbortSignal;
  platform?: NodeJS.Platform;
  env?: NodeJS.ProcessEnv;
  spawnImpl?: typeof spawn;
  stopGraceMs?: number;
  warmTimeoutMs?: number;
  toolsTimeoutMs?: number;
  runtimeTimeoutMs?: number;
  log?: (line: string) => void;
}

export interface WslPreparation {
  tools: WslTools;
  runtimeReady: boolean;
}

/** 超时/取消/活 child 错误后无法确认自有探测 transport 退出。区别于普通探测失败。 */
export class WslProbeCleanupError extends Error {
  readonly reason: "timeout" | "abort" | "error";

  constructor(reason: "timeout" | "abort" | "error") {
    super(`WSL 探测无法确认自有 transport 退出（${reason}）`);
    this.name = "WslProbeCleanupError";
    this.reason = reason;
  }
}

/** Windows 盘符路径 → WSL /mnt/<drive>/...（仅用于拼启动 argv，不做业务文件桥）。 */
export function windowsPathToWslMount(winPath: string): string {
  const trimmed = winPath.trim();
  // 先按 Windows 盘符语义识别，不依赖当前宿主 OS：
  // 在非 Windows 主机上 path.resolve 会把 `C:\...` 当成相对路径拼上 cwd。
  const direct = /^([A-Za-z]):[\\/](.*)$/.exec(trimmed);
  if (direct) {
    return `/mnt/${direct[1].toLowerCase()}/${direct[2].replace(/\\/g, "/")}`;
  }
  const resolved = path.resolve(trimmed);
  const match = /^([A-Za-z]):[\\/](.*)$/.exec(resolved);
  if (!match) return resolved.replace(/\\/g, "/");
  const rest = match[2].replace(/\\/g, "/");
  return `/mnt/${match[1].toLowerCase()}/${rest}`;
}

/**
 * 读取显式配置的 WSL Runner 命令（大小写不敏感、忽略空白）。
 * Windows 环境变量名不区分大小写；只有真正的非空值才算外部来源，
 * 避免空白覆盖被误判为“已配置”而跳过包内资源校验。
 */
export function explicitWslRunnerCommand(
  env: NodeJS.ProcessEnv = process.env,
): string | null {
  for (const key of Object.keys(env)) {
    if (key.toUpperCase() !== "YAOMU_WSL_RUNNER_COMMAND") continue;
    const value = env[key]?.trim();
    if (value) return value;
  }
  return null;
}

export function wslDistro(env: NodeJS.ProcessEnv = process.env): string {
  return env.YAOMU_WSL_DISTRO?.trim() || DEFAULT_WSL_DISTRO;
}

function wslExe(env: NodeJS.ProcessEnv = process.env): string {
  return env.YAOMU_WSL_EXE?.trim() || "wsl.exe";
}

function distroArgs(env: NodeJS.ProcessEnv = process.env): string[] {
  return ["-d", wslDistro(env)];
}

function emptyTools(): WslTools {
  return { flashcli: null, mlstools: null, ffmpeg: null, ffprobe: null, python3: null };
}

function isWslToolName(value: string): value is WslToolName {
  return (TOOL_NAMES as readonly string[]).includes(value);
}

/**
 * 只接受四个已知工具的绝对 POSIX 路径；不接受空白/控制字符或原型属性。
 * 纯解析函数，便于在不启动进程的情况下做回归。
 */
export function parseWslToolsOutput(stdout: string): WslTools {
  const tools = emptyTools();
  for (const line of stdout.split(/\r?\n/)) {
    const separator = line.indexOf("=");
    if (separator <= 0) continue;
    const key = line.slice(0, separator).trim();
    // 不用 `key in tools`：那会接受 toString/constructor/__proto__ 等原型属性。
    if (!isWslToolName(key)) continue;
    const value = line.slice(separator + 1).trim();
    if (!isWslToolPath(value)) continue;
    tools[key] = value;
  }
  return tools;
}

function isWslToolPath(value: string): boolean {
  if (!value.startsWith("/") || value.length > 4_096) return false;
  // 空白/控制字符无法安全地作为镜像内绝对路径传递。
  return !/[\s\u0000-\u001f\u007f]/.test(value);
}

/**
 * 发行版名安全校验：与 Core 的 WSL 探测器保持同一口径。
 * 空值不作为可调用的 distro；前导 '-' 会被 WSL 当成额外选项。
 */
function isSafeWslDistroName(value: string): boolean {
  if (!value || value.length > 64 || value.startsWith("-")) return false;
  return /^[A-Za-z0-9._-]+$/.test(value);
}

/**
 * Core 的环境投影。元数据解析使用随包 ffprobe，不把镜像 ffprobe 交给 Core，
 * 因此导入/重定位不会为了读媒体信息启动 WSL。翻译仍使用镜像内 mlstools。
 * YAOMU_WSL_EXE 仅在父环境显式设置时透传。
 */
function wslMediaProbeEnv(env: NodeJS.ProcessEnv, tools: WslTools | undefined): NodeJS.ProcessEnv {
  const probeEnv: NodeJS.ProcessEnv = {};
  const safeDistro = isSafeWslDistroName(wslDistro(env));
  // 翻译是工具能力：Core 只按桌面探测到的镜像路径标记 translate READY。
  const mlstools = tools?.mlstools;
  if (mlstools && isWslToolPath(mlstools) && safeDistro) {
    probeEnv.YAOMU_WSL_MLSTOOLS_BIN = mlstools;
  }
  const exe = env.YAOMU_WSL_EXE?.trim();
  if (exe) probeEnv.YAOMU_WSL_EXE = exe;
  return probeEnv;
}

/** 读取期间按 UTF-8 字节有界保留；超限后清空并标记失败，避免半截数据被当成成功。 */
class BoundedOutput {
  private readonly chunks: string[] = [];
  private size = 0;
  overflow = false;

  constructor(private readonly limit: number) {}

  push(chunk: string): void {
    if (this.overflow) return;
    const bytes = Buffer.byteLength(chunk, "utf8");
    if (this.size + bytes > this.limit) {
      this.overflow = true;
      this.chunks.length = 0;
      this.size = 0;
      return;
    }
    this.chunks.push(chunk);
    this.size += bytes;
  }

  text(): string {
    return this.overflow ? "" : this.chunks.join("");
  }
}

type WslTransportEvent =
  | { kind: "exit"; code: number | null; signal: NodeJS.Signals | null }
  | { kind: "error"; errorCode: string | null }
  | { kind: "live-error"; errorCode: string | null };

type WslProbeRun =
  | {
      kind: "exit";
      code: number | null;
      signal: NodeJS.Signals | null;
      stdout: string;
      stdoutOverflow: boolean;
      stderrOverflow: boolean;
    }
  | {
      kind: "error";
      errorCode: string | null;
      stdout: string;
      stdoutOverflow: boolean;
      stderrOverflow: boolean;
    }
  | {
      kind: "timeout";
      timeoutMs: number;
      stdout: string;
      stdoutOverflow: boolean;
      stderrOverflow: boolean;
    };

function bindOutput(stream: NodeJS.ReadableStream | null, sink: BoundedOutput): void {
  if (!stream) return;
  stream.setEncoding("utf8");
  stream.on("data", (chunk: string) => sink.push(chunk));
}

function errorCodeOf(error: unknown): string | null {
  if (typeof error === "object" && error !== null && "code" in error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (typeof code === "string") return code;
  }
  return null;
}

/**
 * 观察自有 transport 的退出。
 * - 正常完成以 close 为准，确保 stdout 已收齐；
 * - exit 先到只更新 exited，供停止/确认路径使用；
 * - pid 不存在时的 spawn error 直接完成，表示没有可清理的进程；
 * - 已有 pid 的活 child error（如 kill EPERM）不是退出证据：另行通知，
 *   由调用方走有界 cleanup，且不解除 exit/close 监听。
 */
function observeTransport(child: ChildProcess): {
  event: Promise<WslTransportEvent>;
  liveError: Promise<WslTransportEvent>;
  exited: () => boolean;
  dispose: () => void;
} {
  const spawnedPid = child.pid;
  let exited = child.exitCode !== null || child.signalCode !== null;
  let settled = false;
  let liveErrorReported = false;
  let resolveEvent!: (event: WslTransportEvent) => void;
  let resolveLiveError!: (event: WslTransportEvent) => void;
  const event = new Promise<WslTransportEvent>((resolve) => {
    resolveEvent = resolve;
  });
  const liveError = new Promise<WslTransportEvent>((resolve) => {
    resolveLiveError = resolve;
  });

  function onError(error: NodeJS.ErrnoException): void {
    const errorCode = errorCodeOf(error);
    if (spawnedPid === undefined) {
      // spawn 成功前失败：没有自有进程可清理，error 就是终止事实。
      exited = true;
      finish({ kind: "error", errorCode });
      return;
    }
    // 活 child 的 error 不能当退出证据，也不能据此继续后续探测。
    if (liveErrorReported) return;
    liveErrorReported = true;
    resolveLiveError({ kind: "live-error", errorCode });
  }

  function onExit(): void {
    exited = true;
    // 等 close 收齐 stdio；若 close 迟迟不到，由探测预算超时兜底。
  }

  function onClose(code: number | null, signal: NodeJS.Signals | null): void {
    exited = true;
    finish({ kind: "exit", code, signal });
  }

  function dispose(): void {
    child.removeListener("error", onError);
    child.removeListener("exit", onExit);
    child.removeListener("close", onClose);
  }

  function finish(next: WslTransportEvent): void {
    if (settled) return;
    settled = true;
    dispose();
    resolveEvent(next);
  }

  child.on("error", onError);
  child.once("exit", onExit);
  child.once("close", onClose);

  return { event, liveError, exited: () => exited || settled, dispose };
}

function settleWithin(promise: Promise<unknown>, ms: number): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false;
    let timer: NodeJS.Timeout | undefined;
    const finish = (value: boolean): void => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      resolve(value);
    };
    timer = setTimeout(() => finish(false), ms);
    void promise.then(
      () => finish(true),
      () => finish(true),
    );
  });
}

/**
 * 先停自有 transport（SIGTERM → 有界等待 → SIGKILL → 有界等待）。
 * 只有 exit/close 证据才返回 true；发送信号本身不算已停止。
 */
async function stopTransport(
  child: ChildProcess,
  transport: { event: Promise<WslTransportEvent>; exited: () => boolean },
  graceMs: number,
): Promise<boolean> {
  if (transport.exited()) return true;
  try {
    child.kill();
  } catch {
    // 已退出或无法发送；由退出证据判断。
  }
  if ((await settleWithin(transport.event, graceMs)) || transport.exited()) return true;
  try {
    child.kill("SIGKILL");
  } catch {
    // 同上。
  }
  return (await settleWithin(transport.event, graceMs)) || transport.exited();
}

async function runWslProbe(options: {
  argv: string[];
  timeoutMs: number;
  env: NodeJS.ProcessEnv;
  spawnImpl: typeof spawn;
  stopGraceMs: number;
  signal?: AbortSignal;
}): Promise<WslProbeRun> {
  if (options.signal?.aborted) throw abortError("WSL 探测已取消");

  let child: ChildProcess;
  try {
    child = options.spawnImpl(wslExe(options.env), options.argv, {
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    return {
      kind: "error",
      errorCode: errorCodeOf(error),
      stdout: "",
      stdoutOverflow: false,
      stderrOverflow: false,
    };
  }

  const stdout = new BoundedOutput(PROBE_STREAM_LIMIT);
  const stderr = new BoundedOutput(PROBE_STREAM_LIMIT);
  bindOutput(child.stdout, stdout);
  bindOutput(child.stderr, stderr);
  const transport = observeTransport(child);

  // 宿主进程退出时兜底终止自有探测 transport；确认退出后解绑。
  const killOnParentExit = (): void => {
    try {
      child.kill("SIGKILL");
    } catch {
      // 进程已退出。
    }
  };
  process.once("exit", killOnParentExit);
  const releaseParentFallback = (): void => {
    process.removeListener("exit", killOnParentExit);
  };

  let timer: NodeJS.Timeout | undefined;
  let onAbort: (() => void) | undefined;
  const budget = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => resolve("timeout"), options.timeoutMs);
  });
  const aborted = options.signal
    ? new Promise<"abort">((resolve) => {
        // spawn 后、监听器注册前不会插入其他任务；这里仍显式处理已取消，
        // 避免依赖事件补发，也保证已拉起的自有 transport 走同一停止路径。
        if (options.signal!.aborted) {
          resolve("abort");
          return;
        }
        onAbort = () => resolve("abort");
        options.signal!.addEventListener("abort", onAbort, { once: true });
      })
    : null;

  try {
    const first = await Promise.race([
      transport.event,
      transport.liveError,
      budget,
      ...(aborted ? [aborted] : []),
    ]);

    if (typeof first !== "string") {
      if (first.kind === "exit") {
        return {
          kind: "exit",
          code: first.code,
          signal: first.signal,
          stdout: stdout.text(),
          stdoutOverflow: stdout.overflow,
          stderrOverflow: stderr.overflow,
        };
      }
      if (first.kind === "error") {
        return {
          kind: "error",
          errorCode: first.errorCode,
          stdout: stdout.text(),
          stdoutOverflow: stdout.overflow,
          stderrOverflow: stderr.overflow,
        };
      }
      // 活 child error 不是退出证据：先做有界清理；无法确认则明确失败，
      // 确认退出后才按普通 transport 失败返回。
      const errorConfirmed = await stopTransport(child, transport, options.stopGraceMs);
      if (!errorConfirmed) throw new WslProbeCleanupError("error");
      return {
        kind: "error",
        errorCode: first.errorCode,
        stdout: stdout.text(),
        stdoutOverflow: stdout.overflow,
        stderrOverflow: stderr.overflow,
      };
    }

    const confirmed = await stopTransport(child, transport, options.stopGraceMs);
    if (!confirmed) {
      // 未确认退出：保留父进程 exit 兜底，按明确失败上报，不伪造成普通超时/取消。
      throw new WslProbeCleanupError(first);
    }
    if (first === "abort") throw abortError("WSL 探测已取消");
    return {
      kind: "timeout",
      timeoutMs: options.timeoutMs,
      stdout: stdout.text(),
      stdoutOverflow: stdout.overflow,
      stderrOverflow: stderr.overflow,
    };
  } finally {
    if (timer) clearTimeout(timer);
    if (onAbort) options.signal?.removeEventListener("abort", onAbort);
    if (transport.exited()) {
      transport.dispose();
      releaseParentFallback();
    } else {
      // 未确认退出：保留自有 child 的 exit/close 监听，迟到退出后才解绑父进程兜底；
      // 兜底在此之前仍会 SIGKILL 自有 child。不能因清理失败就永久泄漏 listener。
      void transport.event.then(releaseParentFallback);
    }
  }
}

function describeRunFailure(run: WslProbeRun): string {
  if (run.kind === "timeout") return `timeout after ${run.timeoutMs}ms`;
  if (run.kind === "error") {
    return run.errorCode ? `spawn-error ${run.errorCode}` : "spawn-error";
  }
  return `exit=${String(run.code)} signal=${String(run.signal)}`;
}

function probeRunOptions(
  options: WslProbeOptions,
  defaultTimeoutMs: number,
): {
  timeoutMs: number;
  env: NodeJS.ProcessEnv;
  spawnImpl: typeof spawn;
  stopGraceMs: number;
  signal?: AbortSignal;
} {
  return {
    timeoutMs: options.timeoutMs ?? defaultTimeoutMs,
    env: options.env ?? process.env,
    spawnImpl: options.spawnImpl ?? spawn,
    stopGraceMs: options.stopGraceMs ?? PROBE_STOP_GRACE_MS,
    signal: options.signal,
  };
}

/**
 * 轻量唤醒并启动默认（或 YAOMU_WSL_DISTRO）发行版。
 * 普通失败以固定 reason 返回；取消/无法确认清理则 reject（由调用方停止后续步骤）。
 */
export async function warmWsl(options: WslProbeOptions = {}): Promise<WslWarmResult> {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  if (platform !== "win32") return { ok: true, detail: "skip: not windows" };

  const run = await runWslProbe({
    ...probeRunOptions(options, WARM_TIMEOUT_MS),
    argv: [...distroArgs(env), "-e", "true"],
  });
  if (run.kind === "exit" && run.code === 0) {
    if (run.stdoutOverflow || run.stderrOverflow) {
      return { ok: false, detail: "output-overflow" };
    }
    return { ok: true, detail: "warm ok" };
  }
  return { ok: false, detail: describeRunFailure(run) };
}

/**
 * 运行镜像自带的 check-runtime；这是镜像对自身媒体/模型工具的验收，
 * 客户端不再检查宿主 FFmpeg。
 */
export async function probeWslRuntime(
  options: WslProbeOptions = {},
): Promise<WslRuntimeProbe> {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  if (platform !== "win32") return { ok: true, detail: "skip: not windows" };

  const run = await runWslProbe({
    ...probeRunOptions(options, RUNTIME_PROBE_TIMEOUT_MS),
    argv: [...distroArgs(env), "-e", CHECK_RUNTIME_PATH],
  });
  if (run.kind === "exit" && run.code === 0) {
    if (run.stdoutOverflow || run.stderrOverflow) {
      return { ok: false, detail: "output-overflow" };
    }
    return { ok: true, detail: "check-runtime ok" };
  }
  return { ok: false, detail: describeRunFailure(run) };
}

/**
 * 在镜像内解析工具绝对路径；不依赖宿主 PATH。
 * stdout 超限按读取失败处理，不返回截断的工具列表。
 */
export async function probeWslTools(
  options: WslProbeOptions = {},
): Promise<WslToolsProbeResult> {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  if (platform !== "win32") {
    return { ok: true, tools: emptyTools(), detail: "skip: not windows" };
  }

  const script =
    "for tool in flashcli mlstools ffmpeg ffprobe python3; do " +
    'printf "%s=%s\\n" "$tool" "$(command -v "$tool" 2>/dev/null || true)"; ' +
    "done";
  const run = await runWslProbe({
    ...probeRunOptions(options, TOOL_PROBE_TIMEOUT_MS),
    argv: [...distroArgs(env), "-e", "bash", "-lc", script],
  });

  if (run.kind !== "exit" || run.code !== 0) {
    return { ok: false, tools: emptyTools(), detail: describeRunFailure(run) };
  }
  if (run.stdoutOverflow || run.stderrOverflow) {
    return { ok: false, tools: emptyTools(), detail: "output-overflow" };
  }
  return { ok: true, tools: parseWslToolsOutput(run.stdout), detail: null };
}

function formatTools(tools: WslTools): string {
  return (
    `ffmpeg=${tools.ffmpeg ?? "missing"}, ` +
    `ffprobe=${tools.ffprobe ?? "missing"}, ` +
    `flashcli=${tools.flashcli ?? "missing"}, ` +
    `python3=${tools.python3 ?? "missing"}`
  );
}

/**
 * 顺序执行 WSL 准备：warm → tools → runtime。
 * - 普通探测失败继续后续步骤，并进入 Core 的有限模式；
 * - 取消/无法确认清理直接 reject，调用方不得继续探测或启动 Core；
 * - 日志只包含固定 reason 与解析后的工具路径，不回显原始 stderr/命令/环境。
 */
export async function prepareWslEnvironment(
  options: WslPreparationOptions = {},
): Promise<WslPreparation> {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  const log = options.log ?? (() => undefined);
  const common: WslProbeOptions = {
    signal: options.signal,
    platform,
    env,
    spawnImpl: options.spawnImpl,
    stopGraceMs: options.stopGraceMs,
  };

  if (platform !== "win32") {
    return { tools: emptyTools(), runtimeReady: true };
  }

  const warm = await warmWsl({ ...common, timeoutMs: options.warmTimeoutMs });
  log(
    warm.ok
      ? `[bootstrap] wsl warm ok: ${warm.detail}`
      : `[bootstrap] wsl warm failed: ${warm.detail}`,
  );

  const toolsProbe = await probeWslTools({
    ...common,
    timeoutMs: options.toolsTimeoutMs,
  });
  log(
    toolsProbe.ok
      ? `[bootstrap] yaomu tools: ${formatTools(toolsProbe.tools)}`
      : `[bootstrap] yaomu tools probe failed: ${toolsProbe.detail ?? "unknown"}`,
  );
  if (!toolsProbe.tools.python3) {
    log(
      "[bootstrap] yaomu image has no python3 — WSL Runner 无法启动；" +
        "请在镜像中提供 python3，或用 YAOMU_WSL_PYTHON 指定镜像内解释器",
    );
  }

  const runtime = await probeWslRuntime({
    ...common,
    timeoutMs: options.runtimeTimeoutMs,
  });
  log(
    `[bootstrap] yaomu runtime: ${runtime.ok ? "ok" : "failed"} — ${runtime.detail}`,
  );

  return { tools: toolsProbe.tools, runtimeReady: runtime.ok };
}

/**
 * 翻译 provider 凭据白名单：仅宿主显式存在的变量经 WSLENV 桥接进镜像。
 * 值不进入 wsl.exe argv、不写日志；只传变量名。
 */
const TRANSLATION_ENV_NAMES = new Set([
  "GEMINI_MODEL_API_KEY",
  "GEMINI_MODEL_NAME",
  "GEMINI_MODEL_BASE_URL",
  "ARK_API_KEY",
  "MAX_RETRY_COUNT",
]);
const TRANSLATION_ENV_PREFIXES = [
  "DEEPSEEK_",
  "QWEN_MODEL_",
  "DOUBAO_",
  "HY_MODEL_",
  "SUBTITLE_TRANSLATION_",
];

function isTranslationEnvName(name: string): boolean {
  if (TRANSLATION_ENV_NAMES.has(name)) return true;
  return TRANSLATION_ENV_PREFIXES.some((prefix) => name.startsWith(prefix));
}

/**
 * 把宿主 provider 变量投影为 WSLENV 桥接环境；没有白名单变量时返回空对象。
 * 已有 WSLENV 条目保留，新增条目使用 `/u` 让 wsl.exe 透传。
 */
export function translationEnvForWsl(source: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const bridged: string[] = [];
  const env: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(source)) {
    if (!value || !isTranslationEnvName(name)) continue;
    env[name] = value;
    bridged.push(name);
  }
  if (bridged.length === 0) return {};

  const entries: string[] = [];
  const seen = new Set<string>();
  const add = (entry: string): void => {
    const name = entry.split("/")[0]?.trim();
    if (!name || seen.has(name)) return;
    seen.add(name);
    entries.push(entry);
  };
  for (const entry of String(source.WSLENV ?? "").split(":")) {
    if (entry.trim()) add(entry.trim());
  }
  for (const name of bridged.sort()) add(`${name}/u`);
  env.WSLENV = entries.join(":");
  return env;
}

/**
 * 解析模型侧 WSL Runner 启动环境。
 * 显式 YAOMU_WSL_RUNNER_COMMAND 优先（外部来源）；打包态只用调用方传入的
 * 内部已验证随包描述；开发态自动拼 wsl + 仓库 runner/src。
 * `tools` 只使用调用方显式探测结果；缺省视为未知/未提供，不在此处隐式探测。
 * 不再注入隐式 YAOMU_MOCK_MODEL_SLOTS：只有开发者显式设置时才沿用，
 * 打包态的最终 spawn env 清理由 bootstrap.buildCoreSpawnEnv 负责。
 */
export function resolveWslRunnerEnv(options: WslRunnerOptions = {}): NodeJS.ProcessEnv {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  if (platform !== "win32") return {};
  const base: NodeJS.ProcessEnv = {
    YAOMU_WSL_DISTRO: wslDistro(env),
    // provider 凭据只经 WSLENV 桥接，不进 argv；所有返回路径都带上。
    ...translationEnvForWsl(env),
    // 显式外部 Runner/打包缺 Runner 的早返回路径也保留翻译工具注入。
    // 元数据 ffprobe 由随包二进制提供，不从镜像投影。
    ...wslMediaProbeEnv(env, options.tools),
  };
  if (options.runtimeReady) base.YAOMU_WSL_RUNTIME_READY = "1";
  if (explicitWslRunnerCommand(env)) {
    // 调用方已完整配置；仅透传 distro 提示给 Core 的 WSL 探测。
    // Main 会把该来源标记为 external，不当作包内资源证明。
    return base;
  }

  const python = env.YAOMU_WSL_PYTHON?.trim() || "python3";
  const envPairs = runnerEnvPairs(env, options.tools);
  const packaged = options.packaged ?? app.isPackaged;
  if (packaged) {
    // 打包态绝不推导仓库内 Runner；只接受 Main 异步校验后的随包描述。
    const bundled = options.bundledRunner;
    if (!bundled || !bundled.sourceDir) {
      // 资源缺失/校验失败：Runner 不可用，不回退 checkout/host/mock。
      return base;
    }
    // PYTHONPATH 只含已验证源码根；-B 不写包内 pyc，-P 阻止 cwd 遮蔽，
    // -s 排除用户 site；固定模块入口不来自清单或用户输入。
    const args = [
      ...distroArgs(env),
      "-e",
      "env",
      `PYTHONPATH=${windowsPathToWslMount(bundled.sourceDir)}`,
      ...envPairs,
      python,
      "-B",
      "-P",
      "-s",
      "-m",
      "yaomustudio_runner",
    ];
    return {
      ...base,
      YAOMU_WSL_RUNNER_COMMAND: wslExe(env),
      YAOMU_WSL_RUNNER_ARGS: JSON.stringify(args),
    };
  }

  const repoRoot =
    options.repoRoot !== undefined ? options.repoRoot : path.join(app.getAppPath(), "..");
  if (!repoRoot) {
    return base;
  }
  const runnerSrc = path.join(repoRoot, "runner", "src");
  if (!fs.existsSync(path.join(runnerSrc, "yaomustudio_runner"))) {
    return base;
  }
  const args = [
    ...distroArgs(env),
    "-e",
    "env",
    `PYTHONPATH=${windowsPathToWslMount(runnerSrc)}`,
    ...envPairs,
    python,
    "-m",
    "yaomustudio_runner",
  ];
  // 开发态只有显式设置才保留演示槽位；不再生成 separation,voice-clone 默认值。
  const explicitMockSlots = env.YAOMU_MOCK_MODEL_SLOTS?.trim();
  return {
    ...base,
    YAOMU_WSL_RUNNER_COMMAND: wslExe(env),
    YAOMU_WSL_RUNNER_ARGS: JSON.stringify(args),
    ...(explicitMockSlots ? { YAOMU_MOCK_MODEL_SLOTS: explicitMockSlots } : {}),
  };
}

// 已准入的分离、克隆、ASR bundle 目前只带 cu130 runtime。yaomu 镜像的默认
// 探测标签是 cu124；不覆盖时 FlashCLI 在加载前退出（no runtime environment），
// 提取节点失败，Run 仍可能是 PARTIAL。这是已确认的运行时标签，不是新的 CLI 参数。
// 宿主显式 FLASHCLI_CUDA_TAG 仍然优先。
const ADMITTED_FLASHCLI_CUDA_TAG = "cu130";

/** 镜像内工具与显式运行时覆盖；顺序保持既有确认过的 argv。 */
function runnerEnvPairs(env: NodeJS.ProcessEnv, tools: WslTools | undefined): string[] {
  const envPairs: string[] = [];
  if (tools?.flashcli) {
    envPairs.push(`FLASHCLI_BIN=${tools.flashcli}`);
    // 有二进制时强制能力默认走 flashcli，避免仍落 mock。
    envPairs.push("YAOMU_CAPABILITY_EXECUTOR=flashcli");
  }
  if (tools?.mlstools) {
    envPairs.push(`MLSTOOLS_BIN=${tools.mlstools}`);
  }
  // 显式环境覆盖：宿主已设置的标签优先；未设置且已探测到 FlashCLI 时使用已准入的 cu130。
  const cudaTag =
    env.FLASHCLI_CUDA_TAG?.trim() || (tools?.flashcli ? ADMITTED_FLASHCLI_CUDA_TAG : "");
  if (cudaTag) envPairs.push(`FLASHCLI_CUDA_TAG=${cudaTag}`);
  const checkpoint = env.FLASHCLI_CHECKPOINT?.trim();
  if (checkpoint?.startsWith("/")) {
    envPairs.push(`FLASHCLI_CHECKPOINT=${checkpoint}`);
  }
  if (tools?.ffmpeg) envPairs.push(`FFMPEG_BIN=${tools.ffmpeg}`);
  if (tools?.ffprobe) envPairs.push(`FFPROBE_BIN=${tools.ffprobe}`);
  return envPairs;
}

/**
 * Core 引导（Bootstrap）：定位并拉起 App Server、分配回环端口与实例令牌、等待健康、写 bootstrap.json。
 *
 * 边界（docs/local-api-contract.md §1）：
 * - 只绑定 127.0.0.1；端口由 Shell 随机分配后经私有环境变量交给 Core。
 * - 实例令牌只经私有通道（子进程环境 / bootstrap.json 之外的进程内状态）传递，
 *   不写入 URL、日志或 Renderer 持久化存储；bootstrap.json 只记录端口/进程等引导事实。
 * - 本模块不依赖 electron，便于在普通 node 进程里做无窗口验证。
 */
import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface CoreLaunchCommand {
  command: string;
  args: string[];
  cwd?: string;
}

export interface CoreConnection {
  baseUrl: string;
  token: string;
}

export interface CoreProcessInfo extends CoreConnection {
  child: ChildProcess;
  port: number;
  pid: number;
  startedAt: string;
}

export interface BootstrapOptions {
  /** Core 数据目录（YAOMU_DATA_DIR）；bootstrap.json 也写在这里。 */
  dataDir: string;
  launch: CoreLaunchCommand;
  /** 追加/覆盖给子进程的环境变量（不含实例令牌与端口，二者由本模块注入）。 */
  env?: NodeJS.ProcessEnv;
  /**
   * main 注入的可信打包标记（app.isPackaged）。
   * 打包态会在最终 spawn env 副本中清理由代码核实的演示键（YAOMU_MOCK_MODEL_SLOTS，
   * 含大小写变体；Windows 环境变量名不区分大小写），不继承父进程或 options.env 里的
   * 开发默认值，也不修改全局 process.env。
   */
  packaged: boolean;
  port?: number;
  token?: string;
  healthTimeoutMs?: number;
  healthIntervalMs?: number;
  fetchImpl?: FetchLike;
  /**
   * 可选取消信号。取消时终止本模块拉起的 child，并在确认其退出后 reject AbortError；
   * 不把 abort 本身当成进程已退出的证据。
   */
  signal?: AbortSignal;
  log?: (line: string) => void;
}

export class CoreBootstrapError extends Error {
  readonly code: string;
  readonly hint?: string;

  constructor(code: string, message: string, hint?: string) {
    super(message);
    this.name = "CoreBootstrapError";
    this.code = code;
    this.hint = hint;
  }
}

const DEFAULT_HEALTH_TIMEOUT_MS = 30_000;
const DEFAULT_HEALTH_INTERVAL_MS = 250;
/** 取消/健康失败后终止自有 child 的优雅等待；SIGKILL 阶段由 terminateProcessTree 固定 5s。 */
const CANCEL_TERMINATE_GRACE_MS = 5_000;

/** 取一个随机回环端口。 */
export function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.unref();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (address === null || typeof address === "string") {
        server.close(() => reject(new Error("无法分配回环端口")));
        return;
      }
      const { port } = address;
      server.close(() => resolve(port));
    });
  });
}

/** 实例能力令牌：256 位随机值，只经私有通道交付。 */
export function generateInstanceToken(): string {
  return randomBytes(32).toString("base64url");
}

/** 取消等待时使用的稳定错误类型（AbortError 表示取消，不是进程退出证据）。 */
export function abortError(message = "操作已取消"): Error {
  const error = new Error(message);
  error.name = "AbortError";
  return error;
}

export function isAbortError(error: unknown): error is Error {
  return error instanceof Error && error.name === "AbortError";
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw abortError("Core 启动已取消");
}

/** 可被 signal 取消的有界等待；取消时清理 timer 与监听器。 */
function delay(ms: number, signal?: AbortSignal): Promise<void> {
  if (!signal) return new Promise((resolve) => setTimeout(resolve, ms));
  if (signal.aborted) return Promise.reject(abortError("Core 启动已取消"));
  return new Promise((resolve, reject) => {
    let timer: NodeJS.Timeout | undefined;
    const cleanup = (): void => {
      if (timer) clearTimeout(timer);
      signal.removeEventListener("abort", onAbort);
    };
    const onAbort = (): void => {
      cleanup();
      reject(abortError("Core 启动已取消"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
    timer = setTimeout(() => {
      cleanup();
      resolve();
    }, ms);
  });
}

async function probeHealth(
  baseUrl: string,
  token: string,
  fetchImpl: FetchLike,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<boolean> {
  try {
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    const response = await fetchImpl(`${baseUrl}/health/live`, {
      headers: { Accept: "application/json", "X-YaoMu-Token": token },
      signal: signal ? AbortSignal.any([timeoutSignal, signal]) : timeoutSignal,
    });
    if (!response.ok) return false;
    const body = (await response.json()) as { status?: unknown };
    return body.status === "ok";
  } catch {
    return false;
  }
}

export interface WaitForHealthOptions {
  baseUrl: string;
  token: string;
  timeoutMs?: number;
  intervalMs?: number;
  fetchImpl?: FetchLike;
  /** 可选取消信号：取消后尽快抛出 AbortError，不继续轮询。 */
  signal?: AbortSignal;
  /** 每轮之间检查子进程是否已退出，避免干等。 */
  isAlive?: () => boolean;
}

/** 轮询 /health/live 直到 Core 存活；失败抛 CORE_NOT_READY，取消抛 AbortError。 */
export async function waitForHealth(options: WaitForHealthOptions): Promise<void> {
  const timeoutMs = options.timeoutMs ?? DEFAULT_HEALTH_TIMEOUT_MS;
  const intervalMs = options.intervalMs ?? DEFAULT_HEALTH_INTERVAL_MS;
  const fetchImpl = options.fetchImpl ?? fetch;
  const signal = options.signal;
  const deadline = Date.now() + timeoutMs;
  throwIfAborted(signal);
  while (Date.now() < deadline) {
    throwIfAborted(signal);
    if (options.isAlive && !options.isAlive()) {
      throw new CoreBootstrapError("CORE_NOT_READY", "Core 在就绪前退出");
    }
    if (
      await probeHealth(options.baseUrl, options.token, fetchImpl, Math.min(5_000, timeoutMs), signal)
    ) {
      throwIfAborted(signal);
      return;
    }
    await delay(intervalMs, signal);
  }
  // 截止时间恰好与取消重叠时，取消优先于超时错误。
  throwIfAborted(signal);
  throw new CoreBootstrapError(
    "CORE_NOT_READY",
    `Core 在 ${timeoutMs}ms 内未通过 /health/live 检查`,
    "确认 Core 版本与契约一致，且未被安全软件阻止监听 127.0.0.1。",
  );
}

/** 最终 spawn env 的输入；测试可注入父环境/覆盖环境且不会被修改。 */
export interface CoreSpawnEnvInput {
  dataDir: string;
  port: number;
  token: string;
  packaged: boolean;
  /** 父进程环境；默认 process.env。 */
  parentEnv?: NodeJS.ProcessEnv;
  /** options.env。 */
  overrideEnv?: NodeJS.ProcessEnv;
}

/**
 * 构造最终交给 spawn 的环境变量副本。
 *
 * 合并顺序与 startCore 原实现一致：父环境 → options.env → Bootstrap 私有键。
 * 合并完成后按可信 packaged 标记清理演示键，避免父环境或调用方覆盖把
 * YAOMU_MOCK_MODEL_SLOTS 带进正式 GUI，让 Core 显示假 READY。
 * 清理按 key 大小写不敏感匹配：Windows 环境变量名不区分大小写，Node 按
 * 字典序保留首个变体，只删精确键会漏掉 yaomu_mock_model_slots 等变体。
 * 只清理代码核实的演示键，不宽泛删除 MOCK/TEST 或真实 WSL/FlashCLI 配置。
 */
export function buildCoreSpawnEnv(input: CoreSpawnEnvInput): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    ...(input.parentEnv ?? process.env),
    ...input.overrideEnv,
  };
  if (input.packaged) {
    for (const key of Object.keys(env)) {
      if (key.toUpperCase() === "YAOMU_MOCK_MODEL_SLOTS") delete env[key];
    }
  }
  env.YAOMU_DATA_DIR = input.dataDir;
  env.YAOMU_CORE_PORT = String(input.port);
  env.YAOMU_INSTANCE_TOKEN = input.token;
  return env;
}

/**
 * 拉起 Core 并等待就绪。
 *
 * 子进程通过环境变量获得：
 * - YAOMU_DATA_DIR：数据目录（业务库由 Core 独占写入）
 * - YAOMU_CORE_PORT：Shell 分配的随机回环端口
 * - YAOMU_INSTANCE_TOKEN：本次实例令牌（只经私有通道）
 * - YAOMU_RELEASE_BASE_URLS：批准的模型分发源 URL 前缀（可选；缺省时 Core 用内嵌目录前缀）
 * - YAOMU_RELEASE_CATALOG：覆盖内嵌产品 interim 目录的 JSON 路径，或 none 关闭
 * - YAOMU_WSL_RUNNER_COMMAND / YAOMU_WSL_RUNNER_ARGS：模型侧 WSL Runner（JSON 数组 args）
 * - YAOMU_WSL_DISTRO：可选发行版名（与探测默认发行版一致时可省略）
 * - YAOMU_SHUTDOWN_ON_STDIN=1：Shell 关闭 stdin 管道时 Core 走正常退出并释放 Runner 租约
 * - 打包态从最终 env 副本删除 YAOMU_MOCK_MODEL_SLOTS（含大小写变体）；开发态只沿用显式设置，不生成隐式默认值。
 */
export async function startCore(options: BootstrapOptions): Promise<CoreProcessInfo> {
  const log = options.log ?? (() => undefined);
  const signal = options.signal;
  throwIfAborted(signal);
  const port = options.port ?? (await findFreePort());
  throwIfAborted(signal);
  const token = options.token ?? generateInstanceToken();
  const baseUrl = `http://127.0.0.1:${port}`;
  await mkdir(options.dataDir, { recursive: true });
  // 异步端口/目录准备完成后、spawn 前再次检查取消，避免为已取消的启动创建 child。
  throwIfAborted(signal);

  const env = buildCoreSpawnEnv({
    dataDir: options.dataDir,
    port,
    token,
    packaged: options.packaged,
    overrideEnv: options.env,
  });
  // 与下面的 stdin 管道成对出现。Windows 不能用 SIGTERM 通知 Core。
  env.YAOMU_SHUTDOWN_ON_STDIN = "1";
  const child = spawn(options.launch.command, options.launch.args, {
    cwd: options.launch.cwd,
    env,
    // POSIX 下建立独立进程组，退出时只清理本应用拉起的进程树。
    detached: process.platform !== "win32",
    windowsHide: true,
    stdio: ["pipe", "pipe", "pipe"],
  });
  // 子进程先退出时 end() 可能触发 EPIPE；这不是启动失败。
  child.stdin?.on("error", () => undefined);

  const pipeLines = (stream: NodeJS.ReadableStream | null, prefix: string): void => {
    if (!stream) return;
    let buffer = "";
    stream.setEncoding("utf8");
    stream.on("data", (chunk: string) => {
      buffer += chunk;
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        if (line.trim()) log(`${prefix} ${line.slice(0, 2_000)}`);
      }
    });
  };
  pipeLines(child.stdout, "[core:stdout]");
  pipeLines(child.stderr, "[core:stderr]");

  // 兜底：宿主进程退出时同步终止本次拉起的 Core 进程组，避免孤儿进程。
  // 只解绑本兜底监听（child exit 时），不 removeAllListeners，避免误删其他监听器。
  const killOnParentExit = (): void => {
    if (child.exitCode === null && child.signalCode === null) {
      signalProcessTree(child, "SIGKILL");
    }
  };
  process.once("exit", killOnParentExit);
  const unbindParentExit = (): void => {
    process.removeListener("exit", killOnParentExit);
  };
  child.once("exit", unbindParentExit);
  const startedAt = new Date().toISOString();

  const info = await new Promise<CoreProcessInfo>((resolve, reject) => {
    let settled = false;
    let closing = false;
    /** onAbort 启动的终止确认；success 路径在取消后等待它再结束。 */
    let abortCleanup: Promise<void> | null = null;
    /** 是否已经开始写 ready；为 false 时取消不创建 bootstrap.json。 */
    let readyWriteStarted = false;
    /** 本代 ready 写；取消/失败必须先等它 settle，再串行写最终状态。 */
    let readyWrite: Promise<void> | null = null;

    // 同一代 bootstrap.json 写入串行化：所有写共享一条链，不并行写同一文件。
    let writeTail: Promise<void> = Promise.resolve();

    /** bootstrap.json 只由本模块写；写入失败只记录日志，不打断生命周期。 */
    const publishBootstrapState = (state: BootstrapFileSnapshot["state"]): Promise<void> => {
      const run = writeTail.then(async () => {
        try {
          await writeBootstrapFile(
            options.dataDir,
            { port, pid: child.pid ?? -1, startedAt },
            state,
          );
        } catch (error) {
          log(`[bootstrap] 写入 bootstrap.json 失败：${describe(error)}`);
        }
      });
      writeTail = run;
      return run;
    };

    /** 等本代所有已开始的写完成；卡住的 IO 由 Supervisor 有界隔离，不超时丢弃拥有者。 */
    const waitForWrites = async (): Promise<void> => {
      if (readyWrite) await readyWrite.catch(() => undefined);
      await writeTail.catch(() => undefined);
    };

    const releaseParentFallback = (): void => {
      child.removeListener("exit", unbindParentExit);
      process.removeListener("exit", killOnParentExit);
    };

    const detachWaitListeners = (): void => {
      child.removeListener("exit", onEarlyExit);
      signal?.removeEventListener("abort", onAbort);
    };

    function settleFailure(error: Error): void {
      if (settled) return;
      settled = true;
      child.removeListener("error", onError);
      // 走到这里时 child 已退出或从未成功启动，可以安全释放父进程兜底。
      releaseParentFallback();
      reject(error);
    }

    /**
     * 失败路径：ready 写已开始时，必须等它 settle 并串行纠正为 crashed，
     * 再 reject；不留 startCore settled 之后的写尾巴。
     */
    function fail(error: Error): void {
      if (settled || closing) return;
      if (!readyWriteStarted) {
        detachWaitListeners();
        settleFailure(error);
        return;
      }
      closing = true;
      detachWaitListeners();
      void (async () => {
        try {
          await waitForWrites();
          await publishBootstrapState("crashed");
        } catch {
          // publishBootstrapState 自身吞掉写错误；这里只防御意外异常。
        }
        settleFailure(error);
      })();
    }

    function onError(error: NodeJS.ErrnoException): void {
      if (settled || closing) return;
      const hint =
        error.code === "ENOENT"
          ? "找不到 Core 可执行文件；开发态可设置 YAOMU_CORE_BIN，或在 app-server/ 下构建 Core。"
          : undefined;
      fail(new CoreBootstrapError("CORE_NOT_READY", `Core 启动失败：${error.message}`, hint));
    }

    function onEarlyExit(code: number | null, exitSignal: string | null): void {
      if (settled || closing) return;
      fail(
        new CoreBootstrapError(
          "CORE_NOT_READY",
          `Core 在就绪前退出（code=${String(code)}, signal=${String(exitSignal)}）`,
        ),
      );
    }

    function onAbort(): void {
      if (settled || closing) return;
      closing = true;
      // 终止触发的 exit 不再当作启动失败；确认自有 child 退出后才 reject AbortError。
      detachWaitListeners();
      abortCleanup = terminateProcessTree(child, CANCEL_TERMINATE_GRACE_MS).then(
        async () => {
          // 先等本代 ready 写全部 settle，再串行写 stopped，最后才允许 reject。
          // 不并行写同一文件，也不在 settle 后留下写尾巴。
          if (readyWriteStarted) {
            await waitForWrites();
            await publishBootstrapState("stopped");
          }
        },
        async (error: unknown) => {
          // 无法确认退出：仍等已开始的 ready 写结束，再交给上层按未确认处理。
          if (readyWriteStarted) await waitForWrites();
          throw error;
        },
      );
      void abortCleanup.then(
        () => {
          settleFailure(abortError("Core 启动已取消"));
        },
        (error: unknown) => {
          if (settled) return;
          settled = true;
          // 无法确认自有 child 已退出：保留父进程 exit 兜底，交由上层按未确认处理。
          reject(
            new CoreBootstrapError(
              "CORE_CLEANUP_FAILED",
              `取消 Core 启动后无法确认进程退出：${describe(error)}`,
            ),
          );
        },
      );
    }

    child.once("error", onError);
    child.once("exit", onEarlyExit);
    if (signal) {
      signal.addEventListener("abort", onAbort, { once: true });
      // 监听器注册前已取消（同步间隙）：显式调用一次，不依赖事件补发。
      if (signal.aborted) onAbort();
    }

    if (child.pid === undefined) {
      // spawn 失败由 error 事件收尾；不启动健康轮询。
      return;
    }

    waitForHealth({
      baseUrl,
      token,
      timeoutMs: options.healthTimeoutMs,
      intervalMs: options.healthIntervalMs,
      fetchImpl: options.fetchImpl,
      signal,
      isAlive: () => child.exitCode === null && child.signalCode === null,
    })
      .then(async () => {
        if (settled || closing) return;
        if (signal?.aborted) {
          onAbort();
          return;
        }
        const ready: CoreProcessInfo = {
          child,
          port,
          token,
          baseUrl,
          pid: child.pid ?? -1,
          startedAt,
        };
        readyWriteStarted = true;
        readyWrite = publishBootstrapState("ready");
        await readyWrite;
        // 写文件是异步的：取消/退出可能落在写入期间。取消路径会等本次写
        // settle 后串行写 stopped，因此这里只等待，不再并行纠正。
        if (closing || signal?.aborted) {
          if (!closing) onAbort();
          if (abortCleanup) await abortCleanup.catch(() => undefined);
          return;
        }
        if (settled) return;
        settled = true;
        detachWaitListeners();
        log(`[bootstrap] core ready on 127.0.0.1:${port} (pid ${ready.pid})`);
        resolve(ready);
      })
      .catch(async (error: unknown) => {
        if (settled || closing) return;
        closing = true;
        detachWaitListeners();
        // 健康失败同样先确认自有 child 退出，再 reject；不吞掉清理失败。
        try {
          await terminateProcessTree(child, CANCEL_TERMINATE_GRACE_MS);
          if (readyWriteStarted) {
            await waitForWrites();
            await publishBootstrapState("crashed");
          }
          settleFailure(error instanceof Error ? error : new Error(describe(error)));
        } catch (cleanupError: unknown) {
          if (readyWriteStarted) await waitForWrites().catch(() => undefined);
          if (settled) return;
          settled = true;
          // 无法确认自有 child 已退出：保留父进程 exit 兜底。
          reject(
            new CoreBootstrapError(
              "CORE_CLEANUP_FAILED",
              `Core 启动失败（${describe(error)}），且无法确认进程退出：${describe(cleanupError)}`,
            ),
          );
        }
      });
  });

  return info;
}

function describe(value: unknown): string {
  return value instanceof Error ? value.message : String(value);
}

export interface BootstrapFileSnapshot {
  schema_version: 1;
  app_id: "yaomustudio";
  core_port: number;
  core_pid: number;
  started_at: string;
  state: "ready" | "stopped" | "crashed";
}

/**
 * 写 bootstrap.json（仅 Bootstrap 维护）。
 * 只记录引导事实，不写实例令牌、URL 或业务数据；文件权限 0600。
 */
export async function writeBootstrapFile(
  dataDir: string,
  info: Pick<CoreProcessInfo, "port" | "pid" | "startedAt">,
  state: BootstrapFileSnapshot["state"] = "ready",
): Promise<string> {
  const target = path.join(dataDir, "bootstrap.json");
  const snapshot: BootstrapFileSnapshot = {
    schema_version: 1,
    app_id: "yaomustudio",
    core_port: info.port,
    core_pid: info.pid,
    started_at: info.startedAt,
    state,
  };
  await writeFile(target, `${JSON.stringify(snapshot, null, 2)}\n`, { mode: 0o600 });
  return target;
}

function signalProcessTree(child: ChildProcess, signal: "SIGTERM" | "SIGKILL"): void {
  const pid = child.pid;
  if (pid === undefined) return;
  if (process.platform === "win32") {
    if (signal === "SIGKILL") {
      // 只终止本应用拉起的进程树。
      spawn("taskkill", ["/PID", String(pid), "/T", "/F"], {
        windowsHide: true,
        stdio: "ignore",
      }).unref();
    } else {
      child.kill();
    }
    return;
  }
  try {
    process.kill(-pid, signal);
  } catch {
    try {
      child.kill(signal);
    } catch {
      // 进程已退出。
    }
  }
}

function waitForExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true);
  const pid = child.pid;
  return new Promise((resolve) => {
    let done = false;
    let timer: NodeJS.Timeout | undefined;
    const finish = (exited: boolean): void => {
      if (done) return;
      done = true;
      if (timer) clearTimeout(timer);
      child.removeListener("exit", onExit);
      resolve(exited);
    };
    const onExit = (): void => finish(true);
    child.once("exit", onExit);
    if (child.exitCode !== null || child.signalCode !== null) {
      finish(true);
      return;
    }
    timer = setTimeout(() => {
      if (pid !== undefined && !isProcessAlive(pid) && process.platform !== "win32") finish(true);
      else finish(false);
    }, timeoutMs);
  });
}

export function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/**
 * 请求 Core 自行退出。有可写 stdin 时先关闭管道（配合 YAOMU_SHUTDOWN_ON_STDIN）。
 * 返回 true 表示已经走了这条管道，调用方在 Windows 上不得立刻 TerminateProcess，
 * 否则 Core 来不及释放 Runner 租约，下次启动会被当成“未配置执行器”。
 */
function requestGracefulStop(child: ChildProcess): boolean {
  const stdin = child.stdin;
  if (!stdin || stdin.destroyed || !stdin.writable) return false;
  stdin.on("error", () => undefined);
  stdin.end();
  return true;
}

/**
 * 先优雅停止，超时后只终止本应用拉起的进程树；确认退出后才返回。
 * 未确认退出时抛错，由调用方决定是否释放资源。
 */
export async function terminateProcessTree(child: ChildProcess, graceMs = 5_000): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const askedStdin = requestGracefulStop(child);
  // 有 stdin 管道时先等 Core 自行退出：Windows 的 TerminateProcess 和 POSIX 的
  // SIGTERM 都可能抢在 EOF 处理前打断租约释放。没有管道（测试替身、旧启动方式）
  // 才立即发信号。
  if (!askedStdin) {
    signalProcessTree(child, "SIGTERM");
  }
  if (await waitForExit(child, graceMs)) return;
  signalProcessTree(child, "SIGKILL");
  if (await waitForExit(child, 5_000)) return;
  throw new Error(`无法确认进程 ${child.pid ?? "?"} 已退出`);
}

/** 渲染进程/日志可见的稳定实例标识（不含令牌）。 */
export function newInstanceId(): string {
  return randomUUID();
}

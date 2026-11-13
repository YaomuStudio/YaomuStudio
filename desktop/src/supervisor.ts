/**
 * Runtime Supervisor：Core 生命周期与崩溃恢复。
 *
 * - 区分正常退出与崩溃；崩溃时最多自动重启一次（maxRestarts 可配置）。
 * - 应用退出时先按序停 Core，确认进程树退出。
 * - 不把心跳失联当死亡：本模块只对进程 exit 事件作出反应，业务心跳由 Core/Runner 判定。
 *
 * 所有权模型（启动生命周期批）：
 * - 进行中的启动由单个 attempt 持有；并发 start、自动重启与显式 restart 共享它。
 * - 已 ready 时 start 返回同一个 CoreProcessInfo，不再拉起第二个 Core。
 * - stop 是唯一取消入口：立即断开 getConnection、abort launcher、等待其 cleanup，
 *   并确认本模块拥有的 child 退出；确认失败时报错，不伪 stopped，也不允许新代次启动。
 * - generation 让 stop 之后迟到的 ready/crashed 回调失效；迟到的 child 由 attempt
 *   自己的 stale 分支终止并确认。
 * - stop 的等待有界：launcher 永不 settle 时超时失败，但仍保留 attempt 拥有权；
 *   不清理引用、不丢弃迟到 child，后续 stop 可继续确认，成功后才允许重新启动。
 */
import type { ChildProcess } from "node:child_process";
import {
  CoreBootstrapError,
  abortError,
  isAbortError,
  terminateProcessTree,
  type CoreConnection,
  type CoreProcessInfo,
} from "./bootstrap";

export type CoreRuntimeState =
  | "stopped"
  | "starting"
  | "ready"
  | "restarting"
  | "crashed"
  | "stopping";

export interface SupervisorOptions {
  launcher: (signal: AbortSignal) => Promise<CoreProcessInfo>;
  /** 崩溃后的自动重启次数，默认 1。 */
  maxRestarts?: number;
  /** 停止 Core 的优雅等待时间。 */
  stopTimeoutMs?: number;
  /**
   * stop 等待进行中启动完成 cleanup 的上限；默认 stopTimeoutMs + 5s
   * （覆盖 terminateProcessTree 的 SIGKILL 等待阶段），始终有界。
   */
  settleTimeoutMs?: number;
  log?: (line: string) => void;
  onState?: (state: CoreRuntimeState, error?: Error) => void;
}

interface LaunchAttempt {
  id: number;
  controller: AbortController;
  promise: Promise<CoreProcessInfo>;
  settled: boolean;
  /**
   * pending：仍在进行，资源归属未定；
   * confirmed：启动成功或 launcher 在失败前已确认自有 child 退出；
   * unconfirmed：报告了 CORE_CLEANUP_FAILED，没有退出证据。
   */
  cleanup: "pending" | "confirmed" | "unconfirmed";
  cleanupError: Error | null;
}

type SettlementOutcome =
  | { kind: "settled" }
  | { kind: "timeout" }
  | { kind: "error"; error: unknown };

export class RuntimeSupervisor {
  private readonly options: SupervisorOptions;
  private readonly log: (line: string) => void;
  private state: CoreRuntimeState = "stopped";
  private info: CoreProcessInfo | null = null;
  /** 当前进行中的启动；settle 前一直保留，超时后也不清空。 */
  private attempt: LaunchAttempt | null = null;
  /** 并发 stop 共享同一次等待。 */
  private stopPromise: Promise<void> | null = null;
  /** 本模块拉起、尚未确认退出的 child；确认后立即清空。 */
  private ownedChild: ChildProcess | null = null;
  /** stop 递增以作废进行中的启动回调。 */
  private generation = 0;
  /** 每次 stop 请求计数（含共享等待），用于让外部 stop 优先于更早的 restart。 */
  private stopRequests = 0;
  /** stop 未能确认清理完成；在后续 stop 以证据清除前拒绝新一代启动。 */
  private stopIncomplete = false;
  /**
   * 任一次清理失败且没有退出证据（launcher 报 CORE_CLEANUP_FAILED）。
   * 该事实永久保留到进程重启：后续 stop 不得因为“当前无资源”而清除隔离。
   */
  private cleanupUnconfirmed: Error | null = null;
  /** cleanupUnconfirmed 是否绑定到仍持有的 ownedChild（后续确认退出后可恢复）。 */
  private cleanupEvidencePossible = false;
  private stopping = false;
  private restartsUsed = 0;

  constructor(options: SupervisorOptions) {
    this.options = options;
    this.log = options.log ?? (() => undefined);
  }

  getState(): CoreRuntimeState {
    return this.state;
  }

  getInfo(): CoreProcessInfo | null {
    return this.info;
  }

  getConnection(): CoreConnection | null {
    if (!this.info) return null;
    return { baseUrl: this.info.baseUrl, token: this.info.token };
  }

  async start(): Promise<CoreProcessInfo> {
    if (this.cleanupUnconfirmed) {
      throw new Error(
        `Core 停止尚未确认完成，拒绝重新启动：${this.cleanupUnconfirmed.message}`,
      );
    }
    if (this.stopIncomplete) {
      throw new Error("Core 停止尚未确认完成，拒绝重新启动");
    }
    if (this.stopping) {
      throw abortError("Core 正在停止，拒绝启动");
    }
    if (this.state === "ready" && this.info) {
      if (isChildRunning(this.info.child)) return this.info;
      // 进程已退出但 exit 回调尚未处理：显式 start 接管，不再复用旧 info。
      this.info = null;
      this.ownedChild = null;
    }
    if (this.attempt) return this.attempt.promise;
    return this.launch("starting");
  }

  async restart(): Promise<CoreProcessInfo> {
    const requestsBefore = this.stopRequests;
    await this.stop();
    // stop 期间若又有外部 stop 请求，旧的 restart 不得再开启新 Core。
    if (this.stopRequests !== requestsBefore + 1) {
      throw abortError("Core 重启已被后续停止请求取代");
    }
    this.restartsUsed = 0;
    return this.start();
  }

  async stop(): Promise<void> {
    this.stopRequests += 1;
    if (this.stopPromise) return this.stopPromise;
    // 先登记共享 Promise，再同步进入 performStop：onState 回调里的重入 stop
    // 也会加入同一次等待，而不是再开第二个停止流程。
    let resolveStop!: () => void;
    let rejectStop!: (error: unknown) => void;
    const promise = new Promise<void>((resolve, reject) => {
      resolveStop = resolve;
      rejectStop = reject;
    });
    this.stopPromise = promise;
    void this.performStop().then(resolveStop, rejectStop);
    try {
      await promise;
    } finally {
      if (this.stopPromise === promise) this.stopPromise = null;
    }
  }

  private async performStop(): Promise<void> {
    const stopTimeoutMs = this.options.stopTimeoutMs ?? 5_000;
    this.stopping = true;
    // 先断开连接再通知状态：onState 回调里看到的已经是不可用连接。
    this.generation += 1;
    const attempt = this.attempt;
    const info = this.info;
    this.info = null;
    this.setState("stopping");
    attempt?.controller.abort(abortError("Core 停止"));

    let failure: Error | null = null;

    if (attempt) {
      const settleTimeoutMs = this.settleTimeoutMs();
      const outcome = await waitForSettlement(attempt.promise, settleTimeoutMs);
      if (outcome.kind === "timeout") {
        failure = stopTimeoutError(settleTimeoutMs);
      } else if (outcome.kind === "error") {
        if (attempt.cleanup === "unconfirmed") {
          // runLaunch 已记录持久隔离事实；这里只保留日志。
          this.log(`[supervisor] 启动清理未确认：${describe(outcome.error)}`);
        } else if (!isAbortError(outcome.error)) {
          // 启动自身失败（如 CORE_NOT_READY）：Bootstrap 只在确认自有 child
          // 清理后才 reject，因此不是 stop 的资源失败。
          this.log(`[supervisor] 启动在停止期间失败：${describe(outcome.error)}`);
        }
      }
      // 不清空 this.attempt：runLaunch.finally 只在真正 settle 后清除；
      // 超时后仍保留拥有权，避免丢失迟返回 child。
    }

    const child = this.ownedChild ?? info?.child ?? null;
    if (child) {
      try {
        await terminateProcessTree(child, stopTimeoutMs);
        if (this.ownedChild === child) this.ownedChild = null;
      } catch (error) {
        failure = failure ?? normalizeError(error);
      }
    }

    // 未经证实的清理失败不能被“当前无资源”清除：
    // - 无 ownedChild 的失败没有退出证据，永久隔离到进程重启；
    // - 绑定 ownedChild 的失败，只有该 child 已确认退出才算恢复。
    if (this.cleanupUnconfirmed) {
      if (this.cleanupEvidencePossible && this.ownedChild === null) {
        this.cleanupUnconfirmed = null;
        this.cleanupEvidencePossible = false;
      } else {
        this.stopIncomplete = true;
        this.stopping = false;
        this.log(`[supervisor] 停止 Core 失败：${this.cleanupUnconfirmed.message}`);
        this.setState("crashed", this.cleanupUnconfirmed);
        throw this.cleanupUnconfirmed;
      }
    }

    if (failure) {
      this.stopIncomplete = true;
      this.stopping = false;
      this.log(`[supervisor] 停止 Core 失败：${failure.message}`);
      this.setState("crashed", failure);
      throw failure;
    }

    this.stopIncomplete = false;
    this.stopping = false;
    this.setState("stopped");
  }

  private settleTimeoutMs(): number {
    return this.options.settleTimeoutMs ?? (this.options.stopTimeoutMs ?? 5_000) + 5_000;
  }

  private launch(
    state: "starting" | "restarting",
    stateError?: Error,
  ): Promise<CoreProcessInfo> {
    if (this.cleanupUnconfirmed) {
      return Promise.reject(
        new Error(
          `Core 停止尚未确认完成，拒绝重新启动：${this.cleanupUnconfirmed.message}`,
        ),
      );
    }
    if (this.stopIncomplete) {
      return Promise.reject(new Error("Core 停止尚未确认完成，拒绝重新启动"));
    }
    if (this.stopping) {
      return Promise.reject(abortError("Core 正在停止，拒绝启动"));
    }
    // 单飞：进行中的启动已发布在 this.attempt 上，重复调用共享它。
    if (this.attempt) return this.attempt.promise;

    const controller = new AbortController();
    let resolveAttempt!: (info: CoreProcessInfo) => void;
    let rejectAttempt!: (error: unknown) => void;
    const promise = new Promise<CoreProcessInfo>((resolve, reject) => {
      resolveAttempt = resolve;
      rejectAttempt = reject;
    });
    const attempt: LaunchAttempt = {
      id: (this.generation += 1),
      controller,
      promise,
      settled: false,
      cleanup: "pending",
      cleanupError: null,
    };
    this.attempt = attempt;
    // 先发布 attempt.promise，再同步进入 runLaunch 的 onState：
    // onState 中的重入 start/stop 看到的是同一个已发布 Promise，不是 undefined。
    void promise.catch(() => undefined);
    this.runLaunch(attempt, state, stateError).then(resolveAttempt, rejectAttempt);
    return promise;
  }

  private async runLaunch(
    attempt: LaunchAttempt,
    state: "starting" | "restarting",
    stateError?: Error,
  ): Promise<CoreProcessInfo> {
    this.setState(state, stateError);
    try {
      // onState 里的重入 stop 可能已取消本次启动：不要再调用 launcher。
      if (attempt.controller.signal.aborted) {
        throw abortError("Core 启动已取消");
      }
      const info = await this.options.launcher(attempt.controller.signal);
      if (this.attempt !== attempt || attempt.id !== this.generation) {
        // stop 已取得所有权：这次迟到返回的 child 仍由本模块终止并确认。
        this.ownedChild = info.child;
        try {
          await terminateProcessTree(info.child, this.options.stopTimeoutMs ?? 5_000);
          if (this.ownedChild === info.child) this.ownedChild = null;
          attempt.cleanup = "confirmed";
        } catch (error) {
          const failure = new CoreBootstrapError(
            "CORE_CLEANUP_FAILED",
            `放弃迟到 Core 时无法确认进程退出：${describe(error)}`,
          );
          attempt.cleanup = "unconfirmed";
          attempt.cleanupError = failure;
          // 仍持有该 child：后续成功终止可提供退出证据；否则保持隔离。
          this.latchCleanupFailure(failure, true);
          throw failure;
        }
        throw abortError("Core 启动已被停止请求取代");
      }
      attempt.cleanup = "confirmed";
      this.ownedChild = info.child;
      this.info = info;
      this.attachExit(info);
      this.setState("ready");
      return info;
    } catch (caught) {
      const normalized = normalizeError(caught);
      const current = this.attempt === attempt && attempt.id === this.generation;
      if (attempt.cleanup === "pending") {
        if (isCleanupFailure(normalized)) {
          attempt.cleanup = "unconfirmed";
          attempt.cleanupError = normalized;
          // launcher 未返回 CoreInfo，没有 child 句柄；该隔离事实持久保留。
          this.latchCleanupFailure(normalized, false);
        } else {
          // launcher 契约：只有 CORE_CLEANUP_FAILED 表示无法确认退出；
          // 其他 rejection（含 AbortError/CORE_NOT_READY）视为已确认无自有 child。
          attempt.cleanup = "confirmed";
        }
      }
      if (this.attempt === attempt) this.attempt = null;
      if (!isAbortError(normalized) && current && !this.stopping) {
        this.setState("crashed", normalized);
      }
      throw normalized;
    } finally {
      attempt.settled = true;
      if (this.attempt === attempt) this.attempt = null;
    }
  }

  /**
   * 记录一次“清理无法确认”的隔离事实。
   * - hasOwnedChild=true：失败绑定在当前 ownedChild；只有该 child 确认退出才算恢复。
   * - 否则没有退出证据，永久隔离到进程重启；空 stop 不得清除。
   */
  private latchCleanupFailure(error: Error, hasOwnedChild: boolean): void {
    if (this.cleanupUnconfirmed) return;
    this.cleanupUnconfirmed = error;
    this.cleanupEvidencePossible = hasOwnedChild;
  }

  private attachExit(info: CoreProcessInfo): void {
    info.child.once("exit", (code, signal) => {
      this.handleExit(info, code, signal);
    });
    if (!isChildRunning(info.child)) {
      // 监听器注册前进程已退出：下一轮事件循环补处理。
      queueMicrotask(() => this.handleExit(info, info.child.exitCode, info.child.signalCode));
    }
  }

  private handleExit(info: CoreProcessInfo, code: number | null, signal: string | null): void {
    if (this.ownedChild === info.child) this.ownedChild = null;
    if (this.info !== info) return;
    this.info = null;
    if (this.stopping) return;
    const reason = new Error(`Core 意外退出（code=${String(code)}, signal=${String(signal)}）`) as Error & {
      code?: string;
    };
    // 稳定错误码供上层区分“运行中退出”与启动失败；不改变 Supervisor 状态机。
    reason.code = "CORE_EXITED";
    this.log(`[supervisor] ${reason.message}`);
    const maxRestarts = this.options.maxRestarts ?? 1;
    if (this.restartsUsed < maxRestarts) {
      this.restartsUsed += 1;
      // 不先 setState：launch 会先发布 attempt.promise，再由 runLaunch 同步
      // 通知 restarting；onState 中的重入 start/stop 因此共享同一次启动。
      const promise = this.launch("restarting", reason);
      void promise.catch(() => undefined);
      return;
    }
    this.setState("crashed", reason);
  }

  private setState(state: CoreRuntimeState, error?: Error): void {
    if (this.state === state && !error) return;
    this.state = state;
    this.options.onState?.(state, error);
  }
}

function describe(value: unknown): string {
  return value instanceof Error ? value.message : String(value);
}

function normalizeError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}

function isChildRunning(child: ChildProcess): boolean {
  return child.exitCode === null && child.signalCode === null;
}

function isCleanupFailure(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { code?: unknown }).code === "CORE_CLEANUP_FAILED"
  );
}

function stopTimeoutError(timeoutMs: number): Error {
  const error = new Error(`Core 启动取消后 ${timeoutMs}ms 内未完成清理，停止未确认`);
  error.name = "CoreStopTimeoutError";
  return error;
}

/** 有界等待 attempt 完成；超时也不解除调用方的拥有权。 */
function waitForSettlement(
  promise: Promise<unknown>,
  timeoutMs: number,
): Promise<SettlementOutcome> {
  return new Promise((resolve) => {
    let done = false;
    let timer: NodeJS.Timeout | undefined;
    const finish = (outcome: SettlementOutcome): void => {
      if (done) return;
      done = true;
      if (timer) clearTimeout(timer);
      resolve(outcome);
    };
    timer = setTimeout(() => finish({ kind: "timeout" }), timeoutMs);
    promise.then(
      () => finish({ kind: "settled" }),
      (error) => finish({ kind: "error", error }),
    );
  });
}

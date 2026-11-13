/**
 * 主进程初始化闸门（第二批）。
 *
 * 背景：窗口创建前的 WSL/工具探测会长时间等待外部进程。退出时只依赖
 * `supervisor.stop()` 不够——探测阶段还没有 Supervisor，晚到的探测结果也可能
 * 继续拉起 Core 或创建窗口。这里提供唯一的初始化任务与取消信号：
 *
 * - `run` 只执行一次，重复调用共享同一 promise，不产生第二份探测/Core 启动；
 * - `cancel` 只发出取消，具体资源的有界清理由任务（探测 transport / Supervisor）
 *   自己完成，不把 abort 本身当成已停止；
 * - `settle` 等待进行中的初始化收敛，但自身有界；超时抛明确错误并保留 task，
 *   不清理、不伪称已停止，供调用方报告未确认并继续既有退出兜底。
 *
 * 本模块不依赖 electron，可在普通 Node 测试中验证竞态。
 */

/** 等待初始化收敛的上限；覆盖探测取消确认与 Supervisor 自身的有界停止。 */
const DEFAULT_SETTLE_TIMEOUT_MS = 15_000;

export class StartupSettleTimeoutError extends Error {
  readonly timeoutMs: number;

  constructor(timeoutMs: number) {
    super(`初始化未在 ${timeoutMs}ms 内收敛`);
    this.name = "StartupSettleTimeoutError";
    this.timeoutMs = timeoutMs;
  }
}

export type ShutdownConvergence = {
  startup: { kind: "settled" } | { kind: "timeout"; error: StartupSettleTimeoutError };
  supervisor: { kind: "stopped" } | { kind: "failed"; error: unknown };
};

export class StartupLifecycle {
  private readonly controller = new AbortController();
  private task: Promise<void> | null = null;
  private taskSettled = false;

  get signal(): AbortSignal {
    return this.controller.signal;
  }

  get cancelled(): boolean {
    return this.controller.signal.aborted;
  }

  get started(): boolean {
    return this.task !== null;
  }

  /** 初始化任务是否仍未收敛；超时后保持 true，不清理 task。 */
  get pending(): boolean {
    return this.task !== null && !this.taskSettled;
  }

  /**
   * 执行一次初始化任务；重复调用返回同一个 promise。
   * 任务必须自己通过 signal 观察取消，并在 settle 前完成有界清理。
   */
  run(task: (signal: AbortSignal) => Promise<void>): Promise<void> {
    if (!this.task) {
      this.task = task(this.controller.signal);
      // 始终挂接结算观察，避免任务拒绝在无人等待时变成未处理拒绝。
      void this.task.then(
        () => {
          this.taskSettled = true;
        },
        () => {
          this.taskSettled = true;
        },
      );
    }
    return this.task;
  }

  /** 取消初始化；幂等，可发生在 run 之前或之后。 */
  cancel(): void {
    this.controller.abort();
  }

  /**
   * 等待进行中的初始化收敛；未开始或已完成时立即返回。
   * 超过 timeoutMs 时抛 StartupSettleTimeoutError，并保留未收敛的 task。
   */
  async settle(timeoutMs = DEFAULT_SETTLE_TIMEOUT_MS): Promise<void> {
    if (!this.task) return;
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
      await this.task.catch(() => undefined);
      return;
    }
    const outcome = await waitForSettlement(this.task, timeoutMs);
    if (outcome === "timeout") throw new StartupSettleTimeoutError(timeoutMs);
  }
}

function waitForSettlement(
  task: Promise<void>,
  timeoutMs: number,
): Promise<"settled" | "timeout"> {
  return new Promise((resolve) => {
    let done = false;
    let timer: NodeJS.Timeout | undefined;
    const finish = (value: "settled" | "timeout"): void => {
      if (done) return;
      done = true;
      if (timer) clearTimeout(timer);
      resolve(value);
    };
    timer = setTimeout(() => finish("timeout"), timeoutMs);
    // 成功与失败都算 task 已收敛；错误由 run/调用方自行记录。
    void task.then(
      () => finish("settled"),
      () => finish("settled"),
    );
  });
}

/**
 * 汇合“初始化收敛”和“Supervisor 停止”两路等待：
 * - stop promise 在调用瞬间挂接拒绝处理，避免等待初始化期间被判未处理拒绝；
 * - 初始化等待有界；超时只报告，不清理 task，也不改写 Supervisor 的结果；
 * - 返回两路结果，由 main 记录未确认事实后继续既有退出兜底。
 */
export async function convergeShutdown(options: {
  startup: StartupLifecycle;
  stopSupervisor: () => Promise<void>;
  settleTimeoutMs?: number;
}): Promise<ShutdownConvergence> {
  let stopPromise: Promise<void>;
  try {
    stopPromise = options.stopSupervisor();
  } catch (error) {
    stopPromise = Promise.reject(error);
  }
  // 立即接收拒绝：即使初始化一路超时，也不会出现未处理拒绝。
  const supervisorOutcome = stopPromise.then(
    (): ShutdownConvergence["supervisor"] => ({ kind: "stopped" }),
    (error: unknown): ShutdownConvergence["supervisor"] => ({ kind: "failed", error }),
  );

  let startupOutcome: ShutdownConvergence["startup"];
  try {
    await options.startup.settle(options.settleTimeoutMs);
    startupOutcome = { kind: "settled" };
  } catch (error) {
    if (!(error instanceof StartupSettleTimeoutError)) throw error;
    startupOutcome = { kind: "timeout", error };
  }

  return { startup: startupOutcome, supervisor: await supervisorOutcome };
}

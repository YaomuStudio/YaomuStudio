/**
 * 应用更新检查调度器。
 *
 * 不依赖 Electron，便于在普通 Node 测试中验证间隔、聚焦节流、并发去重与
 * 安装阶段跳过。Main 注入实际的 updater 调用和错误处理；本模块不持有业务
 * 状态，也不直接发网络请求。
 */

export type UpdateCheckSource = "auto" | "focus" | "manual";

export interface UpdateSchedulerOptions {
  /** 周期检查间隔；默认 4 小时。 */
  intervalMs?: number;
  /** 窗口聚焦补查的最小间隔；默认 1 小时。 */
  focusMinIntervalMs?: number;
  now?: () => number;
  setInterval?: (callback: () => void, delayMs: number) => unknown;
  clearInterval?: (handle: unknown) => void;
  /** 自动检查是否允许：打包态、开关开启、未退出等。manual 不受此限制。 */
  isEnabled(): boolean;
  /** 临时阻断（已有待安装版本/正在下载）；只阻断自动/聚焦检查，manual 仍可用。 */
  isCheckBlocked?(): boolean;
  /** 安装准备/停止/安装中：跳过所有检查，避免改变待装版本。 */
  isInstallActive(): boolean;
  check(source: UpdateCheckSource): Promise<unknown>;
  /** 检查失败；source 是并发期间被提升后的有效来源。 */
  onError?(source: UpdateCheckSource, error: unknown): void;
  log?(line: string): void;
}

const DEFAULT_INTERVAL_MS = 4 * 60 * 60 * 1000;
const DEFAULT_FOCUS_MIN_INTERVAL_MS = 60 * 60 * 1000;

export class UpdateScheduler {
  private readonly options: UpdateSchedulerOptions;
  private readonly intervalMs: number;
  private readonly focusMinIntervalMs: number;
  private readonly now: () => number;
  private readonly schedule: (callback: () => void, delayMs: number) => unknown;
  private readonly cancelSchedule: (handle: unknown) => void;
  private timer: unknown = null;
  private inFlight: Promise<void> | null = null;
  private activeSource: UpdateCheckSource | null = null;
  private lastCheckStart = 0;

  constructor(options: UpdateSchedulerOptions) {
    this.options = options;
    this.intervalMs = options.intervalMs ?? DEFAULT_INTERVAL_MS;
    this.focusMinIntervalMs = options.focusMinIntervalMs ?? DEFAULT_FOCUS_MIN_INTERVAL_MS;
    this.now = options.now ?? Date.now;
    this.schedule =
      options.setInterval ?? ((callback, delayMs) => globalThis.setInterval(callback, delayMs));
    this.cancelSchedule =
      options.clearInterval ?? ((handle) => globalThis.clearInterval(handle as NodeJS.Timeout));
  }

  get lastCheckAt(): number {
    return this.lastCheckStart;
  }

  /** 启动周期检查；重复调用不会产生第二个定时器。 */
  start(): void {
    this.stop();
    if (!this.options.isEnabled()) return;
    this.timer = this.schedule(() => {
      void this.checkNow("auto");
    }, this.intervalMs);
  }

  /** 停止周期检查；不取消已发出的检查。 */
  stop(): void {
    if (this.timer === null) return;
    this.cancelSchedule(this.timer);
    this.timer = null;
  }

  /** 开关变化：开启时重建定时器，关闭时只停止后续检查。 */
  setEnabled(enabled: boolean): void {
    if (enabled) this.start();
    else this.stop();
  }

  /** 窗口聚焦：距上次检查不足最小间隔或临时被阻断则跳过。 */
  onFocus(): void {
    if (!this.options.isEnabled()) return;
    if (this.options.isCheckBlocked?.()) return;
    const now = this.now();
    if (this.lastCheckStart !== 0 && now - this.lastCheckStart < this.focusMinIntervalMs) {
      return;
    }
    void this.checkNow("focus");
  }

  /**
   * 立即检查。
   * - 自动/聚焦检查受 isEnabled、isCheckBlocked 与安装阶段约束；
   * - manual 始终允许（仍受安装阶段约束，UI 也会禁用入口）；
   * - 并发时复用同一 Promise；manual 请求会把有效来源提升为 manual，
   *   使迟到的错误按用户主动操作处理。
   */
  checkNow(source: UpdateCheckSource): Promise<void> {
    if (source !== "manual" && !this.options.isEnabled()) return Promise.resolve();
    if (this.options.isInstallActive()) return Promise.resolve();
    if (source !== "manual" && this.options.isCheckBlocked?.()) return Promise.resolve();
    if (this.inFlight) {
      if (source === "manual") this.activeSource = "manual";
      return this.inFlight;
    }

    this.activeSource = source;
    this.lastCheckStart = this.now();
    let run: Promise<void>;
    run = Promise.resolve()
      .then(() => this.options.check(source))
      .then(() => undefined)
      .catch((error: unknown) => {
        const effectiveSource = this.activeSource ?? source;
        this.options.log?.(`更新检查失败（${effectiveSource}）`);
        this.options.onError?.(effectiveSource, error);
      })
      .finally(() => {
        if (this.inFlight === run) {
          this.inFlight = null;
          this.activeSource = null;
        }
      });
    this.inFlight = run;
    return run;
  }
}

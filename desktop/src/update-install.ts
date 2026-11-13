/**
 * 应用更新安装守卫：Main 唯一的显式同意与安装顺序权威。
 *
 * - 只消费 Core 的同实例 check/prepare 证明；不持有第二业务队列，不查看任务列表。
 * - 等待阶段只读轮询（默认 5s 传输预算、2s 间隔），可取消且不改 Core 任务；
 *   idle 后同步进入不可取消的 preparing，再发送 prepare；prepare 永远重新验证。
 * - prepared 必须匹配捕获的 operation / 版本 / 认证身份 / generation / connection；
 *   随后由同一 runtime owner 停止 Core，确认停止后才至多调用一次安装器。
 * - 普通退出只需取消同意：`cancelConsent()` 之后任何迟到回包都不会触发安装。
 * - 快照严格对齐 contracts/app-update-installation.schema.json；不含凭据、身份字符串、
 *   URL 或原始异常。本模块不依赖 electron，可在普通 Node 测试中验证顺序与竞赛。
 */
import { randomUUID } from "node:crypto";
import type { CoreConnection, FetchLike } from "./bootstrap";

export const UPDATE_INSTALL_SCHEMA_VERSION = 1;

export type UpdateInstallPhase =
  | "idle"
  | "waiting"
  | "preparing"
  | "stopping"
  | "installing"
  | "blocked";

export type UpdateInstallReason =
  | "active_work"
  | "request_in_flight"
  | "executor_starting"
  | "runtime_unavailable"
  | "runtime_changed"
  | "executor_unconfirmed"
  | "prepare_failed"
  | "stop_unconfirmed"
  | "restart_required"
  | "update_unavailable"
  | "install_failed";

export interface UpdateInstallSnapshot {
  schemaVersion: 1;
  revision: number;
  phase: UpdateInstallPhase;
  reason: UpdateInstallReason | null;
  version: string | null;
  canCancel: boolean;
}

/** Main 内捕获的认证身份；不得进入快照或 Renderer。 */
export interface UpdateRuntimeIdentity {
  workspaceId: string;
  instanceId: string;
  serverEpoch: number;
  generation: number;
}

/** Main 内捕获的运行时访问；连接含实例令牌，只在内存用于 prepare 请求。 */
export interface UpdateRuntimeAccess {
  identity: UpdateRuntimeIdentity;
  connection: CoreConnection;
}

export type CorePreparationState = "idle" | "blocked" | "prepared";

export type CorePreparationReason =
  | "active_work"
  | "request_in_flight"
  | "executor_starting"
  | "executor_unconfirmed"
  | "preparing"
  | "restart_required";

export interface UpdatePreparationRequest {
  mode: "check" | "prepare";
  operation_id: string;
  workspace_id: string;
  instance_id: string;
  server_epoch: number;
}

/**
 * 一次 Core 准备请求的有限结果。
 * - conflict：409，身份/epoch 不符合该 Core 的当前实例；
 * - identity：401/403，捕获的私有连接已失效（Core 重启/令牌变化）；
 * - unavailable：503 安全信封（存储/查询失败，不得当作零活动）；
 * - invalid：400 或响应形状不符合契约；
 * - transport：网络、超时或读取失败。
 */
export type UpdatePreparationOutcome =
  | { kind: "result"; state: CorePreparationState; reason: CorePreparationReason | null }
  | { kind: "conflict" }
  | { kind: "identity" }
  | { kind: "unavailable" }
  | { kind: "invalid" }
  | { kind: "transport" };

export type UpdatePreparationClient = (
  connection: CoreConnection,
  request: UpdatePreparationRequest,
  signal: AbortSignal,
) => Promise<UpdatePreparationOutcome>;

export interface UpdateInstallControllerOptions {
  /** 读取当前已握手 Core 的 Main-only 身份与连接；不可用时为 null。 */
  getRuntimeAccess(): UpdateRuntimeAccess | null;
  /** 同实例 check/prepare 客户端；缺省由 createUpdatePreparationClient 提供。 */
  prepare: UpdatePreparationClient;
  /** 停止唯一 runtime owner，并在确认自有 Core 退出后 resolve。 */
  stopRuntime(): Promise<void>;
  /** 当前已下载待安装版本；无下载版本返回 null。 */
  getDownloadedVersion(): string | null;
  /** 真实 updater 注入点；生产中固定为 quitAndInstall(false, true)。 */
  installer: { quitAndInstall(): void };
  log?: (line: string) => void;
  checkIntervalMs?: number;
  checkTimeoutMs?: number;
  prepareTimeoutMs?: number;
  operationId?: () => string;
  /** 定时器注入点，便于测试用受控调度器；缺省 globalThis。 */
  schedule?: (callback: () => void, delayMs: number) => unknown;
  cancelSchedule?: (handle: unknown) => void;
}

const MAX_ID_LENGTH = 200;
const MAX_VERSION_LENGTH = 200;
const MAX_REVISION = 9_007_199_254_740_991;

/** Core 响应里仍可安全等待的有限原因；其余一律 fail closed。 */
const WAITING_REASONS: ReadonlySet<CorePreparationReason> = new Set([
  "active_work",
  "request_in_flight",
  "executor_starting",
]);

const BLOCKED_REASONS: ReadonlySet<UpdateInstallReason> = new Set([
  "runtime_unavailable",
  "runtime_changed",
  "executor_unconfirmed",
  "prepare_failed",
  "stop_unconfirmed",
  "restart_required",
  "update_unavailable",
  "install_failed",
]);

const ALL_REASONS: ReadonlySet<string> = new Set<string>([
  ...WAITING_REASONS,
  ...BLOCKED_REASONS,
]);

/**
 * 失败后只有“同意已明确丢弃且未进入不可逆清理”的 blocked 才允许重新发起显式同意。
 * runtime 不可用/身份变化可等恢复后由用户重新确认；prepare/stop 之后的失败保持封闸。
 */
const RETRYABLE_BLOCKED: ReadonlySet<UpdateInstallReason> = new Set([
  "update_unavailable",
  "runtime_unavailable",
  "runtime_changed",
]);

const ACTIVE_PHASES: ReadonlySet<UpdateInstallPhase> = new Set([
  "waiting",
  "preparing",
  "stopping",
  "installing",
]);

const SNAPSHOT_KEYS = [
  "schemaVersion",
  "revision",
  "phase",
  "reason",
  "version",
  "canCancel",
] as const;

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const actual = Object.keys(value);
  return actual.length === keys.length && keys.every((key) => key in value);
}

function boundedId(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= MAX_ID_LENGTH;
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

/** 校验 Main 快照；Bridge 在广播前也复用同一口径。 */
export function isUpdateInstallSnapshot(value: unknown): value is UpdateInstallSnapshot {
  const body = record(value);
  if (!body || !exactKeys(body, SNAPSHOT_KEYS)) return false;
  if (body.schemaVersion !== UPDATE_INSTALL_SCHEMA_VERSION) return false;
  if (
    typeof body.revision !== "number" ||
    !Number.isSafeInteger(body.revision) ||
    body.revision < 1 ||
    body.revision > MAX_REVISION
  ) {
    return false;
  }
  const phase = body.phase;
  if (
    phase !== "idle" &&
    phase !== "waiting" &&
    phase !== "preparing" &&
    phase !== "stopping" &&
    phase !== "installing" &&
    phase !== "blocked"
  ) {
    return false;
  }
  const reason = body.reason;
  if (reason !== null && (typeof reason !== "string" || !ALL_REASONS.has(reason))) {
    return false;
  }
  const version = body.version;
  if (
    version !== null &&
    (typeof version !== "string" || version.length === 0 || version.length > MAX_VERSION_LENGTH)
  ) {
    return false;
  }
  if (typeof body.canCancel !== "boolean") return false;

  if (phase === "waiting") {
    if (body.canCancel !== true) return false;
    if (typeof version !== "string") return false;
    if (reason !== null && !WAITING_REASONS.has(reason as CorePreparationReason)) return false;
    return true;
  }
  if (body.canCancel !== false) return false;
  if (phase === "idle") {
    return version === null && reason === null;
  }
  if (phase === "preparing" || phase === "stopping" || phase === "installing") {
    return typeof version === "string" && reason === null;
  }
  // blocked：必须带有限失败原因；版本已知时保留展示字符串，未知时为 null。
  return reason !== null && BLOCKED_REASONS.has(reason as UpdateInstallReason);
}

function normalizeVersion(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 && value.length <= MAX_VERSION_LENGTH
    ? value
    : null;
}

function sameConnection(left: CoreConnection, right: CoreConnection): boolean {
  return left.baseUrl === right.baseUrl && left.token === right.token;
}

function sameRuntime(access: UpdateRuntimeAccess, attempt: Attempt): boolean {
  const identity = access.identity;
  return (
    identity.workspaceId === attempt.identity.workspaceId &&
    identity.instanceId === attempt.identity.instanceId &&
    identity.serverEpoch === attempt.identity.serverEpoch &&
    identity.generation === attempt.identity.generation &&
    sameConnection(access.connection, attempt.connection)
  );
}

interface Attempt {
  seq: number;
  operationId: string;
  version: string;
  identity: UpdateRuntimeIdentity;
  connection: CoreConnection;
}

function timeoutSignal(timeoutMs: number, external?: AbortSignal): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  return external ? AbortSignal.any([external, timeout]) : timeout;
}

/**
 * 显式同意、等待、准备、停止与安装的唯一顺序权威。
 * 所有副作用入口先校验尝试仍然有效（attempt token），迟到回包不作用于新意图。
 */
export class UpdateInstallController {
  private readonly options: UpdateInstallControllerOptions;
  private readonly log: (line: string) => void;
  private readonly schedule: (callback: () => void, delayMs: number) => unknown;
  private readonly cancelSchedule: (handle: unknown) => void;
  private readonly listeners = new Set<(snapshot: UpdateInstallSnapshot) => void>();
  private snapshot: UpdateInstallSnapshot = {
    schemaVersion: UPDATE_INSTALL_SCHEMA_VERSION,
    revision: 1,
    phase: "idle",
    reason: null,
    version: null,
    canCancel: false,
  };
  private attempt: Attempt | null = null;
  private attemptSeq = 0;
  private timer: unknown = null;
  private checkAbort: AbortController | null = null;
  private quitting = false;
  private installerInvoked = false;

  constructor(options: UpdateInstallControllerOptions) {
    this.options = options;
    this.log = options.log ?? (() => undefined);
    this.schedule =
      options.schedule ?? ((callback, delayMs) => globalThis.setTimeout(callback, delayMs));
    this.cancelSchedule =
      options.cancelSchedule ?? ((handle) => globalThis.clearTimeout(handle as NodeJS.Timeout));
  }

  get checkIntervalMs(): number {
    return this.options.checkIntervalMs ?? 2_000;
  }

  private get checkTimeoutMs(): number {
    return this.options.checkTimeoutMs ?? 5_000;
  }

  private get prepareTimeoutMs(): number {
    return this.options.prepareTimeoutMs ?? 20_000;
  }

  getSnapshot(): UpdateInstallSnapshot {
    return { ...this.snapshot };
  }

  subscribe(listener: (snapshot: UpdateInstallSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /**
   * 开始或加入当前显式同意。等待/准备/停止/安装中的重复调用只返回当前快照，
   * 不会启动第二个 operation 或第二次安装。
   */
  install(): UpdateInstallSnapshot {
    if (this.quitting) return this.getSnapshot();
    if (this.attempt || ACTIVE_PHASES.has(this.snapshot.phase)) return this.getSnapshot();
    if (
      this.snapshot.phase === "blocked" &&
      !RETRYABLE_BLOCKED.has(this.snapshot.reason as UpdateInstallReason)
    ) {
      return this.getSnapshot();
    }
    const version = normalizeVersion(this.options.getDownloadedVersion());
    if (!version) {
      this.setSnapshot("blocked", "update_unavailable", null);
      return this.getSnapshot();
    }
    const access = this.options.getRuntimeAccess();
    if (!access) {
      this.setSnapshot("blocked", "runtime_unavailable", version);
      return this.getSnapshot();
    }
    if (
      !boundedId(access.identity.workspaceId) ||
      !boundedId(access.identity.instanceId) ||
      !positiveSafeInteger(access.identity.serverEpoch)
    ) {
      // 契约要求 1..200 字符与非零安全整数 epoch；不发送必然被拒绝的身份。
      this.setSnapshot("blocked", "runtime_changed", version);
      return this.getSnapshot();
    }
    const attempt: Attempt = {
      seq: ++this.attemptSeq,
      operationId: this.options.operationId?.() ?? randomUUID(),
      version,
      identity: { ...access.identity },
      connection: { ...access.connection },
    };
    this.attempt = attempt;
    this.setSnapshot("waiting", null, version);
    this.scheduleCheck(attempt, 0);
    return this.getSnapshot();
  }

  /** 等待阶段取消：只清同意/定时器/在飞 check，不触碰 Core 任务或执行资源。 */
  cancelInstall(): UpdateInstallSnapshot {
    const attempt = this.attempt;
    if (this.snapshot.phase !== "waiting" || !attempt) return this.getSnapshot();
    this.dropConsent(attempt);
    return this.getSnapshot();
  }

  /**
   * 普通退出取消同意（Main before-quit 同步调用）。此后任何迟到 prepared/停止结果
   * 都不得调用安装器；正在进行的 runtime.stop 仍由既有退出流程负责收敛。
   */
  cancelConsent(): void {
    this.quitting = true;
    const attempt = this.attempt;
    this.clearTimer();
    this.abortCheck();
    this.attempt = null;
    if (this.snapshot.phase === "waiting") {
      this.setSnapshot("idle", null, null);
    } else if (this.snapshot.phase === "preparing") {
      // prepare 已发出：Core 可能已封闸，只能由重启恢复。
      this.setSnapshot("blocked", "restart_required", attempt?.version ?? this.snapshot.version);
    } else if (this.snapshot.phase === "stopping") {
      this.setSnapshot("blocked", "stop_unconfirmed", attempt?.version ?? this.snapshot.version);
    }
  }

  /**
   * Main 在 update-downloaded 事件后先更新自身下载版本，再调用本方法。
   * 版本变化使旧同意失效：等待中回到 idle；prepare/stop 后保持 fail closed。
   */
  noteDownloadedVersion(version: unknown): void {
    const attempt = this.attempt;
    if (!attempt) return;
    if (normalizeVersion(version) === attempt.version) return;
    if (this.snapshot.phase === "waiting") {
      this.dropConsent(attempt);
      return;
    }
    if (this.snapshot.phase === "preparing" || this.snapshot.phase === "stopping") {
      this.failAttempt(attempt, "restart_required");
    }
  }

  /**
   * electron-updater 6.x 的 BaseUpdater 在内部 catch 后 dispatch "error" 而不是 throw；
   * Main 收到 update 错误事件时调用本方法，把 installing 收敛为有限 install_failed。
   * Core 已停止，绝不自动重试/重装，也不尝试重新打开 Core。
   */
  noteInstallerError(): void {
    if (!this.installerInvoked || this.snapshot.phase !== "installing") return;
    this.setSnapshot("blocked", "install_failed", this.snapshot.version);
  }

  /** 释放监听与定时器；不调用安装器，也不停止 Core。 */
  dispose(): void {
    this.cancelConsent();
    this.listeners.clear();
  }

  private scheduleCheck(attempt: Attempt, delayMs: number): void {
    if (!this.isWaitingAttempt(attempt)) return;
    this.clearTimer();
    this.timer = this.schedule(() => {
      this.timer = null;
      void this.runCheck(attempt);
    }, delayMs);
  }

  private async runCheck(attempt: Attempt): Promise<void> {
    if (!this.isWaitingAttempt(attempt)) return;
    const access = this.options.getRuntimeAccess();
    if (!access) {
      this.failAttempt(attempt, "runtime_unavailable");
      return;
    }
    if (!sameRuntime(access, attempt)) {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    if (normalizeVersion(this.options.getDownloadedVersion()) !== attempt.version) {
      // 下载版本已变化：旧同意失效，回到可重新显式确认的 idle。
      this.dropConsent(attempt);
      return;
    }
    const abort = new AbortController();
    this.checkAbort = abort;
    let outcome: UpdatePreparationOutcome;
    try {
      outcome = await this.options.prepare(
        access.connection,
        this.wireRequest(attempt, "check"),
        timeoutSignal(this.checkTimeoutMs, abort.signal),
      );
    } catch {
      // 注入客户端抛错/异常不得变成 unhandled rejection，也不构成任何许可。
      if (this.checkAbort === abort) this.checkAbort = null;
      if (!this.isWaitingAttempt(attempt)) return;
      this.failAttempt(attempt, "runtime_unavailable");
      return;
    }
    if (this.checkAbort === abort) this.checkAbort = null;
    // 取消/退出/新 operation 的迟到回包一律忽略。
    if (!this.isWaitingAttempt(attempt)) return;
    this.handleCheckOutcome(attempt, outcome);
  }

  private handleCheckOutcome(attempt: Attempt, outcome: UpdatePreparationOutcome): void {
    if (outcome.kind === "result") {
      if (outcome.state === "idle") {
        this.beginPrepare(attempt);
        return;
      }
      if (outcome.state === "blocked" && outcome.reason && WAITING_REASONS.has(outcome.reason)) {
        this.setWaitingReason(attempt, outcome.reason);
        this.scheduleCheck(attempt, this.checkIntervalMs);
        return;
      }
      if (outcome.state === "blocked" && outcome.reason) {
        this.failAttempt(attempt, mapCoreBlocked(outcome.reason));
        return;
      }
      // check 不得返回 prepared；idle/blocked 以外的形状按契约违约 fail closed。
      this.failAttempt(attempt, "prepare_failed");
      return;
    }
    if (outcome.kind === "conflict" || outcome.kind === "identity") {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    if (outcome.kind === "unavailable" || outcome.kind === "transport") {
      this.failAttempt(attempt, "runtime_unavailable");
      return;
    }
    this.failAttempt(attempt, "prepare_failed");
  }

  /** check 观察到 idle 后先同步进入不可取消 preparing，再发送 prepare。 */
  private beginPrepare(attempt: Attempt): void {
    if (!this.isWaitingAttempt(attempt)) return;
    const access = this.options.getRuntimeAccess();
    if (!access) {
      this.failAttempt(attempt, "runtime_unavailable");
      return;
    }
    if (!sameRuntime(access, attempt)) {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    if (normalizeVersion(this.options.getDownloadedVersion()) !== attempt.version) {
      this.dropConsent(attempt);
      return;
    }
    this.setSnapshot("preparing", null, attempt.version);
    void this.runPrepare(attempt, access);
  }

  private async runPrepare(attempt: Attempt, access: UpdateRuntimeAccess): Promise<void> {
    let outcome: UpdatePreparationOutcome;
    try {
      outcome = await this.options.prepare(
        access.connection,
        this.wireRequest(attempt, "prepare"),
        timeoutSignal(this.prepareTimeoutMs),
      );
    } catch {
      // 异常不构成准备证明；保持 fail closed，由用户重启或重新显式同意。
      if (this.isCurrentAttempt(attempt) && this.snapshot.phase === "preparing" && !this.quitting) {
        this.failAttempt(attempt, "prepare_failed");
      }
      return;
    }
    if (!this.isCurrentAttempt(attempt) || this.snapshot.phase !== "preparing" || this.quitting) {
      return;
    }
    if (outcome.kind === "result") {
      if (outcome.state === "prepared") {
        this.beginStop(attempt);
        return;
      }
      if (outcome.state === "blocked" && outcome.reason) {
        if (WAITING_REASONS.has(outcome.reason)) {
          // prepare 未封闸：安全回到可取消等待，再走 check 复核。
          this.setSnapshot("waiting", outcome.reason as UpdateInstallReason, attempt.version);
          this.scheduleCheck(attempt, this.checkIntervalMs);
          return;
        }
        this.failAttempt(attempt, mapCoreBlocked(outcome.reason));
        return;
      }
      // idle 只允许出现在 check；prepared 之外的形状不构成准备证明。
      this.failAttempt(attempt, "prepare_failed");
      return;
    }
    if (outcome.kind === "conflict" || outcome.kind === "identity") {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    // 503/超时/传输失败：不把未知清理状态当成功；不调用安装器。
    this.failAttempt(attempt, "prepare_failed");
  }

  /** prepared 精确校验后才允许停止；停止开始后同意不可再取消。 */
  private beginStop(attempt: Attempt): void {
    if (!this.isCurrentAttempt(attempt) || this.snapshot.phase !== "preparing") return;
    const access = this.options.getRuntimeAccess();
    if (!access) {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    if (!sameRuntime(access, attempt)) {
      this.failAttempt(attempt, "runtime_changed");
      return;
    }
    if (normalizeVersion(this.options.getDownloadedVersion()) !== attempt.version) {
      // 已封闸后版本/同意失效：不得安装，恢复只能重启。
      this.failAttempt(attempt, "restart_required");
      return;
    }
    this.setSnapshot("stopping", null, attempt.version);
    void this.runStop(attempt);
  }

  private async runStop(attempt: Attempt): Promise<void> {
    try {
      await this.options.stopRuntime();
    } catch {
      if (this.isCurrentAttempt(attempt) && this.snapshot.phase === "stopping") {
        this.failAttempt(attempt, "stop_unconfirmed");
      }
      return;
    }
    // 普通退出（before-quit）已取消同意时，即使停止确认成功也不得安装。
    if (!this.isCurrentAttempt(attempt) || this.quitting || this.snapshot.phase !== "stopping") {
      return;
    }
    if (normalizeVersion(this.options.getDownloadedVersion()) !== attempt.version) {
      this.failAttempt(attempt, "restart_required");
      return;
    }
    if (this.installerInvoked) return;
    this.installerInvoked = true;
    this.attempt = null;
    this.setSnapshot("installing", null, attempt.version);
    try {
      this.options.installer.quitAndInstall();
    } catch {
      this.setSnapshot("blocked", "install_failed", attempt.version);
    }
  }

  private wireRequest(attempt: Attempt, mode: "check" | "prepare"): UpdatePreparationRequest {
    return {
      mode,
      operation_id: attempt.operationId,
      workspace_id: attempt.identity.workspaceId,
      instance_id: attempt.identity.instanceId,
      server_epoch: attempt.identity.serverEpoch,
    };
  }

  private setWaitingReason(attempt: Attempt, reason: CorePreparationReason): void {
    if (!this.isWaitingAttempt(attempt)) return;
    if (this.snapshot.reason === reason) return;
    this.setSnapshot("waiting", reason as UpdateInstallReason, attempt.version);
  }

  private isCurrentAttempt(attempt: Attempt): boolean {
    return this.attempt === attempt && attempt.seq === this.attemptSeq;
  }

  private isWaitingAttempt(attempt: Attempt): boolean {
    return (
      this.isCurrentAttempt(attempt) &&
      !this.quitting &&
      this.snapshot.phase === "waiting"
    );
  }

  private failAttempt(attempt: Attempt, reason: UpdateInstallReason): void {
    if (!this.isCurrentAttempt(attempt)) return;
    this.clearTimer();
    this.abortCheck();
    this.attempt = null;
    this.setSnapshot("blocked", reason, attempt.version);
  }

  private dropConsent(attempt: Attempt): void {
    if (!this.isCurrentAttempt(attempt)) return;
    this.clearTimer();
    this.abortCheck();
    this.attempt = null;
    this.setSnapshot("idle", null, null);
  }

  private clearTimer(): void {
    if (this.timer === null) return;
    this.cancelSchedule(this.timer);
    this.timer = null;
  }

  private abortCheck(): void {
    this.checkAbort?.abort();
    this.checkAbort = null;
  }

  private setSnapshot(
    phase: UpdateInstallPhase,
    reason: UpdateInstallReason | null,
    version: string | null,
  ): void {
    const canCancel = phase === "waiting";
    const snapshot: UpdateInstallSnapshot = {
      schemaVersion: UPDATE_INSTALL_SCHEMA_VERSION,
      revision: this.snapshot.revision + 1,
      phase,
      reason,
      version: version ?? null,
      canCancel,
    };
    if (
      snapshot.phase === this.snapshot.phase &&
      snapshot.reason === this.snapshot.reason &&
      snapshot.version === this.snapshot.version &&
      snapshot.canCancel === this.snapshot.canCancel
    ) {
      return;
    }
    this.snapshot = snapshot;
    // 只记录有限状态，不输出身份、连接、令牌、路径或原始异常。
    this.log(
      `[update-install] ${snapshot.phase}${snapshot.reason ? ` (${snapshot.reason})` : ""}` +
        `${snapshot.version ? ` version=${snapshot.version}` : ""}`,
    );
    for (const listener of [...this.listeners]) {
      try {
        listener({ ...snapshot });
      } catch {
        // 监听器异常不得中断状态机。
      }
    }
  }
}

function mapCoreBlocked(reason: CorePreparationReason): UpdateInstallReason {
  switch (reason) {
    case "executor_unconfirmed":
      return "executor_unconfirmed";
    case "preparing":
    case "restart_required":
      // 该实例已由其他 operation 封闸；只能重启恢复。
      return "restart_required";
    default:
      return "prepare_failed";
  }
}

const PREPARATION_RESULT_KEYS = [
  "schema_version",
  "operation_id",
  "workspace_id",
  "instance_id",
  "server_epoch",
  "state",
  "reason",
] as const;

const CORE_BLOCKED_REASONS: ReadonlySet<string> = new Set([
  "active_work",
  "request_in_flight",
  "executor_starting",
  "executor_unconfirmed",
  "preparing",
  "restart_required",
]);

const MAX_PREPARATION_BODY_BYTES = 64 * 1024;
/** 取消底层 reader 的有界等待；底层 cancel 不 settle 也不能拖住请求预算。 */
const CANCEL_READER_WAIT_MS = 250;
const READ_ABORTED = Symbol("read-aborted");

type ReadOutcome = { done: boolean; value?: Uint8Array };

async function cancelReader(reader: ReadableStreamDefaultReader<Uint8Array>): Promise<void> {
  let timer: NodeJS.Timeout | undefined;
  try {
    await Promise.race([
      reader.cancel().catch(() => undefined),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, CANCEL_READER_WAIT_MS);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/** 单次读取与取消信号竞速；abort 后不等待底层 read 自行结束。 */
function readChunk(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  signal: AbortSignal,
): Promise<ReadOutcome | typeof READ_ABORTED> {
  if (signal.aborted) return Promise.resolve(READ_ABORTED);
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = (settle: () => void): void => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", onAbort);
      settle();
    };
    const onAbort = (): void => finish(() => resolve(READ_ABORTED));
    signal.addEventListener("abort", onAbort, { once: true });
    void reader.read().then(
      (result) => finish(() => resolve(result)),
      (error: unknown) => finish(() => reject(error)),
    );
  });
}

/**
 * 分块读取并限制响应体；读取与 abort 竞速，超限/取消/超时都先有界取消 reader。
 * 不使用无界 response.json()/response.text()，也不把永久 pending 的流当成功。
 */
async function readBoundedBody(
  response: Response,
  signal: AbortSignal,
): Promise<string | null> {
  const body = response.body;
  if (!body) return null;
  let reader: ReadableStreamDefaultReader<Uint8Array>;
  try {
    reader = body.getReader();
  } catch {
    return null;
  }
  const decoder = new TextDecoder();
  let bytes = 0;
  let text = "";
  let completed = false;
  try {
    for (;;) {
      if (signal.aborted) return null;
      const outcome = await readChunk(reader, signal);
      if (outcome === READ_ABORTED) return null;
      if (signal.aborted) return null;
      if (outcome.done) {
        completed = true;
        break;
      }
      const value = outcome.value;
      if (!value || value.byteLength === 0) continue;
      if (bytes + value.byteLength > MAX_PREPARATION_BODY_BYTES) return null;
      bytes += value.byteLength;
      text += decoder.decode(value, { stream: true });
    }
    text += decoder.decode();
    return text;
  } catch {
    return null;
  } finally {
    if (!completed) {
      await cancelReader(reader);
    }
    try {
      reader.releaseLock();
    } catch {
      // 取消后仍可能有未 settle 的底层 read；释放锁失败不覆盖请求结果。
    }
  }
}

function parsePreparationResult(
  payload: unknown,
  request: UpdatePreparationRequest,
): UpdatePreparationOutcome {
  const body = record(payload);
  if (!body || !exactKeys(body, PREPARATION_RESULT_KEYS)) return { kind: "invalid" };
  if (body.schema_version !== 1) return { kind: "invalid" };
  // 只接受同一 operation 与完全相同身份的当前实例回包；旧 epoch/operation 不构成证明。
  if (
    body.operation_id !== request.operation_id ||
    body.workspace_id !== request.workspace_id ||
    body.instance_id !== request.instance_id ||
    body.server_epoch !== request.server_epoch
  ) {
    return { kind: "invalid" };
  }
  const state = body.state;
  const reason = body.reason;
  if (state === "idle") {
    return reason === null ? { kind: "result", state, reason: null } : { kind: "invalid" };
  }
  if (state === "prepared") {
    return reason === null ? { kind: "result", state, reason: null } : { kind: "invalid" };
  }
  if (state === "blocked") {
    return typeof reason === "string" && CORE_BLOCKED_REASONS.has(reason)
      ? { kind: "result", state, reason: reason as CorePreparationReason }
      : { kind: "invalid" };
  }
  return { kind: "invalid" };
}

/**
 * 构造同实例 check/prepare 客户端。只经 Main 捕获的私有连接调用；
 * 回包必须精确回显 operation/workspace/instance/epoch，否则视为无效证明。
 */
export function createUpdatePreparationClient(
  fetchImpl: FetchLike = fetch,
  options: { requestId?: () => string } = {},
): UpdatePreparationClient {
  const newRequestId = options.requestId ?? randomUUID;
  return async (connection, request, signal) => {
    let response: Response;
    try {
      response = await fetchImpl(`${connection.baseUrl}/api/v1/system/update-preparation`, {
        method: "POST",
        headers: {
          Accept: "application/json",
          // 实例令牌只在 Main 私有连接中使用，不进入 Renderer 或日志。
          "X-YaoMu-Token": connection.token,
          request_id: newRequestId(),
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          mode: request.mode,
          operation_id: request.operation_id,
          workspace_id: request.workspace_id,
          instance_id: request.instance_id,
          server_epoch: request.server_epoch,
        }),
        signal,
        redirect: "error",
      });
    } catch {
      return { kind: "transport" };
    }

    if (response.status === 409) return { kind: "conflict" };
    if (response.status === 401 || response.status === 403) return { kind: "identity" };
    if (response.status === 503) return { kind: "unavailable" };
    if (response.status !== 200) return { kind: "invalid" };

    const text = await readBoundedBody(response, signal);
    if (text === null) {
      // 超时/取消/流错误都不构成准备证明；abort 归为传输失败。
      return signal.aborted ? { kind: "transport" } : { kind: "invalid" };
    }
    let payload: unknown;
    try {
      payload = JSON.parse(text) as unknown;
    } catch {
      return { kind: "invalid" };
    }
    return parsePreparationResult(payload, request);
  };
}

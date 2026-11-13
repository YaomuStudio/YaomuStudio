/**
 * Desktop Runtime Controller：宿主初始化状态的唯一权威（第三批）。
 *
 * - 包装一个长期存活的 RuntimeSupervisor；重试只让同一个拥有者再次 start，
 *   不创建第二个 Supervisor、不绕过既有清理隔离。
 * - launcher 顺序执行 prepare（WSL/环境）→ startCore → 认证 system 握手；
 *   只有 app_id=yaomu、protocol_version=0.2、core_ready=true 且身份字段合法时，
 *   该实例才被接受。失败时先确认自有 Core 退出，无法确认则报告
 *   CORE_CLEANUP_FAILED，由 Supervisor 保持隔离。
 * - 快照严格对齐 contracts/desktop-runtime.schema.json：schemaVersion=1、
 *   单调 revision、已确认连接 generation、有限 phase/reason/canRetry。
 *   快照不含端口、URL、令牌、路径、原始错误或 Core 身份字符串。
 * - 首次握手锁定 workspace；同会话不同 workspace 失败且不可重试。
 *   同 workspace 的不同兼容实例允许递增 generation。
 * - 本模块不依赖 electron，可在普通 Node 测试中验证生命周期与竞态。
 */
import type { ChildProcess } from "node:child_process";
import {
  CoreBootstrapError,
  abortError,
  isAbortError,
  terminateProcessTree,
  type CoreConnection,
  type CoreProcessInfo,
  type FetchLike,
} from "./bootstrap";
import { RuntimeSupervisor, type CoreRuntimeState } from "./supervisor";

export const RUNTIME_SCHEMA_VERSION = 1;

export type RuntimePhase =
  | "preparing"
  | "starting"
  | "ready"
  | "restarting"
  | "failed"
  | "stopping"
  | "stopped";

export type RuntimeReason =
  | "startup_failed"
  | "core_exited"
  | "cleanup_unconfirmed"
  | "handshake_failed"
  | "protocol_mismatch"
  | "workspace_mismatch";

export interface RuntimeSnapshot {
  schemaVersion: 1;
  revision: number;
  generation: number;
  phase: RuntimePhase;
  reason: RuntimeReason | null;
  canRetry: boolean;
}

/** 认证 system 握手结果；只在 Main 内使用，不进入快照。 */
export interface SystemIdentity {
  appId: "yaomu";
  protocolVersion: "0.2";
  instanceId: string;
  workspaceId: string;
  serverEpoch: number;
}

export type LaunchFailureReason =
  | "startup_failed"
  | "handshake_failed"
  | "protocol_mismatch"
  | "workspace_mismatch";

/** 握手层失败：只携带有限 reason 与安全文案。 */
export class RuntimeHandshakeError extends Error {
  readonly reason: "handshake_failed" | "protocol_mismatch";

  constructor(reason: "handshake_failed" | "protocol_mismatch", message: string) {
    super(message);
    this.name = "RuntimeHandshakeError";
    this.reason = reason;
  }
}

/** launcher 层失败：安全 reason 供控制器分类，不包含原始异常文本。 */
export class RuntimeLaunchError extends Error {
  readonly reason: LaunchFailureReason;

  constructor(reason: LaunchFailureReason, message: string) {
    super(message);
    this.name = "RuntimeLaunchError";
    this.reason = reason;
  }
}

const DEFAULT_HANDSHAKE_TIMEOUT_MS = 5_000;
/** 身份字段是固定格式的不透明 ID；限制长度只为拒绝异常响应。 */
const MAX_IDENTITY_LENGTH = 512;
/** /api/v1/system 只应返回小 JSON；超限按握手失败处理。 */
const MAX_SYSTEM_BODY_BYTES = 64 * 1024;

const NON_RETRYABLE = new Set<RuntimeReason>([
  "cleanup_unconfirmed",
  "protocol_mismatch",
  "workspace_mismatch",
]);

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function identityField(value: unknown): string {
  if (typeof value !== "string" || value.length === 0 || value.length > MAX_IDENTITY_LENGTH) {
    throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手身份字段不合法");
  }
  return value;
}

/** 解析并校验 Core 的 system 响应；只接受契约要求的有限字段。 */
function parseSystemIdentity(payload: unknown): SystemIdentity {
  const body = record(payload);
  if (!body) {
    throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手响应不合法");
  }
  if (body.app_id !== "yaomu" || body.protocol_version !== "0.2") {
    throw new RuntimeHandshakeError("protocol_mismatch", "Core 应用或协议版本不兼容");
  }
  if (body.core_ready !== true) {
    throw new RuntimeHandshakeError("handshake_failed", "Core 尚未就绪");
  }
  const instanceId = identityField(body.instance_id);
  const workspaceId = identityField(body.workspace_id);
  const serverEpoch = body.server_epoch;
  if (
    typeof serverEpoch !== "number" ||
    !Number.isSafeInteger(serverEpoch) ||
    serverEpoch < 0
  ) {
    throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手 epoch 不合法");
  }
  return {
    appId: "yaomu",
    protocolVersion: "0.2",
    instanceId,
    workspaceId,
    serverEpoch,
  };
}

export interface SystemHandshakeOptions {
  timeoutMs?: number;
  signal?: AbortSignal;
  fetchImpl?: FetchLike;
}

/** 取消 reader 的有界等待；底层 cancel 不 settle 也不能拖住握手超时。 */
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
 * 分块读取 system 响应：累计超过 64KiB 立即取消 reader，且不再继续 read。
 * 读取前后都检查取消/超时；成功、超限、取消与超时都释放 reader。
 */
async function readSystemBody(
  response: Response,
  external: AbortSignal | undefined,
  timedOut: () => boolean,
  signal: AbortSignal,
): Promise<string> {
  const body = response.body;
  if (!body) {
    throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手响应不可读");
  }
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let bytes = 0;
  let text = "";
  let completed = false;
  const failure = (): Error => {
    if (external?.aborted) return abortError("系统握手已取消");
    return new RuntimeHandshakeError(
      "handshake_failed",
      timedOut() ? "Core 系统握手超时" : "Core 系统握手响应不可读",
    );
  };

  try {
    for (;;) {
      if (external?.aborted || timedOut()) throw failure();
      const outcome = await readChunk(reader, signal);
      if (outcome === READ_ABORTED) throw failure();
      // 读取后再次检查：超时/取消与数据到达同轮时取消优先，不继续处理。
      if (external?.aborted || timedOut()) throw failure();
      if (outcome.done) {
        completed = true;
        break;
      }
      const value = outcome.value;
      if (!value || value.byteLength === 0) continue;
      if (bytes + value.byteLength > MAX_SYSTEM_BODY_BYTES) {
        // 不把截断内容当成功；先停止底层流，再以安全 reason 失败。
        throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手响应超限");
      }
      bytes += value.byteLength;
      text += decoder.decode(value, { stream: true });
    }
    text += decoder.decode();
    return text;
  } catch (error) {
    if (error instanceof RuntimeHandshakeError || isAbortError(error)) throw error;
    throw failure();
  } finally {
    if (!completed) {
      // 超限/取消/超时/流错误都释放自有 reader，避免半读 transport 残留。
      await cancelReader(reader);
    }
    try {
      reader.releaseLock();
    } catch {
      // 取消后仍可能有未 settle 的底层 read；释放锁失败不覆盖握手结果。
    }
  }
}

/**
 * 经 Main 私有连接完成认证 system 握手。
 * 请求受外部 AbortSignal 与有界超时同时约束；取消抛 AbortError，
 * 超时/网络/形状错误只抛有限 reason 的安全错误，不泄露连接或原始异常。
 */
export async function fetchSystemIdentity(
  connection: CoreConnection,
  options: SystemHandshakeOptions = {},
): Promise<SystemIdentity> {
  const timeoutMs = options.timeoutMs ?? DEFAULT_HANDSHAKE_TIMEOUT_MS;
  const external = options.signal;
  if (external?.aborted) throw abortError("系统握手已取消");

  const fetchImpl = options.fetchImpl ?? fetch;
  const timeoutController = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    timeoutController.abort();
  }, timeoutMs);
  const signal = external
    ? AbortSignal.any([external, timeoutController.signal])
    : timeoutController.signal;

  try {
    let response: Response;
    try {
      response = await fetchImpl(`${connection.baseUrl}/api/v1/system`, {
        method: "GET",
        headers: { Accept: "application/json", "X-YaoMu-Token": connection.token },
        signal,
        redirect: "error",
      });
    } catch {
      if (external?.aborted) throw abortError("系统握手已取消");
      throw new RuntimeHandshakeError(
        "handshake_failed",
        timedOut ? "Core 系统握手超时" : "Core 系统握手不可用",
      );
    }

    if (!response.ok) {
      throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手返回错误");
    }

    let text: string;
    try {
      text = await readSystemBody(response, external, () => timedOut, signal);
    } catch (error) {
      if (error instanceof RuntimeHandshakeError || isAbortError(error)) throw error;
      throw new RuntimeHandshakeError(
        "handshake_failed",
        timedOut ? "Core 系统握手超时" : "Core 系统握手响应不可读",
      );
    }

    let payload: unknown;
    try {
      payload = JSON.parse(text) as unknown;
    } catch {
      throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手响应不合法");
    }
    return parseSystemIdentity(payload);
  } finally {
    clearTimeout(timer);
  }
}

export interface RuntimeControllerOptions {
  /** 窗口环境准备（WSL/工具探测）；可选。取消时以 AbortError 结束。 */
  prepare?: (signal: AbortSignal) => Promise<void>;
  /** 拉起 Core 并等待健康；必须自行确认失败时的自有 child 清理。 */
  startCore: (signal: AbortSignal) => Promise<CoreProcessInfo>;
  /** 认证 system 握手；缺省使用 fetchSystemIdentity。 */
  handshake?: (connection: CoreConnection, signal: AbortSignal) => Promise<SystemIdentity>;
  /** 握手失败后终止自有 Core；默认 terminateProcessTree。 */
  terminateChild?: (child: ChildProcess) => Promise<void>;
  handshakeTimeoutMs?: number;
  maxRestarts?: number;
  stopTimeoutMs?: number;
  settleTimeoutMs?: number;
  log?: (line: string) => void;
  /** 连接可用性变化；只会在握手确认的 ready 快照后提供连接。 */
  onConnectionChange?: (connection: CoreConnection | null) => void;
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw abortError("Core 启动已取消");
}

function describe(value: unknown): string {
  return value instanceof Error ? value.message : String(value);
}

/**
 * 宿主生命周期控制器。构造时创建唯一的 RuntimeSupervisor；
 * start/retry 共享该拥有者，stop 是唯一退出入口。
 */
export class RuntimeController {
  private readonly options: RuntimeControllerOptions;
  private readonly log: (line: string) => void;
  private readonly supervisor: RuntimeSupervisor;
  private readonly listeners = new Set<(snapshot: RuntimeSnapshot) => void>();
  private snapshot: RuntimeSnapshot = {
    schemaVersion: RUNTIME_SCHEMA_VERSION,
    revision: 1,
    generation: 0,
    phase: "preparing",
    reason: null,
    canRetry: false,
  };
  private startPromise: Promise<void> | null = null;
  private stopPromise: Promise<void> | null = null;
  private closing = false;
  private workspaceId: string | null = null;
  private lastInstanceId: string | null = null;
  private pendingIdentity: SystemIdentity | null = null;
  // 当前 ready 代次已接受的身份；只在 Main 内使用，停止/失联/新启动即失效。
  private acceptedIdentity: SystemIdentity | null = null;

  constructor(options: RuntimeControllerOptions) {
    this.options = options;
    this.log = options.log ?? (() => undefined);
    this.supervisor = new RuntimeSupervisor({
      launcher: (signal) => this.launch(signal),
      maxRestarts: options.maxRestarts,
      stopTimeoutMs: options.stopTimeoutMs,
      settleTimeoutMs: options.settleTimeoutMs,
      log: options.log,
      onState: (state, error) => this.handleSupervisorState(state, error),
    });
  }

  getSnapshot(): RuntimeSnapshot {
    return { ...this.snapshot };
  }

  /** 业务连接只在握手确认的 ready 快照期间可用；停止开始后立即断开。 */
  getConnection(): CoreConnection | null {
    if (this.closing || this.snapshot.phase !== "ready") return null;
    return this.supervisor.getConnection();
  }

  /**
   * Main-only：返回当前 ready 代次已认证的 Core 身份与 generation。
   * 不进入 Renderer 快照；停止、失联或重新启动后返回 null。
   */
  getAcceptedIdentity(): (SystemIdentity & { generation: number }) | null {
    if (this.closing || this.snapshot.phase !== "ready") return null;
    const identity = this.acceptedIdentity;
    if (!identity || !this.supervisor.getConnection()) return null;
    return { ...identity, generation: this.snapshot.generation };
  }

  subscribe(listener: (snapshot: RuntimeSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /** 首次初始化；重复调用共享同一结果，不第二次拉起 Core。 */
  start(): Promise<void> {
    if (this.startPromise) return this.startPromise;
    if (this.closing) return Promise.resolve();
    const promise = this.runAttempt();
    this.startPromise = promise;
    return promise;
  }

  /**
   * 显式重试：只在 failed 且 canRetry=true 时安全地再次使用同一 Supervisor。
   * preparing/starting/restarting/ready/stopping/stopped 及隔离原因均只返回当前快照。
   */
  retry(): RuntimeSnapshot {
    const current = this.snapshot;
    if (this.closing || current.phase !== "failed" || !current.canRetry) {
      return this.getSnapshot();
    }
    // 同步切换到新尝试的初始 phase：并发 retry 看到非 failed 后不再重复启动。
    this.setPhase(this.options.prepare ? "preparing" : "starting");
    void this.runAttempt();
    return this.getSnapshot();
  }

  /** Main 已知的启动前不可用（如冒烟 no-core）；不产生 Core，也不可重试。 */
  markFailed(reason: RuntimeReason, canRetry: boolean): RuntimeSnapshot {
    if (!this.closing && this.snapshot.phase !== "ready") {
      this.setFailed(reason, canRetry);
    }
    return this.getSnapshot();
  }

  /** 停止唯一 Core 拥有者；之后拒绝新 retry。失败保留隔离事实并向上抛。 */
  stop(): Promise<void> {
    this.closing = true;
    if (!this.stopPromise) {
      const promise = this.supervisor.stop().catch((error: unknown) => {
        this.setFailed("cleanup_unconfirmed", false);
        throw error;
      });
      this.stopPromise = promise;
      // 调用方通常有界等待；这里防止忽略 promise 时出现未处理拒绝。
      void promise.catch(() => undefined);
    }
    return this.stopPromise;
  }

  private async runAttempt(): Promise<void> {
    try {
      await this.supervisor.start();
    } catch (error) {
      if (isAbortError(error)) return;
      this.log(`[runtime] 启动失败：${describe(error)}`);
      // 正常启动失败已由 onState(crashed) 发布有限 reason；这里只兜底
      // Supervisor 在进入 launcher 前拒绝（清理隔离/正在停止）的情况。
      if (this.snapshot.phase !== "failed") this.setFailed("startup_failed", false);
    }
  }

  /** Supervisor 的唯一 launcher：prepare → startCore → 认证握手。 */
  private async launch(signal: AbortSignal): Promise<CoreProcessInfo> {
    this.pendingIdentity = null;
    if (this.options.prepare) {
      try {
        await this.options.prepare(signal);
      } catch (error) {
        throw this.mapPreparationError(error);
      }
      throwIfAborted(signal);
      this.setPhase("starting");
    }

    throwIfAborted(signal);
    const info = await this.options.startCore(signal);

    let identity: SystemIdentity;
    try {
      identity = await this.performHandshake(info, signal);
      // 迟到的握手不能绕过取消：先核 signal 再 pin workspace，返回前再核一次。
      throwIfAborted(signal);
      this.acceptIdentity(identity);
      throwIfAborted(signal);
    } catch (error) {
      // 握手/身份失败后只停本次自有 Core；无法确认退出则交给 Supervisor 隔离。
      try {
        await this.terminateOwned(info.child);
      } catch {
        throw new CoreBootstrapError(
          "CORE_CLEANUP_FAILED",
          "握手失败后无法确认自有 Core 退出",
        );
      }
      if (isAbortError(error)) throw abortError("Core 启动已取消");
      if (error instanceof RuntimeLaunchError) throw error;
      if (error instanceof RuntimeHandshakeError) {
        throw new RuntimeLaunchError(error.reason, error.message);
      }
      throw new RuntimeLaunchError("handshake_failed", "Core 系统握手失败");
    }

    this.pendingIdentity = identity;
    return info;
  }

  private performHandshake(
    info: CoreProcessInfo,
    signal: AbortSignal,
  ): Promise<SystemIdentity> {
    const connection: CoreConnection = { baseUrl: info.baseUrl, token: info.token };
    if (this.options.handshake) return this.options.handshake(connection, signal);
    return fetchSystemIdentity(connection, {
      signal,
      timeoutMs: this.options.handshakeTimeoutMs,
    });
  }

  private terminateOwned(child: ChildProcess): Promise<void> {
    if (this.options.terminateChild) return this.options.terminateChild(child);
    return terminateProcessTree(child);
  }

  private mapPreparationError(error: unknown): Error {
    if (isAbortError(error)) return abortError("Core 启动已取消");
    if (
      typeof error === "object" &&
      error !== null &&
      (error as { name?: unknown }).name === "WslProbeCleanupError"
    ) {
      // 探测 transport 无法确认退出：映射为 Supervisor 的未确认清理类别。
      return new CoreBootstrapError("CORE_CLEANUP_FAILED", "环境准备无法确认清理完成");
    }
    return error instanceof Error ? error : new Error("环境准备失败");
  }

  /** 首次成功握手锁定 workspace；同会话不同 workspace 失败关闭。 */
  private acceptIdentity(identity: SystemIdentity): void {
    if (this.workspaceId !== null && identity.workspaceId !== this.workspaceId) {
      throw new RuntimeLaunchError(
        "workspace_mismatch",
        "Core 工作区与本次会话不一致，需重启应用",
      );
    }
    if (this.workspaceId === null) this.workspaceId = identity.workspaceId;
  }

  private handleSupervisorState(state: CoreRuntimeState, error?: Error): void {
    switch (state) {
      case "starting":
        this.pendingIdentity = null;
        this.acceptedIdentity = null;
        this.setPhase(this.options.prepare ? "preparing" : "starting");
        break;
      case "restarting":
        // 自动重启先用 restarting 表示连接已丢失、正在建立新连接；
        // prepare 完成后 launcher 会切到 starting，握手成功后才 ready。
        this.pendingIdentity = null;
        this.acceptedIdentity = null;
        this.setPhase("restarting");
        break;
      case "ready": {
        // 退出/停止已开始：迟到的 ready 不得复活连接或窗口。
        if (this.closing) {
          this.pendingIdentity = null;
          this.acceptedIdentity = null;
          break;
        }
        const identity = this.pendingIdentity;
        if (!identity) {
          // 防御：没有认证身份的实例绝不发布 ready，并停止该自有 Core。
          this.acceptedIdentity = null;
          this.setFailed("handshake_failed", false);
          void this.supervisor.stop().catch((stopError: unknown) => {
            this.log(`[runtime] 停止未认证 Core 失败：${describe(stopError)}`);
          });
          break;
        }
        this.pendingIdentity = null;
        this.acceptedIdentity = { ...identity };
        if (identity.instanceId !== this.lastInstanceId) {
          this.lastInstanceId = identity.instanceId;
          this.setReady(this.snapshot.generation + 1);
        } else {
          this.setReady(this.snapshot.generation);
        }
        break;
      }
      case "crashed": {
        this.acceptedIdentity = null;
        const failure = this.classifyFailure(error);
        this.setFailed(failure.reason, failure.canRetry);
        break;
      }
      case "stopping":
        this.acceptedIdentity = null;
        this.setPhase("stopping");
        break;
      case "stopped":
        this.acceptedIdentity = null;
        this.setPhase("stopped");
        break;
    }
    this.options.onConnectionChange?.(this.getConnection());
  }

  private classifyFailure(error?: Error): { reason: RuntimeReason; canRetry: boolean } {
    if (this.closing) {
      // 停止期间失败只可能是清理未确认；不可重试，也不能伪 stopped。
      return { reason: "cleanup_unconfirmed", canRetry: false };
    }
    const reason = (error as { reason?: unknown } | undefined)?.reason;
    if (
      reason === "cleanup_unconfirmed" ||
      reason === "protocol_mismatch" ||
      reason === "workspace_mismatch"
    ) {
      return { reason, canRetry: false };
    }
    if (reason === "handshake_failed" || reason === "startup_failed") {
      return { reason, canRetry: true };
    }
    if ((error as { code?: unknown } | undefined)?.code === "CORE_CLEANUP_FAILED") {
      return { reason: "cleanup_unconfirmed", canRetry: false };
    }
    if ((error as { code?: unknown } | undefined)?.code === "CORE_EXITED") {
      return { reason: "core_exited", canRetry: true };
    }
    return { reason: "startup_failed", canRetry: true };
  }

  private setReady(generation: number): void {
    this.update({ generation, phase: "ready", reason: null, canRetry: false });
  }

  private setFailed(reason: RuntimeReason, canRetry: boolean): void {
    this.update({
      phase: "failed",
      reason,
      canRetry: canRetry && !this.closing && !NON_RETRYABLE.has(reason),
    });
  }

  private setPhase(phase: Exclude<RuntimePhase, "failed" | "ready">): void {
    this.update({ phase, reason: null, canRetry: false });
  }

  private update(next: {
    generation?: number;
    phase?: RuntimePhase;
    reason?: RuntimeReason | null;
    canRetry?: boolean;
  }): void {
    const current = this.snapshot;
    const generation = next.generation ?? current.generation;
    const phase = next.phase ?? current.phase;
    const reason = next.reason === undefined ? current.reason : next.reason;
    const canRetry = next.canRetry ?? current.canRetry;
    if (
      generation === current.generation &&
      phase === current.phase &&
      reason === current.reason &&
      canRetry === current.canRetry
    ) {
      return;
    }
    const snapshot: RuntimeSnapshot = {
      schemaVersion: RUNTIME_SCHEMA_VERSION,
      revision: current.revision + 1,
      generation,
      phase,
      reason,
      canRetry,
    };
    this.snapshot = snapshot;
    if (phase !== current.phase || reason !== current.reason || generation !== current.generation) {
      // 只记录有限状态；不输出端口、令牌、URL、路径或原始异常。
      this.log(
        `[runtime] ${current.phase} -> ${phase}` +
          `${reason ? ` (${reason})` : ""} generation=${generation}`,
      );
    }
    for (const listener of [...this.listeners]) {
      try {
        listener({ ...snapshot });
      } catch (error) {
        this.log(`[runtime] 快照监听失败：${describe(error)}`);
      }
    }
  }
}

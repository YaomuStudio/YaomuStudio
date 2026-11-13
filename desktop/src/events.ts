/**
 * Core SSE 事件桥：Main 进程订阅内部事件流，按窗口订阅情况转发给 Renderer。
 *
 * - 每窗口按引用计数 attach/detach；最后一个订阅取消后断开 SSE。
 * - 客户端按 seq 去重；进度类事件不推进持久游标（由 Core 语义保证）。
 * - 游标过期（EVENT_CURSOR_EXPIRED）时停止重试并上报，要求重新获取快照。
 * - 连接通知（contracts/desktop-events.md）只是传输信号，不是业务事件：
 *   每次 HTTP SSE 成功接收响应体后向当前已授权订阅者发固定载荷，供客户端合并快照。
 */
import type { WebContents } from "electron";
import type { CoreConnection, FetchLike } from "./bootstrap";
import { DesktopAuthorization } from "./ipc/authorization";

const EVENT_CHANNEL = "yaomu:api:event";
const EVENT_ERROR_CHANNEL = "yaomu:api:event-error";
const CONNECTED_CHANNEL = "yaomu:api:events-connected";
const ATTACH_CHANNEL = "yaomu:events:attach";
const DETACH_CHANNEL = "yaomu:events:detach";

const DEFAULT_RETRY_MS = 1_000;
const MAX_RETRY_MS = 10_000;

export interface EventStreamError {
  code: string;
  message: string;
  retryable: boolean;
}

export interface EventStreamOptions {
  connection: CoreConnection;
  afterSeq?: number;
  onEvent: (event: unknown) => void;
  onError?: (error: EventStreamError) => void;
  /**
   * HTTP SSE 成功收到响应体后调用（初连与自动重连都算）。只表示传输已建立，
   * 不表示历史回放已排空或模型就绪；回调异常不得影响流生命周期。
   */
  onConnected?: () => void;
  fetchImpl?: FetchLike;
  retryDelayMs?: number;
  maxRetryDelayMs?: number;
}

/** 单个 Core 的增量事件流（SSE over fetch）。 */
export class EventStream {
  private readonly options: EventStreamOptions;
  private controller: AbortController | null = null;
  private stopped = false;
  private lastSeq: number;
  private retryTimer: NodeJS.Timeout | null = null;
  private retryDelay: number;
  /** 本次连接是否已交付有效帧；用于区分正常分批续传与零帧断流。 */
  private delivered = false;

  constructor(options: EventStreamOptions) {
    this.options = options;
    this.lastSeq = options.afterSeq ?? 0;
    this.retryDelay = options.retryDelayMs ?? DEFAULT_RETRY_MS;
  }

  start(): void {
    if (this.stopped || this.controller) return;
    void this.run();
  }

  stop(): void {
    this.stopped = true;
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this.controller?.abort();
    this.controller = null;
  }

  private get maxRetryDelay(): number {
    return this.options.maxRetryDelayMs ?? MAX_RETRY_MS;
  }

  private async run(): Promise<void> {
    const controller = new AbortController();
    this.controller = controller;
    this.delivered = false;
    try {
      const fetchImpl = this.options.fetchImpl ?? fetch;
      const response = await fetchImpl(
        `${this.options.connection.baseUrl}/api/v1/events?after_seq=${this.lastSeq}`,
        {
          headers: {
            Accept: "text/event-stream",
            "X-YaoMu-Token": this.options.connection.token,
          },
          signal: controller.signal,
        },
      );
      // stop() 可能发生在 fetch 飞行期间；被停止/替换的流不得再产生任何回调。
      if (this.stopped) {
        controller.abort();
        return;
      }
      if (response.status === 409) {
        this.report({
          code: "EVENT_CURSOR_EXPIRED",
          message: "事件游标已过期，需要重新获取快照",
          retryable: false,
        });
        this.stop();
        return;
      }
      if (response.status === 401 || response.status === 403) {
        this.report({
          code: response.status === 401 ? "LOCAL_AUTH_REQUIRED" : "LOCAL_ACCESS_DENIED",
          message: `事件流鉴权失败（HTTP ${response.status}）`,
          retryable: false,
        });
        this.stop();
        return;
      }
      if (!response.ok || !response.body) {
        throw new Error(`事件流响应不可用（HTTP ${response.status}）`);
      }
      this.notifyConnected();
      await this.readBody(response.body);
    } catch (error) {
      if (this.stopped) return;
      this.report({
        code: "CORE_NOT_READY",
        message: error instanceof Error ? error.message : String(error),
        retryable: true,
      });
    } finally {
      if (this.controller === controller) this.controller = null;
    }
    this.scheduleRetry();
  }

  private async readBody(body: ReadableStream<Uint8Array>): Promise<void> {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
      while (!this.stopped) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
        let boundary = buffer.indexOf("\n\n");
        while (boundary >= 0) {
          const block = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          this.handleBlock(block);
          boundary = buffer.indexOf("\n\n");
        }
      }
    } finally {
      reader.releaseLock();
    }
    if (!this.stopped && !this.delivered) throw new Error("事件流结束");
  }

  private handleBlock(block: string): void {
    if (this.stopped) return;
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) dataLines.push(line.slice(5).replace(/^ /, ""));
    }
    if (dataLines.length === 0) return;
    try {
      const parsed = JSON.parse(dataLines.join("\n")) as { seq?: unknown };
      // seq>0 是持久事件，按游标去重；seq=0 是可丢弃实时事件（如执行过程行），
      // 不推进游标，也不能被当作旧事件丢弃。
      const seq = typeof parsed.seq === "number" && parsed.seq > 0 ? parsed.seq : 0;
      if (seq > 0 && seq <= this.lastSeq) return;
      // 收到有效帧才说明连接可用；连接建立后立刻断流不能重置退避，
      // 否则上游异常时会退化成每秒重连并拖垮连接池。
      this.delivered = true;
      this.retryDelay = this.options.retryDelayMs ?? DEFAULT_RETRY_MS;
      if (seq > 0) this.lastSeq = seq;
      this.options.onEvent(parsed);
    } catch {
      // 非 JSON 帧丢弃，不把解析失败当业务事件。
    }
  }

  private scheduleRetry(): void {
    if (this.stopped || this.retryTimer) return;
    const delay = this.retryDelay;
    this.retryDelay = Math.min(this.retryDelay * 2, this.maxRetryDelay);
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      if (!this.stopped) void this.run();
    }, delay);
  }

  /** 只在流仍存活时通知；用户回调异常不得触发重连或改变流状态。 */
  private notifyConnected(): void {
    if (this.stopped) return;
    try {
      this.options.onConnected?.();
    } catch {
      // Renderer 回调错误由 Renderer 自己处理；传输层继续读取事件。
    }
  }

  private report(error: EventStreamError): void {
    if (this.stopped) return;
    this.options.onError?.(error);
  }
}

interface Subscriber {
  sender: WebContents;
  refs: number;
}

export interface EventBrokerOptions {
  fetchImpl?: FetchLike;
  log?: (line: string) => void;
  isTrustedSender?: (sender: WebContents) => boolean;
  /** 传输退避参数，默认 1s 起、10s 封顶；仅内部/测试调整。 */
  retryDelayMs?: number;
  maxRetryDelayMs?: number;
}

/** 按窗口引用计数管理 SSE 连接与广播。 */
export class EventBroker {
  private readonly subscribers = new Map<number, Subscriber>();
  private readonly options: EventBrokerOptions;
  private connection: CoreConnection | null = null;
  private stream: EventStream | null = null;
  private lastSeq = 0;

  constructor(options: EventBrokerOptions = {}) {
    this.options = options;
  }

  setConnection(connection: CoreConnection | null): void {
    this.connection = connection;
    if (!connection) {
      this.stopStream();
      return;
    }
    if (this.subscribers.size > 0) this.ensureStream();
  }

  attach(sender: WebContents, afterSeq?: number): void {
    const watermark =
      typeof afterSeq === "number" && Number.isFinite(afterSeq)
        ? Math.max(0, Math.floor(afterSeq))
        : undefined;
    const existing = this.subscribers.get(sender.id);
    if (existing) {
      existing.refs += 1;
    } else {
      const subscriber: Subscriber = { sender, refs: 1 };
      this.subscribers.set(sender.id, subscriber);
      sender.once("destroyed", () => {
        this.subscribers.delete(sender.id);
        if (this.subscribers.size === 0) this.stopStream();
      });
    }
    // 快照高水位握手：用快照返回的 H 推进游标，重启流从 H 续订，
    // 避免把快照已包含的历史再回放一遍；只前进不后退。
    if (watermark !== undefined && watermark > this.lastSeq) {
      this.lastSeq = watermark;
      this.ensureStream();
      return;
    }
    if (!this.stream) this.ensureStream();
  }

  detach(sender: WebContents): void {
    const subscriber = this.subscribers.get(sender.id);
    if (!subscriber) return;
    subscriber.refs -= 1;
    if (subscriber.refs <= 0) this.subscribers.delete(sender.id);
    if (this.subscribers.size === 0) this.stopStream();
  }

  detachWindow(sender: WebContents): void {
    this.subscribers.delete(sender.id);
    if (this.subscribers.size === 0) this.stopStream();
  }

  stop(): void {
    this.subscribers.clear();
    this.stopStream();
  }

  private ensureStream(): void {
    if (!this.connection) return;
    this.stopStream();
    const connection = this.connection;
    const stream = new EventStream({
      connection,
      afterSeq: this.lastSeq,
      fetchImpl: this.options.fetchImpl,
      retryDelayMs: this.options.retryDelayMs,
      maxRetryDelayMs: this.options.maxRetryDelayMs,
      // 已被替换/停止的旧流晚到的回调一律丢弃：只认当前拥有的 stream 实例。
      onConnected: () => {
        if (this.stream === stream) this.broadcastConnected();
      },
      onEvent: (event) => {
        if (this.stream === stream) this.broadcast(event);
      },
      onError: (error) => {
        if (this.stream !== stream) return;
        this.options.log?.(`[events] ${error.code}: ${error.message}`);
        // 可重试错误由 EventStream 退避重连；只有不可恢复错误才提示 Renderer 重新同步，
        // 并清空流，等待客户端用快照高水位重新 attach。
        if (!error.retryable) {
          this.broadcastError(error);
          this.stopStream();
        }
      },
    });
    this.stream = stream;
    stream.start();
  }

  private stopStream(): void {
    this.stream?.stop();
    this.stream = null;
  }

  private broadcast(event: unknown): void {
    if (typeof event === "object" && event !== null) {
      const seq = (event as { seq?: unknown }).seq;
      if (typeof seq === "number" && seq > this.lastSeq) this.lastSeq = seq;
    }
    for (const [id, subscriber] of this.subscribers) {
      try {
        if (subscriber.sender.isDestroyed() || this.options.isTrustedSender?.(subscriber.sender) === false) {
          this.subscribers.delete(id);
          continue;
        }
        subscriber.sender.send(EVENT_CHANNEL, event);
      } catch {
        this.subscribers.delete(id);
      }
    }
    if (this.subscribers.size === 0) this.stopStream();
  }

  /**
   * 向当前仍订阅且已授权的窗口发送固定连接信号。
   * 载荷只含 schemaVersion；不携带 URL、令牌、任务或序号，也不推进业务游标。
   */
  private broadcastConnected(): void {
    for (const [id, subscriber] of this.subscribers) {
      try {
        if (subscriber.sender.isDestroyed() || this.options.isTrustedSender?.(subscriber.sender) === false) {
          this.subscribers.delete(id);
          continue;
        }
        subscriber.sender.send(CONNECTED_CHANNEL, { schemaVersion: 1 });
      } catch {
        this.subscribers.delete(id);
      }
    }
    if (this.subscribers.size === 0) this.stopStream();
  }

  private broadcastError(error: EventStreamError): void {
    for (const [id, subscriber] of this.subscribers) {
      try {
        if (subscriber.sender.isDestroyed() || this.options.isTrustedSender?.(subscriber.sender) === false) {
          this.subscribers.delete(id);
          continue;
        }
        subscriber.sender.send(EVENT_ERROR_CHANNEL, error);
      } catch {
        this.subscribers.delete(id);
      }
    }
  }
}

/** 注册事件订阅 IPC（只接受 attach/detach 两个通道，不放开任意通道）。 */
export function registerEventIpc(broker: EventBroker, authorization: DesktopAuthorization): void {
  // 延迟加载 electron：本模块的事件流逻辑可在普通 node 进程里独立验证。
  const { ipcMain } = require("electron") as typeof import("electron");
  ipcMain.on(ATTACH_CHANNEL, (event, afterSeq: unknown) => {
    try {
      authorization.requireSender(event);
      broker.attach(event.sender, typeof afterSeq === "number" ? afterSeq : undefined);
    } catch {
      // send 通道没有 Promise 错误回执；不接入事件流，也不向未受信来源回发错误。
    }
  });
  ipcMain.on(DETACH_CHANNEL, (event) => {
    try {
      authorization.requireSender(event);
      broker.detach(event.sender);
    } catch {
      // 不允许子 frame 借同一 WebContents 取消顶层订阅。
    }
  });
}

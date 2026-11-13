/**
 * Desktop Runtime IPC（第三批）：受控读取/订阅/重试宿主初始化快照。
 *
 * - 通道固定为 yaomu:runtime:get / retry（invoke）与 attach / detach（send）；
 *   不接受 Renderer 提供的状态、版本、通道、命令或路径。
 * - 每个 handler 在读状态、登记订阅或启动重试之前调用同一个
 *   DesktopAuthorization.requireSender；invoke 被拒绝时返回安全信封，
 *   send 被拒绝时不回发。
 * - 广播前再次校验来源；导航/销毁由 Main 调用 detachWindow 释放订阅。
 * - 本模块不加载 electron，可在普通 Node 测试中验证授权与顺序。
 */
import type { IpcMain, WebContents } from "electron";
import type { DesktopAuthorization, IpcSource } from "./authorization";
import type { RuntimeController, RuntimeSnapshot } from "../runtime";

export const RUNTIME_GET_CHANNEL = "yaomu:runtime:get";
export const RUNTIME_RETRY_CHANNEL = "yaomu:runtime:retry";
export const RUNTIME_ATTACH_CHANNEL = "yaomu:runtime:attach";
export const RUNTIME_DETACH_CHANNEL = "yaomu:runtime:detach";
export const RUNTIME_CHANGED_CHANNEL = "yaomu:runtime:changed";

export interface RuntimeBridgeError {
  code: "LOCAL_ACCESS_DENIED" | "CORE_NOT_READY";
  messageKey: "api.accessDenied" | "api.coreNotReady";
  retryable: boolean;
  actionIds: [];
}

export type RuntimeResult =
  | { ok: true; data: RuntimeSnapshot }
  | { ok: false; error: RuntimeBridgeError };

function denied(): RuntimeResult {
  return {
    ok: false,
    error: {
      code: "LOCAL_ACCESS_DENIED",
      messageKey: "api.accessDenied",
      retryable: false,
      actionIds: [],
    },
  };
}

interface Subscriber {
  sender: WebContents;
  refs: number;
}

/**
 * Main 侧 runtime 广播器。构造时订阅控制器快照；stop 后不再推送。
 * 同一窗口多个 preload 订阅按引用计数合并为一次广播。
 */
export class RuntimeBridge {
  private readonly controller: RuntimeController;
  private readonly authorization: DesktopAuthorization;
  private readonly subscribers = new Map<number, Subscriber>();
  private readonly unsubscribe: () => void;

  constructor(controller: RuntimeController, authorization: DesktopAuthorization) {
    this.controller = controller;
    this.authorization = authorization;
    this.unsubscribe = controller.subscribe((snapshot) => this.broadcast(snapshot));
  }

  /** invoke：授权在读取快照之前。 */
  handleGet(event: IpcSource): RuntimeResult {
    try {
      this.authorization.requireSender(event);
    } catch {
      return denied();
    }
    return { ok: true, data: this.controller.getSnapshot() };
  }

  /** invoke：授权在触发重试之前；未授权调用不产生任何启动副作用。 */
  handleRetry(event: IpcSource): RuntimeResult {
    try {
      this.authorization.requireSender(event);
    } catch {
      return denied();
    }
    return { ok: true, data: this.controller.retry() };
  }

  /** send：未授权时不登记、不回发。 */
  handleAttach(event: IpcSource): void {
    const authority = this.authorization.requireSender(event);
    const sender = authority.sender as WebContents;
    const existing = this.subscribers.get(sender.id);
    if (existing) {
      existing.refs += 1;
      return;
    }
    this.subscribers.set(sender.id, { sender, refs: 1 });
    // Main 的窗口销毁钩子也会释放；这里只作为 WebContents 直接销毁的兜底。
    if (typeof sender.once === "function") {
      sender.once("destroyed", () => {
        this.subscribers.delete(sender.id);
      });
    }
  }

  /** send：只允许同一受信顶层文档取消自己的引用。 */
  handleDetach(event: IpcSource): void {
    this.authorization.requireSender(event);
    this.release(event.sender);
  }

  /** 导航离开应用文档或窗口销毁时由 Main 调用，释放全部订阅。 */
  detachWindow(sender: WebContents): void {
    this.subscribers.delete(sender.id);
  }

  /** 推送当前快照；发送前再次校验来源，失效订阅立即移除。 */
  broadcast(snapshot: RuntimeSnapshot): void {
    for (const [id, subscriber] of this.subscribers) {
      try {
        if (
          subscriber.sender.isDestroyed() ||
          this.authorization.isTrustedContents(subscriber.sender) === false
        ) {
          this.subscribers.delete(id);
          continue;
        }
        subscriber.sender.send(RUNTIME_CHANGED_CHANNEL, snapshot);
      } catch {
        this.subscribers.delete(id);
      }
    }
  }

  stop(): void {
    this.subscribers.clear();
    this.unsubscribe();
  }

  private release(sender: { readonly id: number }): void {
    const subscriber = this.subscribers.get(sender.id);
    if (!subscriber) return;
    subscriber.refs -= 1;
    if (subscriber.refs <= 0) this.subscribers.delete(sender.id);
  }
}

/** 注册固定白名单通道；事件通道不向未受信来源回发错误。 */
export function registerRuntimeIpc(ipcMain: IpcMain, bridge: RuntimeBridge): void {
  ipcMain.handle(RUNTIME_GET_CHANNEL, (event) => bridge.handleGet(event));
  ipcMain.handle(RUNTIME_RETRY_CHANNEL, (event) => bridge.handleRetry(event));
  ipcMain.on(RUNTIME_ATTACH_CHANNEL, (event) => {
    try {
      bridge.handleAttach(event);
    } catch {
      // send 通道没有 Promise 错误回执；不登记订阅，也不回发错误。
    }
  });
  ipcMain.on(RUNTIME_DETACH_CHANNEL, (event) => {
    try {
      bridge.handleDetach(event);
    } catch {
      // 不允许子 frame 借同一 WebContents 取消顶层订阅。
    }
  });
}

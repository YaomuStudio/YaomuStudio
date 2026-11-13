/**
 * 桌面更新安装状态 IPC：有限快照读取/订阅、显式同意与等待取消。
 *
 * - 通道固定为 update:get-install-state / update:install / update:cancel-install（invoke）
 *   与 update:install-state:attach / detach（send）；update:install-state 只由 Main 推送。
 * - 每个 handler 在读取状态、启动同意或取消之前调用同一个 DesktopAuthorization.requireSender；
 *   未授权 invoke 返回固定安全信封，未授权 send 不登记、不回发。
 * - 广播前再次校验来源并复用 schema 校验；快照不含凭据、Core 身份字符串或路径。
 * - 本模块不加载 electron，可在普通 Node 测试中验证授权、订阅与广播边界。
 */
import type { IpcMain, WebContents } from "electron";
import type { DesktopAuthorization, IpcSource } from "./authorization";
import {
  isUpdateInstallSnapshot,
  type UpdateInstallController,
  type UpdateInstallSnapshot,
} from "../update-install";

export const UPDATE_INSTALL_STATE_CHANNEL = "update:install-state";
export const UPDATE_GET_INSTALL_STATE_CHANNEL = "update:get-install-state";
export const UPDATE_CANCEL_INSTALL_CHANNEL = "update:cancel-install";
export const UPDATE_INSTALL_STATE_ATTACH_CHANNEL = "update:install-state:attach";
export const UPDATE_INSTALL_STATE_DETACH_CHANNEL = "update:install-state:detach";

export interface UpdateInstallBridgeError {
  code: "UPDATE_INSTALL_UNAVAILABLE" | "UPDATE_INSTALL_ACCESS_DENIED";
  /** 固定安全文案；UI 只按 code 映射已注册消息，不展示任意异常文本。 */
  message: string;
}

export type UpdateInstallResult =
  | { ok: true; data: UpdateInstallSnapshot }
  | { ok: false; error: UpdateInstallBridgeError };

const ACCESS_DENIED_MESSAGE = "本地访问被拒绝";

function denied(): UpdateInstallResult {
  return {
    ok: false,
    error: { code: "UPDATE_INSTALL_ACCESS_DENIED", message: ACCESS_DENIED_MESSAGE },
  };
}

interface Subscriber {
  sender: WebContents;
  refs: number;
}

/**
 * Main 侧更新安装广播器。构造时订阅控制器快照；stop 后不再推送。
 * 同一窗口多个 preload 订阅按引用计数合并为一次广播。
 */
export class UpdateInstallBridge {
  private readonly controller: UpdateInstallController;
  private readonly authorization: DesktopAuthorization;
  private readonly subscribers = new Map<number, Subscriber>();
  private readonly unsubscribe: () => void;

  constructor(controller: UpdateInstallController, authorization: DesktopAuthorization) {
    this.controller = controller;
    this.authorization = authorization;
    this.unsubscribe = controller.subscribe((snapshot) => this.broadcast(snapshot));
  }

  /** invoke：授权先于读取快照。 */
  handleGet(event: IpcSource): UpdateInstallResult {
    try {
      this.authorization.requireSender(event);
    } catch {
      return denied();
    }
    return { ok: true, data: this.controller.getSnapshot() };
  }

  /** invoke：授权先于启动/加入显式同意。 */
  handleInstall(event: IpcSource): UpdateInstallResult {
    try {
      this.authorization.requireSender(event);
    } catch {
      return denied();
    }
    return { ok: true, data: this.controller.install() };
  }

  /** invoke：授权先于取消；只有 waiting 会改变状态。 */
  handleCancel(event: IpcSource): UpdateInstallResult {
    try {
      this.authorization.requireSender(event);
    } catch {
      return denied();
    }
    return { ok: true, data: this.controller.cancelInstall() };
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

  /** 推送当前快照；无效形状不发送，来源失效的订阅立即移除。 */
  broadcast(snapshot: UpdateInstallSnapshot): void {
    if (!isUpdateInstallSnapshot(snapshot)) return;
    for (const [id, subscriber] of this.subscribers) {
      try {
        if (
          subscriber.sender.isDestroyed() ||
          this.authorization.isTrustedContents(subscriber.sender) === false
        ) {
          this.subscribers.delete(id);
          continue;
        }
        subscriber.sender.send(UPDATE_INSTALL_STATE_CHANNEL, snapshot);
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

/** 注册固定白名单通道；send 通道拒绝时不回发错误。 */
export function registerUpdateInstallIpc(
  ipcMain: IpcMain,
  bridge: UpdateInstallBridge,
): void {
  ipcMain.handle(UPDATE_GET_INSTALL_STATE_CHANNEL, (event) => bridge.handleGet(event));
  ipcMain.handle("update:install", (event) => bridge.handleInstall(event));
  ipcMain.handle(UPDATE_CANCEL_INSTALL_CHANNEL, (event) => bridge.handleCancel(event));
  ipcMain.on(UPDATE_INSTALL_STATE_ATTACH_CHANNEL, (event) => {
    try {
      bridge.handleAttach(event);
    } catch {
      // send 通道没有 Promise 错误回执；不登记订阅，也不回发错误。
    }
  });
  ipcMain.on(UPDATE_INSTALL_STATE_DETACH_CHANNEL, (event) => {
    try {
      bridge.handleDetach(event);
    } catch {
      // 不允许子 frame 借同一 WebContents 取消顶层订阅。
    }
  });
}

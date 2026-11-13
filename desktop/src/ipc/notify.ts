/**
 * 系统通知 IPC。分类开关由 Renderer 的设置 store 决定；主进程只按请求发送。
 */
import { ipcMain, Notification } from "electron";
import { DesktopAuthorization } from "./authorization";

export interface NotifyPayload {
  title: string;
  body: string;
  category?: string;
}

export function registerNotificationIpc(authorization: DesktopAuthorization): void {
  ipcMain.handle("yaomu:notify", (event, payload: unknown) => {
    authorization.requireSender(event);
    if (typeof payload !== "object" || payload === null) throw new Error("通知参数不合法");
    const record = payload as Partial<NotifyPayload>;
    if (typeof record.title !== "string" || typeof record.body !== "string") {
      throw new Error("通知标题与正文必须是字符串");
    }
    if (record.category !== undefined && typeof record.category !== "string") {
      throw new Error("通知分类不合法");
    }
    if (!Notification.isSupported()) return;
    new Notification({
      title: record.title.slice(0, 200),
      body: record.body.slice(0, 1_000),
    }).show();
  });
}

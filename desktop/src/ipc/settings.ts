/**
 * 桌面设置 IPC。来源校验与值校验都在读写偏好之前完成；
 * 授权对象、读写回调由 Main 注入，本模块不持有业务状态。
 */
import type { IpcMain, WebContents } from "electron";
import { isCloseBehavior, type CloseBehavior } from "../preferences";
import type { DesktopAuthorization } from "./authorization";

export interface DesktopSettingsHandlers {
  getAutoCheckUpdates(): boolean;
  getAutoDownloadUpdates(): boolean;
  applyCloseBehavior(value: CloseBehavior): void;
  applyAutoCheckUpdates(value: boolean): void;
  applyAutoDownloadUpdates(value: boolean): void;
  applyUiScale(target: WebContents, value: number): void;
}

export function registerDesktopSettingsIpc(
  ipcMain: IpcMain,
  authorization: DesktopAuthorization,
  handlers: DesktopSettingsHandlers,
): void {
  // 白名单 IPC：只接受两个枚举值，非法值拒绝且不落盘。
  ipcMain.handle("yaomu:settings:set-close-behavior", (event, value: unknown) => {
    authorization.requireSender(event);
    if (!isCloseBehavior(value)) throw new Error("closeBehavior 不合法");
    handlers.applyCloseBehavior(value);
  });
  ipcMain.handle("yaomu:settings:set-auto-check-updates", (event, value: unknown) => {
    authorization.requireSender(event);
    if (typeof value !== "boolean") throw new Error("autoCheckUpdates 不合法");
    handlers.applyAutoCheckUpdates(value);
  });
  // 只读水合：Renderer 的 localStorage 没有持久值时以主进程为准，
  // 避免把界面默认值 true 写回覆盖用户已保存的 false。
  ipcMain.handle("yaomu:settings:get-auto-check-updates", (event) => {
    authorization.requireSender(event);
    return handlers.getAutoCheckUpdates();
  });
  ipcMain.handle("yaomu:settings:set-auto-download-updates", (event, value: unknown) => {
    authorization.requireSender(event);
    if (typeof value !== "boolean") throw new Error("autoDownloadUpdates 不合法");
    handlers.applyAutoDownloadUpdates(value);
  });
  // 只读水合：与自动检查相同，避免 Renderer 默认值覆盖主进程已保存的选择。
  ipcMain.handle("yaomu:settings:get-auto-download-updates", (event) => {
    authorization.requireSender(event);
    return handlers.getAutoDownloadUpdates();
  });
  // 界面缩放：只接受 FR-SET-43 的 80%–150%，由 Main 应用到发起窗口。
  // 使用 Chromium 页面缩放而不是 CSS zoom，媒体查询、fixed 定位和 100% 高度
  // 都按缩放后的真实视口计算，避免高缩放下布局被撑出窗口。
  ipcMain.handle("yaomu:settings:set-ui-scale", (event, value: unknown) => {
    authorization.requireSender(event);
    if (typeof value !== "number" || !Number.isFinite(value) || value < 0.8 || value > 1.5) {
      throw new Error("uiScale 不合法");
    }
    handlers.applyUiScale(event.sender, value);
  });
}

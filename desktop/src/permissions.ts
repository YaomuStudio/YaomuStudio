/**
 * Renderer Web 权限默认拒绝。
 *
 * 除受信顶层文档的显式 fullscreen 外全部拒绝：V1 没有麦克风/摄像头/地理位置/
 * 剪贴板/Web 通知需求；本地媒体经 yaomu-asset 协议播放，不申请媒体权限。
 * fullscreen 例外只保留产品既有 `<video controls>` 全屏能力，不放 automatic-fullscreen、
 * media 或其他权限；Main 原生 Notification 与文件对话框不经过 Web 权限。
 * Electron 要求 permission check 与 request 同时安装。
 */
import type { Session } from "electron";
import { isApplicationURL, type DesktopAuthorization, type SenderContents } from "./ipc/authorization";

type PermissionSession = Pick<Session, "setPermissionRequestHandler" | "setPermissionCheckHandler">;

interface PermissionDetails {
  readonly isMainFrame: boolean;
  readonly requestingUrl?: string;
}

/** 仅 permission==="fullscreen"、受信顶层文档且请求 URL 一致时允许；其余一律拒绝。 */
function isTrustedFullscreen(
  authorization: DesktopAuthorization,
  contents: SenderContents | null,
  permission: string,
  details: PermissionDetails | undefined,
): boolean {
  if (permission !== "fullscreen" || contents === null || details?.isMainFrame !== true) return false;
  const requestingUrl = details.requestingUrl;
  if (typeof requestingUrl !== "string" || requestingUrl.length === 0) return false;
  return authorization.isTrustedContents(contents) &&
    isApplicationURL(requestingUrl, contents.mainFrame.url);
}

export function defaultDenyRendererPermissions(
  authorization: DesktopAuthorization,
  target: PermissionSession,
): void {
  target.setPermissionRequestHandler((contents, permission, callback, details) => {
    callback(isTrustedFullscreen(authorization, contents, permission, details));
  });
  target.setPermissionCheckHandler((contents, permission, _requestingOrigin, details) => {
    return isTrustedFullscreen(authorization, contents, permission, details);
  });
}

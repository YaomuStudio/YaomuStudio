/**
 * 受控资产另存 IPC：系统保存对话框 + 主进程带实例令牌读取 Core 资产内容。
 *
 * Renderer 只传 assetId 与展示文案；目标路径只能来自保存对话框，不接受 URL/源路径/令牌。
 */
import { app, BrowserWindow, dialog, ipcMain } from "electron";
import fs from "node:fs";
import path from "node:path";
import type { CoreConnection } from "../bootstrap";
import { DesktopAuthorization } from "./authorization";
import {
  downloadAssetContent,
  sanitizeSuggestedName,
  validateAssetSaveInput,
} from "./asset-content";
import { resolveOutputsDir } from "../outputs";

export interface SavedAsset {
  path: string;
}

/** 资产另存的默认位置与任务产出一致：<程序目录>/outputs。 */
function defaultSavePath(fileName: string): string {
  const directory = resolveOutputsDir({
    packaged: app.isPackaged,
    appPath: app.getAppPath(),
    execPath: process.execPath,
  });
  try {
    fs.mkdirSync(directory, { recursive: true });
    return path.join(directory, fileName);
  } catch {
    // 程序目录不可写时仍允许用户另存，不用错误路径阻断下载。
    return fileName;
  }
}

export function registerAssetSaveIpc(
  getConnection: () => CoreConnection | null,
  authorization: DesktopAuthorization,
  fetchImpl: typeof fetch = fetch,
): void {
  ipcMain.handle(
    "yaomu:assets:save",
    async (event, raw: unknown): Promise<SavedAsset | null> => {
      const authority = authorization.requireSender(event);
      // 先校验参数，非法输入不打开对话框。
      const input = validateAssetSaveInput(raw);
      const connection = getConnection();
      if (!connection) throw new Error("CORE_NOT_READY");
      const win = BrowserWindow.fromWebContents(event.sender);
      if (!win) throw new Error("ASSET_SAVE_NO_WINDOW");

      const suggestedName = sanitizeSuggestedName(input.suggestedName ?? "", input.assetId);
      const result = await dialog.showSaveDialog(win, {
        title: input.title ?? "保存资产",
        defaultPath: defaultSavePath(suggestedName),
        properties: ["showOverwriteConfirmation", "createDirectory"],
      });
      authorization.assertCurrent(event, authority);
      if (result.canceled || !result.filePath) return null;

      await downloadAssetContent({
        connection,
        assetId: input.assetId,
        targetPath: result.filePath,
        fetchImpl,
      });
      authorization.assertCurrent(event, authority);
      authority.paths.grantOpenPath(result.filePath);
      return { path: result.filePath };
    },
  );
}

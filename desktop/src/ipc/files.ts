/**
 * 文件与打开路径的受控 IPC。
 *
 * - 选择器由用户在系统对话框里完成授权；Renderer 不能直接读任意路径。
 * - openPath 只接受同窗口授权路径或 Core 明确返回的项目/导出目录。
 */
import { app, BrowserWindow, dialog, ipcMain, shell } from "electron";
import fs from "node:fs";
import path from "node:path";
import { DesktopAuthorization } from "./authorization";
import {
  MAX_MEDIA_SCAN_FILES,
  scanMediaDirectory,
  type MediaScanResult,
} from "../media-scan";
import { resolveOutputsDir } from "../outputs";

export interface SelectFilesOptions {
  title?: string;
  filters?: { name: string; extensions: string[] }[];
}

export interface SelectDirectoryFilesOptions {
  title?: string;
  extensions?: string[];
  limit?: number;
}

function senderWindow(sender: Electron.WebContents): BrowserWindow {
  const win = BrowserWindow.fromWebContents(sender);
  if (!win) throw new Error("未知窗口");
  return win;
}

function cleanTitle(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const title = value.trim().slice(0, 120);
  return title.length > 0 ? title : undefined;
}

function cleanFilters(value: unknown): Electron.FileFilter[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const filters = value.flatMap((item) => {
    if (typeof item !== "object" || item === null) return [];
    const record = item as { name?: unknown; extensions?: unknown };
    if (typeof record.name !== "string" || !Array.isArray(record.extensions)) return [];
    const extensions = record.extensions.filter(
      (ext): ext is string => typeof ext === "string" && /^[A-Za-z0-9*]+$/.test(ext),
    );
    return extensions.length > 0 ? [{ name: record.name.slice(0, 80), extensions }] : [];
  });
  return filters.length > 0 ? filters : undefined;
}

/** 只保留字母数字扩展名（接受带点的写法）；无有效项时交给扫描器回退默认列表。 */
function cleanScanExtensions(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const extensions = value.flatMap((item) => {
    if (typeof item !== "string") return [];
    const extension = item.trim().replace(/^\.+/, "").toLowerCase();
    return /^[a-z0-9]+$/.test(extension) ? [extension] : [];
  });
  return extensions.length > 0 ? [...new Set(extensions)] : undefined;
}

/** 限制在 1..MAX_MEDIA_SCAN_FILES；非法值交给扫描器使用默认上限。 */
function cleanScanLimit(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  const limit = Math.floor(value);
  if (limit < 1) return undefined;
  return Math.min(limit, MAX_MEDIA_SCAN_FILES);
}

/** 固定默认输出目录：<程序目录>/outputs。主进程负责创建，Renderer 不传路径。 */
export function defaultOutputDir(): string {
  return resolveOutputsDir({
    packaged: app.isPackaged,
    appPath: app.getAppPath(),
    execPath: process.execPath,
  });
}

export interface SaveTextOptions {
  suggestedName?: string;
  text: string;
  title?: string;
  filters?: { name: string; extensions: string[] }[];
}

function cleanSaveTextName(value: unknown): string {
  const raw = typeof value === "string" ? path.basename(value.trim()) : "";
  const safe = raw.replace(/[\\/:*?"<>|\x00-\x1f]/g, "_").replace(/[. ]+$/g, "");
  const name = safe.slice(0, 120) || "subtitles.srt";
  return /\.[a-z0-9]+$/i.test(name) ? name : `${name}.srt`;
}

export function registerFileIpc(authorization: DesktopAuthorization): void {
  // 任务产出固定落盘目录：由主进程创建并授权，Renderer 只能读取这个路径，
  // 不能借目录选择器扩大范围。后续支持用户配置时仍由主进程解析后返回。
  ipcMain.handle("yaomu:files:default-output-dir", (event) => {
    const authority = authorization.requireSender(event);
    const directory = defaultOutputDir();
    try {
      fs.mkdirSync(directory, { recursive: true });
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      throw new Error(`无法创建默认输出目录 ${directory}：${detail}`);
    }
    return authority.paths.grantOutputDirectory(directory);
  });

  ipcMain.handle("yaomu:files:select-files", async (event, rawOptions: unknown) => {
    const authority = authorization.requireSender(event);
    const win = senderWindow(event.sender);
    const options = (rawOptions ?? {}) as SelectFilesOptions;
    const result = await dialog.showOpenDialog(win, {
      title: cleanTitle(options.title) ?? "选择媒体文件",
      properties: ["openFile", "multiSelections"],
      filters: cleanFilters(options.filters),
    });
    authorization.assertCurrent(event, authority);
    if (result.canceled) return [];
    return authority.paths.grantInputFiles(result.filePaths);
  });

  ipcMain.handle("yaomu:files:select-directory", async (event, rawOptions: unknown) => {
    const authority = authorization.requireSender(event);
    const win = senderWindow(event.sender);
    const options = (rawOptions ?? {}) as SelectFilesOptions;
    const result = await dialog.showOpenDialog(win, {
      title: cleanTitle(options.title) ?? "选择目录",
      properties: ["openDirectory", "createDirectory"],
    });
    authorization.assertCurrent(event, authority);
    if (result.canceled || result.filePaths.length === 0) return null;
    return authority.paths.grantOutputDirectory(result.filePaths[0]);
  });

  // 文件夹导入：系统目录选择器授权后，由主进程递归扫描并返回受支持文件；
  // Renderer 只接收已授权的绝对路径，不能自行构造目录内路径。
  ipcMain.handle(
    "yaomu:files:select-directory-files",
    async (event, rawOptions: unknown): Promise<MediaScanResult> => {
      const authority = authorization.requireSender(event);
      const win = senderWindow(event.sender);
      const options = (rawOptions ?? {}) as SelectDirectoryFilesOptions;
      const result = await dialog.showOpenDialog(win, {
        title: cleanTitle(options.title) ?? "选择媒体文件夹",
        properties: ["openDirectory"],
      });
      authorization.assertCurrent(event, authority);
      if (result.canceled || result.filePaths.length === 0) {
        return { directory: "", paths: [], total: 0, truncated: false, errors: 0 };
      }
      const scan = await scanMediaDirectory(result.filePaths[0], {
        extensions: cleanScanExtensions(options.extensions),
        limit: cleanScanLimit(options.limit),
      });
      authorization.assertCurrent(event, authority);
      authority.paths.grantInputFiles(scan.paths);
      try {
        authority.paths.grantOpenPath(scan.directory);
      } catch {
        // 扫描根已消失时保留扫描器的错误结果，不签发失效授权。
      }
      return scan;
    },
  );

  // 只有 preload 可调用此私有通道：路径来自 webUtils 解析的原生 File，
  // Renderer 桥不暴露接收 string 的授权方法。同步返回保留既有 getPathForFile 形状。
  ipcMain.on("yaomu:files:register-drop", (event, target: unknown) => {
    try {
      const authority = authorization.requireSender(event);
      event.returnValue = authority.paths.grantInputFiles([target as string])[0];
    } catch {
      event.returnValue = "";
    }
  });

  // 字幕等文本由主进程打开系统保存对话框；Renderer 只传文件名和内容，不传任意目标路径。
  ipcMain.handle("yaomu:files:save-text", async (event, raw: unknown) => {
    const authority = authorization.requireSender(event);
    const input = (raw ?? {}) as Partial<SaveTextOptions>;
    if (typeof input.text !== "string" || input.text.length === 0) {
      throw new Error("empty text");
    }
    if (Buffer.byteLength(input.text, "utf8") > 16 * 1024 * 1024) {
      throw new Error("text too large");
    }
    const win = BrowserWindow.fromWebContents(event.sender);
    if (!win) throw new Error("SAVE_TEXT_NO_WINDOW");
    const suggestedName = cleanSaveTextName(input.suggestedName);
    const result = await dialog.showSaveDialog(win, {
      title: cleanTitle(input.title) ?? "保存字幕",
      defaultPath: path.join(defaultOutputDir(), suggestedName),
      filters: cleanFilters(input.filters) ?? [{ name: "SRT", extensions: ["srt"] }],
      properties: ["showOverwriteConfirmation", "createDirectory"],
    });
    authorization.assertCurrent(event, authority);
    if (result.canceled || !result.filePath) return null;
    await fs.promises.writeFile(result.filePath, input.text, { encoding: "utf8" });
    authority.paths.grantOpenPath(result.filePath);
    return { path: result.filePath };
  });

  ipcMain.handle("yaomu:files:open-path", async (event, target: unknown) => {
    const authority = authorization.requireSender(event);
    const error = await shell.openPath(authority.paths.assertOpenPath(target));
    if (error) throw new Error(error);
  });
}

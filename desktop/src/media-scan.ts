/**
 * 文件夹导入的纯扫描逻辑（不依赖 Electron，可在 Node 下直接单测）。
 *
 * 目录授权由主进程的系统目录选择器完成；本模块只遍历用户已选择的目录：
 * - 跳过隐藏文件/目录与符号链接，避免跟进任意位置或循环；
 * - 按扩展名（大小写不敏感）过滤支持的媒体文件；
 * - 不可读子目录计入 errors 并继续，不中断整体扫描；
 * - 结果按绝对路径排序后截断到上限（默认且最多 128，与 Core 导入上限一致）。
 */
import { readdir } from "node:fs/promises";
import type { Dirent } from "node:fs";
import path from "node:path";

/** FR-IMP-02 支持的媒体扩展名（不带点，小写）。 */
export const DEFAULT_MEDIA_EXTENSIONS = [
  "mp4",
  "mov",
  "mkv",
  "webm",
  "wav",
  "mp3",
  "m4a",
  "flac",
  "aac",
] as const;

/** 单次文件夹扫描/导入的路径上限，与 Core 的 maxImportGrants 保持一致。 */
export const MAX_MEDIA_SCAN_FILES = 128;

export interface MediaScanOptions {
  /** 受控扩展名列表；空/非法时回退 DEFAULT_MEDIA_EXTENSIONS。 */
  extensions?: readonly string[];
  /** 返回路径上限；默认且最大为 MAX_MEDIA_SCAN_FILES。 */
  limit?: number;
}

export interface MediaScanResult {
  directory: string;
  paths: string[];
  /** 目录内匹配的文件总数（不受 limit 截断）。 */
  total: number;
  truncated: boolean;
  /** 不可读子目录数量；扫描继续，已跳过这些目录。 */
  errors: number;
}

/** 归一化扩展名：去点、小写、只保留字母数字；无有效项时回退默认媒体列表。 */
export function normalizeMediaExtensions(values?: readonly string[]): string[] {
  const cleaned = (values ?? [])
    .filter((value): value is string => typeof value === "string")
    .map((value) => value.trim().toLowerCase().replace(/^\.+/, ""))
    .filter((value) => /^[a-z0-9]+$/.test(value));
  if (cleaned.length === 0) return [...DEFAULT_MEDIA_EXTENSIONS];
  return [...new Set(cleaned)];
}

function normalizeLimit(value?: number): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return MAX_MEDIA_SCAN_FILES;
  }
  return Math.min(Math.floor(value), MAX_MEDIA_SCAN_FILES);
}

/** 递归扫描 root 下的受支持媒体文件；不跟随符号链接。 */
export async function scanMediaDirectory(
  root: string,
  options: MediaScanOptions = {},
): Promise<MediaScanResult> {
  const directory = path.resolve(root);
  const extensions = new Set(normalizeMediaExtensions(options.extensions));
  const limit = normalizeLimit(options.limit);

  const matches: string[] = [];
  let total = 0;
  let errors = 0;

  async function walk(current: string): Promise<void> {
    let entries: Dirent[];
    try {
      entries = await readdir(current, { withFileTypes: true });
    } catch {
      // 无权限/目录消失：计入错误并继续其他分支，不伪造成功。
      errors += 1;
      return;
    }
    for (const entry of entries) {
      if (entry.name.startsWith(".")) continue;
      if (entry.isSymbolicLink()) continue;
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        await walk(full);
        continue;
      }
      if (!entry.isFile()) continue;
      const extension = path.extname(entry.name).slice(1).toLowerCase();
      if (!extensions.has(extension)) continue;
      total += 1;
      matches.push(full);
    }
  }

  await walk(directory);
  matches.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  const paths = matches.slice(0, limit);
  return {
    directory,
    paths,
    total,
    truncated: total > paths.length,
    errors,
  };
}

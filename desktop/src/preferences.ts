/**
 * 桌面 UI 偏好文件（非业务数据）。
 *
 * 只保存影响桌面壳行为的最小设置：关闭窗口时“退出 / 最小化到托盘”、自动检查更新。
 * 该模块不依赖 Electron，便于 Node 单测；主进程负责传入 `<userData>` 下的具体路径。
 *
 * 读取策略：文件缺失、JSON 损坏或字段未知时一律回退默认值（`quit` / 自动检查开启），
 * 只返回状态供主进程记录日志，不阻断启动。
 * 写入策略：父目录按需创建，临时文件 + rename 原子替换，POSIX 权限 0600。
 */
import fs from "node:fs";
import path from "node:path";

export type CloseBehavior = "quit" | "minimize";

export interface DesktopPreferences {
  schemaVersion: 1;
  closeBehavior: CloseBehavior;
  /** 打包态启动/开关切换时是否自动检查更新；手动检查不受此开关限制。 */
  autoCheckUpdates: boolean;
  /** 发现新版本后是否后台下载；默认关闭，安装始终需要用户确认。 */
  autoDownloadUpdates: boolean;
}

export const DEFAULT_PREFERENCES: Readonly<DesktopPreferences> = Object.freeze({
  schemaVersion: 1,
  closeBehavior: "quit",
  autoCheckUpdates: true,
  autoDownloadUpdates: false,
});

/** `ok`：读取成功；其余状态均已回退默认值，仅用于日志区分原因。 */
export type PreferencesReadStatus = "ok" | "missing" | "corrupt" | "unknown";

export interface PreferencesReadResult {
  preferences: DesktopPreferences;
  status: PreferencesReadStatus;
}

const SCHEMA_VERSION = 1;

export function isCloseBehavior(value: unknown): value is CloseBehavior {
  return value === "quit" || value === "minimize";
}

function defaultPreferences(): DesktopPreferences {
  return { ...DEFAULT_PREFERENCES };
}

/**
 * 把磁盘上的 JSON 归一化为受支持形状。
 * `schema_version` 或 `close_behavior` 不是已知值时返回 `unknown`，避免猜测未来字段语义。
 * 旧版偏好文件可能没有 `auto_check_updates`；缺失或非布尔时按 `true` 处理，保持兼容。
 * `auto_download_updates` 缺失或非布尔时按 `false` 处理，保持“默认不自动下载”的新默认值。
 */
function normalizePreferences(value: unknown): PreferencesReadResult | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const record = value as {
    schema_version?: unknown;
    close_behavior?: unknown;
    auto_check_updates?: unknown;
    auto_download_updates?: unknown;
  };
  if (record.schema_version !== SCHEMA_VERSION) {
    return { preferences: defaultPreferences(), status: "unknown" };
  }
  if (!isCloseBehavior(record.close_behavior)) {
    return { preferences: defaultPreferences(), status: "unknown" };
  }
  const autoCheckUpdates =
    typeof record.auto_check_updates === "boolean" ? record.auto_check_updates : true;
  const autoDownloadUpdates =
    typeof record.auto_download_updates === "boolean" ? record.auto_download_updates : false;
  return {
    preferences: {
      schemaVersion: SCHEMA_VERSION,
      closeBehavior: record.close_behavior,
      autoCheckUpdates,
      autoDownloadUpdates,
    },
    status: "ok",
  };
}

/** 读取偏好并附带状态。任何 IO/格式问题都不抛错。 */
export function readPreferencesFile(filePath: string): PreferencesReadResult {
  let raw: string;
  try {
    raw = fs.readFileSync(filePath, "utf8");
  } catch {
    return { preferences: defaultPreferences(), status: "missing" };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { preferences: defaultPreferences(), status: "corrupt" };
  }

  return normalizePreferences(parsed) ?? { preferences: defaultPreferences(), status: "corrupt" };
}

/** 读取偏好；缺失/损坏/未知值回退默认 `quit`。 */
export function readPreferences(filePath: string): DesktopPreferences {
  return readPreferencesFile(filePath).preferences;
}

/**
 * 原子写入偏好。非法枚举/版本直接拒绝，避免把未知值落盘。
 * 失败时清理临时文件后抛出原始错误。
 */
export function writePreferences(filePath: string, preferences: DesktopPreferences): void {
  if (typeof preferences !== "object" || preferences === null) {
    throw new Error("DESKTOP_PREFERENCES_INVALID");
  }
  if (preferences.schemaVersion !== SCHEMA_VERSION) {
    throw new Error("DESKTOP_PREFERENCES_SCHEMA_UNSUPPORTED");
  }
  if (!isCloseBehavior(preferences.closeBehavior)) {
    throw new Error("DESKTOP_PREFERENCES_CLOSE_BEHAVIOR_INVALID");
  }
  if (typeof preferences.autoCheckUpdates !== "boolean") {
    throw new Error("DESKTOP_PREFERENCES_AUTO_CHECK_UPDATES_INVALID");
  }
  if (typeof preferences.autoDownloadUpdates !== "boolean") {
    throw new Error("DESKTOP_PREFERENCES_AUTO_DOWNLOAD_UPDATES_INVALID");
  }

  const serialized = `${JSON.stringify(
    {
      schema_version: SCHEMA_VERSION,
      close_behavior: preferences.closeBehavior,
      auto_check_updates: preferences.autoCheckUpdates,
      auto_download_updates: preferences.autoDownloadUpdates,
    },
    null,
    2,
  )}\n`;

  const directory = path.dirname(filePath);
  fs.mkdirSync(directory, { recursive: true });

  const tempPath = path.join(
    directory,
    `.${path.basename(filePath)}.${process.pid}.${Date.now()}.tmp`,
  );
  try {
    fs.writeFileSync(tempPath, serialized, { encoding: "utf8", mode: 0o600 });
    if (process.platform !== "win32") {
      // umask 可能影响创建权限；显式收敛到 0600，避免偏好被同机其他用户读取。
      fs.chmodSync(tempPath, 0o600);
    }
    fs.renameSync(tempPath, filePath);
  } catch (error) {
    try {
      fs.unlinkSync(tempPath);
    } catch {
      // 临时文件可能未创建或已被 rename；忽略清理失败。
    }
    throw error;
  }
}

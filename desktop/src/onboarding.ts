/**
 * 引导完成状态文件（`<userData>/onboarding.json`）。
 *
 * 权威状态由主进程持有：安装器在非自动更新安装时写入“未完成”，Renderer 完成引导后
 * 通过 IPC 写入“已完成”。该模块不依赖 Electron，便于 Node 单测；主进程负责传入路径。
 *
 * 读取策略：文件缺失、JSON 损坏、未知 schema 或 `completed` 非布尔时返回 `null`
 * （语义为“未设置”，调用方按未完成处理的安全方向），只附带状态供日志区分原因。
 * 写入策略：父目录按需创建，临时文件 + rename 原子替换，POSIX 权限 0600。
 */
import fs from "node:fs";
import path from "node:path";

export type OnboardingReadStatus = "ok" | "missing" | "corrupt" | "unknown";

export interface OnboardingReadResult {
  /** `null` 表示未设置或不可用；调用方一律按“未完成”处理。 */
  completed: boolean | null;
  status: OnboardingReadStatus;
}

export const ONBOARDING_SCHEMA_VERSION = 1;

function unset(status: OnboardingReadStatus): OnboardingReadResult {
  return { completed: null, status };
}

/** 读取引导状态并附带状态。任何 IO/格式问题都不抛错，也不会崩启动。 */
export function readOnboardingFile(filePath: string): OnboardingReadResult {
  let raw: string;
  try {
    raw = fs.readFileSync(filePath, "utf8");
  } catch {
    return unset("missing");
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return unset("corrupt");
  }

  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return unset("corrupt");
  }
  const record = parsed as { schema_version?: unknown; completed?: unknown };
  if (record.schema_version !== ONBOARDING_SCHEMA_VERSION) {
    // 未知 schema 不猜测未来字段语义，按未完成处理。
    return unset("unknown");
  }
  if (typeof record.completed !== "boolean") {
    return unset("corrupt");
  }
  return { completed: record.completed, status: "ok" };
}

/**
 * 原子写入引导状态。安装器写入 `completed:false`，Renderer 完成引导后写 `true`。
 * 失败时清理临时文件后抛出原始错误；调用方决定是否回滚界面状态。
 */
export function writeOnboardingCompleted(filePath: string, completed: boolean = true): void {
  if (typeof completed !== "boolean") {
    throw new Error("ONBOARDING_COMPLETED_INVALID");
  }

  const serialized = `${JSON.stringify(
    { schema_version: ONBOARDING_SCHEMA_VERSION, completed },
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
      // umask 可能影响创建权限；显式收敛到 0600，避免状态文件被同机其他用户改动。
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

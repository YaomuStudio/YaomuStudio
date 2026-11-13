/**
 * 非 Windows 打包态的宿主运行时投影。
 *
 * 只读取应用自有 resources/core 下的随包 Runner/媒体工具；不探测仓库、
 * 不下载、不修改 PATH。Linux 额外 best-effort 探测宿主 FlashCLI/mlstools，
 * macOS 只启用媒体链路。
 */
import fs from "node:fs";
import path from "node:path";

export type HostRuntimeReason =
  | "not-packaged"
  | "windows"
  | "runner-invalid"
  | "python-missing"
  | "ffmpeg-missing"
  | "ffprobe-missing"
  | "ok";

export interface HostRuntimeInput {
  platform: NodeJS.Platform;
  packaged: boolean;
  coreDir: string;
  runnerValidated: boolean;
  env?: NodeJS.ProcessEnv;
}

export interface HostRuntimeResult {
  env: NodeJS.ProcessEnv;
  reason: HostRuntimeReason;
}

function isExecutable(file: string): boolean {
  try {
    if (!fs.statSync(file).isFile()) return false;
    fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function isExecutableInside(file: string, rootDir: string): boolean {
  try {
    const realRoot = fs.realpathSync(rootDir);
    const realFile = fs.realpathSync(file);
    if (realFile !== realRoot && !realFile.startsWith(realRoot + path.sep)) return false;
    return isExecutable(realFile);
  } catch {
    return false;
  }
}

function existingEnv(env: NodeJS.ProcessEnv, key: string): string | undefined {
  const value = env[key]?.trim();
  return value ? value : undefined;
}

function findOnPath(env: NodeJS.ProcessEnv, name: string): string | null {
  const pathValue = env.PATH ?? env.Path ?? "";
  for (const entry of pathValue.split(path.delimiter)) {
    if (!entry || !path.isAbsolute(entry)) continue;
    const candidate = path.join(entry, name);
    if (isExecutable(candidate)) return candidate;
  }
  return null;
}

function resolveTool(env: NodeJS.ProcessEnv, envKey: string, name: string): string | null {
  const explicit = existingEnv(env, envKey);
  if (explicit) {
    return path.isAbsolute(explicit) && isExecutable(explicit) ? explicit : null;
  }
  return findOnPath(env, name);
}

/**
 * 返回应注入 Core 的宿主运行时环境。显式 process.env 覆盖优先：
 * 已设置的键不被随包值覆盖。
 */
export function resolveHostRuntimeEnv(input: HostRuntimeInput): HostRuntimeResult {
  const env = input.env ?? process.env;
  if (!input.packaged) return { env: {}, reason: "not-packaged" };
  if (input.platform === "win32") return { env: {}, reason: "windows" };
  if (!input.runnerValidated) return { env: {}, reason: "runner-invalid" };

  const launcher = path.join(input.coreDir, "yaomu-runner");
  const python = path.join(input.coreDir, "python", "bin", "python3");
  const ffmpeg = path.join(input.coreDir, "ffmpeg");
  const ffprobe = path.join(input.coreDir, "ffprobe");

  if (!isExecutableInside(launcher, input.coreDir)) {
    return { env: {}, reason: "runner-invalid" };
  }
  if (!isExecutableInside(python, input.coreDir)) {
    return { env: {}, reason: "python-missing" };
  }
  if (!isExecutableInside(ffmpeg, input.coreDir)) {
    return { env: {}, reason: "ffmpeg-missing" };
  }
  if (!isExecutableInside(ffprobe, input.coreDir)) {
    return { env: {}, reason: "ffprobe-missing" };
  }

  const injected: NodeJS.ProcessEnv = {};
  if (!existingEnv(env, "YAOMU_RUNNER_BIN")) injected.YAOMU_RUNNER_BIN = launcher;
  if (!existingEnv(env, "FFMPEG_BIN")) injected.FFMPEG_BIN = ffmpeg;
  if (!existingEnv(env, "FFPROBE_BIN")) injected.FFPROBE_BIN = ffprobe;

  // Linux 模型执行 best-effort：显式路径优先，其次 PATH；不下载、不猜参数。
  if (input.platform === "linux") {
    const flashcli = resolveTool(env, "FLASHCLI_BIN", "flashcli");
    if (flashcli) {
      if (!existingEnv(env, "FLASHCLI_BIN")) injected.FLASHCLI_BIN = flashcli;
      if (!existingEnv(env, "YAOMU_CAPABILITY_EXECUTOR")) {
        injected.YAOMU_CAPABILITY_EXECUTOR = "flashcli";
      }
    }
    const mlstools = resolveTool(env, "MLSTOOLS_BIN", "mlstools");
    if (mlstools && !existingEnv(env, "MLSTOOLS_BIN")) injected.MLSTOOLS_BIN = mlstools;
  }

  return { env: injected, reason: "ok" };
}

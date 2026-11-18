import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
export const YAOMU_WSL_DISTRO = "yaomu";

export interface WslMaintenanceOptions {
  userDataDir: string;
  wslExe?: string;
  distro?: string;
  timeoutMs?: number;
  exec?: (file: string, args: string[], options: { timeout: number }) => Promise<{ stdout: string }>;
}

export interface WslMaintenanceResult {
  removed: boolean;
  reason?: "not_installed";
}

function userDataWslDir(userDataDir: string): string {
  const root = path.resolve(userDataDir);
  const target = path.resolve(root, "wsl");
  const relative = path.relative(root, target);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new Error("wsl data path escapes userData");
  }
  return target;
}

/**
 * 只清理应用自有的 WSL 发行版与镜像目录；调用方必须已经停止 Core 并确认无活动任务。
 * 不删除 Windows WSL 功能、其他发行版或用户文件。
 */
export async function cleanYaomuWslRuntime(
  options: WslMaintenanceOptions,
): Promise<WslMaintenanceResult> {
  const wslExe = options.wslExe ?? "wsl.exe";
  const distro = options.distro ?? YAOMU_WSL_DISTRO;
  if (distro !== YAOMU_WSL_DISTRO) throw new Error("refusing to unregister a non-app WSL distro");
  const exec = options.exec ?? ((file, args, execOptions) => execFileAsync(file, args, execOptions));
  const timeout = options.timeoutMs ?? 60_000;
  const listed = await exec(wslExe, ["-l", "-q"], { timeout });
  const names = listed.stdout
    .replace(/\0/g, "")
    .split(/\r?\n/)
    .map((name) => name.trim())
    .filter(Boolean);
  if (!names.includes(distro)) {
    await fs.rm(userDataWslDir(options.userDataDir), { recursive: true, force: true });
    return { removed: false, reason: "not_installed" };
  }
  await exec(wslExe, ["--unregister", distro], { timeout });
  await fs.rm(userDataWslDir(options.userDataDir), { recursive: true, force: true });
  return { removed: true };
}

#!/usr/bin/env node
/**
 * Cross-platform entry for `npm run pack:dir`.
 *
 * Windows 的 PATH 里 `bash` 常指向 WSL（Ubuntu 未启动时会挂起）。这里显式
 * 解析 Git for Windows 的 bash，并把它的 bin 放到子进程 PATH 最前，保证
 * pack-dir.sh 内部再次调用 `bash scripts/build-core.sh` 时不会切到 WSL。
 *
 * 可用 YAOMU_BASH 指定解释器；找不到 Git Bash 时直接报错，不回退到 WSL。
 */
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const script = path.join(root, "scripts", "pack-dir.sh");

function findBash() {
  const override = process.env.YAOMU_BASH?.trim();
  if (override) {
    if (!existsSync(override)) throw new Error(`YAOMU_BASH 不存在: ${override}`);
    return override;
  }
  if (process.platform !== "win32") {
    return "bash";
  }
  const candidates = [
    path.join(process.env.ProgramFiles ?? "C:\\Program Files", "Git", "bin", "bash.exe"),
    path.join(process.env.ProgramW6432 ?? "C:\\Program Files", "Git", "bin", "bash.exe"),
    path.join(process.env["ProgramFiles(x86)"] ?? "C:\\Program Files (x86)", "Git", "bin", "bash.exe"),
    path.join(process.env.LOCALAPPDATA ?? "", "Programs", "Git", "bin", "bash.exe"),
  ];
  for (const candidate of candidates) {
    if (candidate && existsSync(candidate)) return candidate;
  }
  throw new Error(
    "找不到 Git for Windows 的 bash。请安装 Git for Windows，或设置 YAOMU_BASH 指向 bash.exe；" +
      "不会回退到 PATH 中的 WSL bash。",
  );
}

let bash;
try {
  bash = findBash();
} catch (error) {
  console.error(`[pack:dir] ${error.message}`);
  process.exit(1);
}

if (process.argv.includes("--check")) {
  console.log(JSON.stringify({ platform: process.platform, bash, script }, null, 2));
  process.exit(0);
}

const env = { ...process.env };
if (process.platform === "win32") {
  env.PATH = `${path.dirname(bash)}${path.delimiter}${env.Path ?? env.PATH ?? ""}`;
  env.Path = env.PATH;
}

console.log(`[pack:dir] bash: ${bash}`);
const result = spawnSync(bash, [script], { cwd: root, stdio: "inherit", env, shell: false });
if (result.error) {
  console.error(`[pack:dir] 启动失败: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);

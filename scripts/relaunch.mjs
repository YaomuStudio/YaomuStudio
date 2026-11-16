#!/usr/bin/env node
/**
 * 一键重拉开发态桌面客户端：停掉本仓 electron → 构建 desktop/ui → electron .
 * 不启 Vite；UI 改动需再跑本命令（或改用 VITE_DEV_SERVER_URL 热重载流程）。
 * 不碰打包态 YaomuStudio.app。
 *
 * 用法：npm run relaunch
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const electronBin = path.join(root, "node_modules", ".bin", "electron");
const electronApp = path.join(
  root,
  "node_modules",
  "electron",
  "dist",
  "Electron.app",
);

function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function killDevElectron() {
  if (process.platform === "win32") {
    const escaped = root.replace(/'/g, "''");
    spawnSync(
      "powershell.exe",
      [
        "-NoProfile",
        "-Command",
        [
          "Get-CimInstance Win32_Process |",
          "Where-Object {",
          "  $_.Name -match '^(electron|Electron)\\.exe$' -and",
          `  $_.CommandLine -like '*${escaped}*'`,
          "} |",
          "ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }",
        ].join(" "),
      ],
      { stdio: "ignore" },
    );
    return;
  }
  spawnSync("pkill", ["-f", electronBin], { stdio: "ignore" });
  spawnSync("pkill", ["-f", electronApp], { stdio: "ignore" });
}

function runNpm(args) {
  const result = spawnSync("npm", args, {
    cwd: root,
    stdio: "inherit",
    shell: process.platform === "win32",
    env: process.env,
  });
  if (result.error) {
    console.error(result.error);
    process.exit(1);
  }
  if (result.status) process.exit(result.status);
}

console.log("[relaunch] stopping workspace Electron…");
killDevElectron();
sleep(1000);

console.log("[relaunch] building desktop…");
runNpm(["run", "build", "-w", "desktop"]);

console.log("[relaunch] building ui…");
runNpm(["run", "build:ui"]);

console.log("[relaunch] starting electron…");
runNpm(["run", "dev", "-w", "desktop"]);

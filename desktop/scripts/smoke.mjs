#!/usr/bin/env node
/**
 * 曜幕后台冒烟（绝不显示窗口）。
 *
 * 1. 构建 Go Core 到临时目录（构建失败时退回 go run 包装脚本，并在输出注明）；
 * 2. 构建 ui/ 与 desktop/（SMOKE_SKIP_BUILD=1 可跳过）；
 * 3. 以 YAOMU_SMOKE=ready 启动 Electron（show:false，隔离 userData）：
 *    断言桥可用、Core 就绪、真实项目出现在项目页与工作台，截图到 /tmp/yaomu-smoke/；
 * 4. 以 YAOMU_SMOKE=no-core 启动：断言 CORE_NOT_READY 可读错误、不白屏；
 * 5. 两个场景都通过才退出 0。
 *
 * 用法：node desktop/scripts/smoke.mjs
 * 环境：SMOKE_SKIP_BUILD=1 跳过前端构建；SMOKE_TIMEOUT_MS 调整单场景超时；
 *       SMOKE_ELECTRON_ARGS 追加测试专用 Electron 参数（如容器内 --no-sandbox），
 *       仅用于验证环境，不进入生产启动路径。
 */
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const desktopDir = path.join(repoRoot, "desktop");
const appServerDir = path.join(repoRoot, "app-server");
const electronBin = path.join(
  repoRoot,
  "node_modules",
  ".bin",
  process.platform === "win32" ? "electron.cmd" : "electron",
);
const timeoutMs = Number(process.env.SMOKE_TIMEOUT_MS ?? 120_000);

/**
 * 测试专用额外 Electron 参数（按空白拆分），追加在 `electron .` 之后。
 * 未设置时返回空数组，启动参数与改动前完全一致；不得用于生产启动。
 */
function extraElectronArgs() {
  return (process.env.SMOKE_ELECTRON_ARGS ?? "").split(/\s+/).filter(Boolean);
}

function log(message) {
  console.log(`[smoke] ${message}`);
}

function fail(message) {
  console.error(`[smoke] FAIL: ${message}`);
  process.exit(1);
}

function runSync(command, args, options) {
  const result = spawnSync(command, args, {
    ...options,
    stdio: "inherit",
    shell: process.platform === "win32",
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} exited with ${result.status ?? "signal"}`);
  }
}

function buildCore() {
  const suffix = process.platform === "win32" ? ".exe" : "";
  const coreBin = path.join(os.tmpdir(), `yaomu-smoke-core-${process.pid}${suffix}`);
  log(`building Core -> ${coreBin}`);
  try {
    runSync("go", ["build", "-o", coreBin, "./cmd/server"], { cwd: appServerDir });
    return { command: coreBin, note: undefined };
  } catch (error) {
    if (process.platform === "win32") {
      fail(`go build failed and go run fallback is not supported on Windows: ${error.message}`);
    }
    // 构建失败时退回 go run 包装脚本（注明在输出里，不伪造结果）。
    const wrapper = path.join(os.tmpdir(), `yaomu-smoke-core-${process.pid}.sh`);
    fs.writeFileSync(
      wrapper,
      `#!/bin/sh\ncd ${JSON.stringify(appServerDir)} && exec go run ./cmd/server\n`,
      { mode: 0o755 },
    );
    log(`go build failed (${error.message}); falling back to go run wrapper: ${wrapper}`);
    return { command: wrapper, note: "CORE_LAUNCHED_WITH_GO_RUN" };
  }
}

function runElectron(mode, env) {
  return new Promise((resolve, reject) => {
    const extraArgs = extraElectronArgs();
    log(`launching Electron (${mode}, hidden)${extraArgs.length ? ` args: ${extraArgs.join(" ")}` : ""}`);
    const child = spawn(electronBin, [".", ...extraArgs], {
      cwd: desktopDir,
      env: { ...process.env, ...env },
      stdio: ["ignore", "pipe", "pipe"],
      detached: process.platform !== "win32",
      // Windows 下 .bin/electron.cmd 需要经 shell 启动。
      shell: process.platform === "win32",
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      log(`${mode}: timeout after ${timeoutMs}ms, terminating process tree`);
      killTree(child);
      resolve({ code: null, stdout, stderr, timedOut: true });
    }, timeoutMs);

    child.stdout?.on("data", (chunk) => {
      stdout += chunk.toString();
      for (const line of chunk.toString().split(/\r?\n/)) {
        if (line.trim()) log(`[${mode}:stdout] ${line}`);
      }
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk.toString();
      for (const line of chunk.toString().split(/\r?\n/)) {
        if (line.trim()) log(`[${mode}:stderr] ${line}`);
      }
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("exit", (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal, stdout, stderr, timedOut: false });
    });
  });
}

function killTree(child) {
  if (child.pid === undefined) return;
  if (process.platform === "win32") {
    spawn("taskkill", ["/PID", String(child.pid), "/T", "/F"], { stdio: "ignore" });
    return;
  }
  try {
    process.kill(-child.pid, "SIGKILL");
  } catch {
    try {
      child.kill("SIGKILL");
    } catch {
      // 已退出
    }
  }
}

function parseResult(stdout, mode) {
  const lines = stdout
    .split(/\r?\n/)
    .filter((line) => line.includes("SMOKE_RESULT "));
  if (lines.length === 0) return undefined;
  const last = lines[lines.length - 1];
  try {
    return JSON.parse(last.slice(last.indexOf("SMOKE_RESULT ") + "SMOKE_RESULT ".length));
  } catch {
    return undefined;
  }
}

function report(mode, result, run) {
  if (!result) {
    console.error(`[smoke] ${mode}: no SMOKE_RESULT emitted (exit=${run.code ?? "timeout"})`);
    return false;
  }
  console.log(`[smoke] ${mode}: ${result.ok ? "PASS" : "FAIL"}`);
  for (const check of result.checks ?? []) {
    console.log(`[smoke]   ${check.ok ? "ok  " : "FAIL"} ${check.name}${check.detail ? ` — ${check.detail}` : ""}`);
  }
  if (result.consoleErrors?.length) {
    console.log(`[smoke]   console errors: ${result.consoleErrors.length}`);
    for (const message of result.consoleErrors.slice(0, 10)) {
      console.log(`[smoke]     ${message}`);
    }
  }
  if (result.securityProbes) {
    console.log(`[smoke]   security probes: ${JSON.stringify(result.securityProbes)}`);
  }
  if (result.screenshot) console.log(`[smoke]   screenshot: ${result.screenshot}`);
  if (result.pipelineScreenshot) console.log(`[smoke]   pipeline screenshot: ${result.pipelineScreenshot}`);
  if (result.pdf) console.log(`[smoke]   pdf fallback: ${result.pdf}`);
  if (result.captureError) console.log(`[smoke]   capture note: ${result.captureError}`);
  if (run.timedOut) console.error(`[smoke] ${mode}: timed out`);
  return result.ok === true && run.code === 0 && !run.timedOut;
}

async function main() {
  if (!fs.existsSync(electronBin)) {
    fail(`Electron binary not found at ${electronBin}; run npm install first`);
  }

  const core = buildCore();
  if (core.note) log(`note: ${core.note}`);

  if (process.env.SMOKE_SKIP_BUILD !== "1") {
    log("building ui + desktop");
    runSync("npm", ["run", "build"], { cwd: repoRoot });
  }

  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), "yaomu-smoke-"));
  const readyData = path.join(tempRoot, "data-ready");
  const readyUser = path.join(tempRoot, "user-ready");
  const noCoreData = path.join(tempRoot, "data-no-core");
  const noCoreUser = path.join(tempRoot, "user-no-core");
  for (const dir of [readyData, readyUser, noCoreData, noCoreUser]) {
    fs.mkdirSync(dir, { recursive: true });
  }
  log(`temp root: ${tempRoot}`);

  const readyRun = await runElectron("ready", {
    YAOMU_CORE_BIN: core.command,
    YAOMU_DATA_DIR: readyData,
    YAOMU_SMOKE: "ready",
    YAOMU_SMOKE_USER_DATA: readyUser,
    // 测试夹具：让 Core 把 translate 视为已注入工具，从而可创建
    // translate-only 的 custom Run 来产生真实交付门槛。仅字符串校验，
    // 不执行该路径；只在冒烟进程内注入，不进入生产启动路径。
    YAOMU_WSL_MLSTOOLS_BIN: "/usr/bin/true",
  });
  const readyResult = parseResult(readyRun.stdout, "ready");
  const readyPass = report("ready", readyResult, readyRun);

  const noCoreRun = await runElectron("no-core", {
    YAOMU_DATA_DIR: noCoreData,
    YAOMU_SMOKE: "no-core",
    YAOMU_SMOKE_USER_DATA: noCoreUser,
  });
  const noCoreResult = parseResult(noCoreRun.stdout, "no-core");
  const noCorePass = report("no-core", noCoreResult, noCoreRun);

  if (readyPass && noCorePass) {
    log("SMOKE PASS");
    process.exit(0);
  }
  log("SMOKE FAIL");
  process.exit(1);
}

main().catch((error) => {
  fail(error instanceof Error ? error.stack ?? error.message : String(error));
});

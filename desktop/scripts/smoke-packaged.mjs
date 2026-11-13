#!/usr/bin/env node
/**
 * 打包产物后台冒烟（绝不显示窗口）。
 *
 * 直接运行 `electron-builder --dir` 产出的 App 二进制（不经过 npm/electron 开发态），
 * 复用 desktop/src/smoke.ts 的断言，并强制验证 Runner 来源：
 * - 任一选定应用（含 --app）都必须解析到包内 resources/core；缺失即失败，不跳过。
 * - 用生产校验模块检查 runner_src 与 runner-manifest.json（版本/逐文件 SHA-256）。
 * - Windows：使用生产 resolveWslRunnerEnv 生成 argv，执行真实 WSL hello，
 *   并要求应用返回**同次** Core 的 `core.runner_ready`（runner=model）证据。
 * - 非 Windows：明确报告 WSL not-run；只做资源/控制面与宿主启动器兼容验证，
 *   不能代替 Windows 验收。
 * - hello/应用 transport 都有输出上限、截止与自有退出确认；超时、溢出、非法帧、
 *   清理未确认或 Core 停止失败都不能 PASS。
 *
 * 用法：node desktop/scripts/smoke-packaged.mjs [--app <可执行文件路径>]
 * 前置：npm run pack:dir（产物在 desktop/dist-app/），且 desktop/dist 已构建。
 * 环境：SMOKE_ELECTRON_ARGS 追加测试专用启动参数（如容器内 --no-sandbox）；
 *       SMOKE_TIMEOUT_MS 调整单个应用场景上限（默认 240000，含 WSL 冷启动预算）。
 * macOS 上如提示“无法打开”，确认产物未被 Gatekeeper 隔离；本地构建不做签名。
 */
import fs from "node:fs";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import {
  describeRunFacts,
  formatMissingResult,
  isScenarioPass,
  parseSmokeResult,
  runAppScenario,
  runScenarioSequence,
} from "./smoke-packaged-suite.mjs";
import {
  cleanSourceEnv,
  resolvePackagedArtifacts,
  runWslHello,
  verifyPackagedLayout,
} from "./smoke-packaged-support.mjs";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const desktopDir = path.join(repoRoot, "desktop");
const outDir = path.join(desktopDir, "dist-app");
const timeoutMs = Number(process.env.SMOKE_TIMEOUT_MS ?? 240_000);
/** 应用/hello transport 保留输出上限；超限即失败，不把截断内容当证据。 */
const APP_STDOUT_LIMIT = 4 * 1024 * 1024;
const APP_STDERR_LIMIT = 1024 * 1024;
const require = createRequire(import.meta.url);

/**
 * 测试专用额外启动参数（按空白拆分），追加在打包二进制之后。
 * 未设置时返回空数组，启动参数与改动前完全一致；不得用于生产启动。
 */
function extraElectronArgs() {
  return (process.env.SMOKE_ELECTRON_ARGS ?? "").split(/\s+/).filter(Boolean);
}

function log(message) {
  console.log(`[smoke-packaged] ${message}`);
}

function fail(message) {
  console.error(`[smoke-packaged] FAIL: ${message}`);
  process.exit(1);
}

/** 加载生产资源校验与 WSL argv 投影；缺失即失败，不回退到脚本内复制逻辑。 */
function loadProductionModules() {
  try {
    return {
      runnerResources: require("../dist/runner-resources.js"),
      wsl: require("../dist/wsl.js"),
    };
  } catch (error) {
    fail(
      `无法加载 desktop/dist 生产模块（${error instanceof Error ? error.message : String(error)}）；` +
        "先运行 npm run build -w desktop",
    );
  }
}

/**
 * 解析打包产物：显式 --app 也必须携带同级 resources。
 * 找不到即失败，绝不跳过资源/hello 检查。
 */
function resolveArtifacts(explicit) {
  try {
    return resolvePackagedArtifacts({ explicit, outDir });
  } catch (error) {
    fail(error instanceof Error ? error.message : String(error));
  }
}

/**
 * 运行一次应用场景：隔离 data/userData、中立 cwd、清来源覆盖。
 * 原始 stdout/stderr 不再逐 chunk 转发；只由 suite 保留有界内容并记录固定摘要。
 */
async function runScenario(executable, mode, cwd) {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), `yaomu-packaged-${mode}-`));
  const dataDir = path.join(tempRoot, "data");
  const userData = path.join(tempRoot, "user");
  fs.mkdirSync(dataDir, { recursive: true });
  fs.mkdirSync(userData, { recursive: true });

  // 严格冒烟使用中立来源环境：清掉已知开发/外部/演示覆盖（大小写不敏感），
  // 只保留明确选择的镜像/解释器配置。不打印清理后的环境。
  const env = cleanSourceEnv(process.env);
  env.YAOMU_SMOKE = mode;
  env.YAOMU_SMOKE_USER_DATA = userData;
  env.YAOMU_DATA_DIR = dataDir;

  log(`launching packaged app (${mode}, hidden)`);
  const extraArgs = extraElectronArgs();
  if (extraArgs.length) log(`extra args: ${extraArgs.join(" ")}`);
  const run = await runAppScenario({
    executable,
    args: extraArgs,
    cwd,
    env,
    timeoutMs,
    stdoutLimit: APP_STDOUT_LIMIT,
    stderrLimit: APP_STDERR_LIMIT,
    log: (line) => log(`[${mode}] ${line}`),
  });
  log(`${mode}: ${describeRunFacts(run)}`);
  return run;
}

function report(mode, result, run, options = {}) {
  if (!result) {
    // 缺 SMOKE_RESULT 也必须保留超时/溢出/清理未确认事实，不能提前吞掉。
    console.error(`[smoke-packaged] ${formatMissingResult(mode, run)}`);
    return false;
  }
  console.log(`[smoke-packaged] ${mode}: ${result.ok ? "PASS" : "FAIL"}`);
  for (const check of result.checks ?? []) {
    console.log(
      `[smoke-packaged]   ${check.ok ? "ok  " : "FAIL"} ${check.name}${check.detail ? ` — ${check.detail}` : ""}`,
    );
  }
  const consoleErrors = result.consoleErrors ?? [];
  console.log(`[smoke-packaged]   console errors: ${consoleErrors.length}`);
  for (const message of consoleErrors.slice(0, 10)) {
    console.log(`[smoke-packaged]     ${message}`);
  }
  if (result.securityProbes) {
    console.log(`[smoke-packaged]   security probes: ${JSON.stringify(result.securityProbes)}`);
  }

  let evidencePass = true;
  const evidence = result.coreRunnerEvidence;
  if (options.requireCoreEvidence) {
    evidencePass =
      evidence?.required === true &&
      evidence?.ok === true &&
      evidence?.source === "bundled" &&
      typeof options.expectedRunner?.runnerVersion === "string" &&
      evidence?.runnerVersion === options.expectedRunner.runnerVersion &&
      evidence?.manifestDigest === options.expectedRunner.manifestDigest;
    console.log(
      `[smoke-packaged]   core runner evidence: ${evidencePass ? "PASS" : "FAIL"}` +
        ` (source=${evidence?.source ?? "missing"}, detail=${evidence?.detail ?? "missing"})`,
    );
  } else if (evidence) {
    console.log(
      `[smoke-packaged]   core runner evidence: not-run (${evidence.detail}); ` +
        "does not satisfy Windows acceptance",
    );
  }

  if (result.screenshot) console.log(`[smoke-packaged]   screenshot: ${result.screenshot}`);
  if (result.pipelineScreenshot) {
    console.log(`[smoke-packaged]   pipeline screenshot: ${result.pipelineScreenshot}`);
  }
  if (result.pdf) console.log(`[smoke-packaged]   pdf fallback: ${result.pdf}`);
  if (result.captureError) console.log(`[smoke-packaged]   capture note: ${result.captureError}`);
  if (run.timedOut) console.error(`[smoke-packaged] ${mode}: timed out`);
  if (run.outputOverflow) console.error(`[smoke-packaged] ${mode}: output exceeded retention limit`);
  if (run.exitUnconfirmed) console.error(`[smoke-packaged] ${mode}: owned process exit was not confirmed`);
  if (run.liveError) console.error(`[smoke-packaged] ${mode}: live child error observed`);
  if (run.closed !== true) console.error(`[smoke-packaged] ${mode}: process close was not confirmed`);
  return isScenarioPass(result, run) && evidencePass;
}

/** 校验随包资源布局；返回生产清单校验所需的 core 目录。 */
function verifyResourceLayout(resourcesDir) {
  let layout;
  try {
    layout = verifyPackagedLayout(resourcesDir);
  } catch (error) {
    fail(error instanceof Error ? error.message : String(error));
  }
  log(`packaged core: ${layout.coreBinary}`);
  log(`packaged runner compatibility launcher: ${layout.runner}`);
  return layout;
}

/**
 * 独立于 Electron 的宿主启动器兼容 hello（非 Windows 资源/控制面检查）。
 * 与 WSL hello 共用有界 transport/退出确认；宿主结果不能当作 Windows WSL 证据。
 */
async function verifyRunnerHello(runnerPath, expectedVersion, cwd) {
  const coreDir = path.dirname(runnerPath);
  const names =
    process.platform === "win32"
      ? { ffmpeg: "ffmpeg.exe", ffprobe: "ffprobe.exe" }
      : { ffmpeg: "ffmpeg", ffprobe: "ffprobe" };
  const env = cleanSourceEnv(process.env);
  const ffmpeg = path.join(coreDir, names.ffmpeg);
  const ffprobe = path.join(coreDir, names.ffprobe);
  if (!env.FFMPEG_BIN && fs.existsSync(ffmpeg)) env.FFMPEG_BIN = ffmpeg;
  if (!env.FFPROBE_BIN && fs.existsSync(ffprobe)) env.FFPROBE_BIN = ffprobe;

  return runWslHello({
    command: runnerPath,
    args: [],
    env,
    cwd,
    expectedVersion,
    timeoutMs: 30_000,
    // 兼容性断言：镜像/宿主未提供 FlashCLI 时模型槽位必须显式不可用，
    // 而不是伪装 READY；这是资源/控制面检查，不是模型执行。
    verifyAdapters: (data) => {
      const adapters = Array.isArray(data.adapters) ? data.adapters : [];
      const ffmpegAdapter = adapters.find((item) => item?.id === "ffmpeg");
      const flashcliAdapter = adapters.find((item) => item?.id === "flashcli");
      const ok =
        !!ffmpegAdapter &&
        flashcliAdapter?.ready === false &&
        flashcliAdapter?.reason === "MODEL_RUNTIME_UNAVAILABLE";
      return { ok, detail: ok ? "hello-ok" : "host-adapter-check-failed" };
    },
  });
}

/**
 * Windows 严格证据：生产 resolveWslRunnerEnv 只使用已验证包内描述，
 * 生成的 argv 源码路径必须与包内清单对应，且包含 -B/-P/-s/-m 固定模块入口。
 */
function verifyWindowsProjection({ wsl, descriptor, cleanEnv }) {
  const projection = wsl.resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env: cleanEnv,
    bundledRunner: descriptor,
  });
  const command = projection?.YAOMU_WSL_RUNNER_COMMAND;
  const argsRaw = projection?.YAOMU_WSL_RUNNER_ARGS;
  if (typeof command !== "string" || !command || typeof argsRaw !== "string") {
    return { ok: false, detail: "no-projected-command" };
  }
  let args;
  try {
    args = JSON.parse(argsRaw);
  } catch {
    return { ok: false, detail: "invalid-projected-args" };
  }
  if (!Array.isArray(args) || args.some((arg) => typeof arg !== "string")) {
    return { ok: false, detail: "invalid-projected-args" };
  }
  const expectedSource = `PYTHONPATH=${wsl.windowsPathToWslMount(descriptor.sourceDir)}`;
  const pythonPathArgs = args.filter((arg) => arg.startsWith("PYTHONPATH="));
  if (pythonPathArgs.length !== 1 || pythonPathArgs[0] !== expectedSource) {
    return { ok: false, detail: "wsl-argv-source-mismatch" };
  }
  const tail = ["-B", "-P", "-s", "-m", "yaomustudio_runner"];
  if (args.slice(-tail.length).join("\u0000") !== tail.join("\u0000")) {
    return { ok: false, detail: "wsl-argv-module-flags-mismatch" };
  }
  return { ok: true, command, args };
}

async function main() {
  const index = process.argv.indexOf("--app");
  const explicit = index >= 0 ? process.argv[index + 1] : undefined;
  if (index >= 0 && !explicit) fail("--app 缺少可执行文件路径");

  const { runnerResources, wsl } = loadProductionModules();
  const artifacts = resolveArtifacts(explicit);
  log(`app executable: ${artifacts.executable}`);
  log(`app resources: ${artifacts.resourcesDir}`);

  // 任一选定应用都必须通过真实包内资源校验；缺失/损坏即失败。
  const layout = verifyResourceLayout(artifacts.resourcesDir);
  const validation = await runnerResources.validateBundledRunnerResources({
    coreDir: layout.coreDir,
  });
  if (!validation.ok) {
    fail(`包内 Runner 资源校验失败：${validation.reason} (${validation.detail})`);
  }
  const descriptor = validation.descriptor;
  log(
    `packaged runner resources: version=${descriptor.runnerVersion} ` +
      `manifest=${descriptor.manifestDigest.slice(0, 12)}`,
  );

  // 中立 cwd：不继承调用目录，避免相对配置或同名模块影响来源判断。
  const neutralCwd = fs.mkdtempSync(path.join(os.tmpdir(), "yaomu-packaged-cwd-"));
  log(`neutral cwd: ${neutralCwd}`);

  let runnerPass = true;
  let helloCleanupUnconfirmed = false;
  if (process.platform === "win32") {
    const cleanEnv = cleanSourceEnv(process.env);
    const projection = verifyWindowsProjection({ wsl, descriptor, cleanEnv });
    if (!projection.ok) fail(`生产 WSL argv 校验失败：${projection.detail}`);
    const hello = await runWslHello({
      command: projection.command,
      args: projection.args,
      env: cleanEnv,
      cwd: neutralCwd,
      expectedVersion: descriptor.runnerVersion,
      // WSL 冷启动/退出可能比宿主慢；仍是有界预算，不做全局 shutdown。
      stopGraceMs: 10_000,
    });
    log(`wsl runner hello: ${hello.ok ? "PASS" : "FAIL"}${hello.detail ? ` — ${hello.detail}` : ""}`);
    runnerPass = hello.ok;
    helloCleanupUnconfirmed = hello.detail === "cleanup-unconfirmed";
  } else {
    const hello = await verifyRunnerHello(
      layout.runner,
      descriptor.runnerVersion,
      neutralCwd,
    );
    log(`runner hello: ${hello.ok ? "PASS" : "FAIL"}${hello.detail ? ` — ${hello.detail}` : ""}`);
    log(
      "WSL runner: not-run (non-Windows packaged check); " +
        "this run covers resources/control plane only and does not satisfy Windows acceptance",
    );
    runnerPass = hello.ok;
    helloCleanupUnconfirmed = hello.detail === "cleanup-unconfirmed";
  }

  // hello transport 清理未确认：立即结束整轮，不再启动应用或其他 transport。
  if (helloCleanupUnconfirmed) {
    log("runner hello cleanup unconfirmed — aborting packaged smoke without further transports");
    log("SMOKE FAIL");
    process.exit(1);
  }

  const sequence = await runScenarioSequence(
    () => runScenario(artifacts.executable, "ready", neutralCwd),
    () => runScenario(artifacts.executable, "no-core", neutralCwd),
  );
  const readyRun = sequence.readyRun;
  const readyPass = report("ready", parseSmokeResult(readyRun.stdout), readyRun, {
    requireCoreEvidence: process.platform === "win32",
    expectedRunner: descriptor,
  });

  let noCorePass = false;
  if (sequence.noCoreRun) {
    noCorePass = report(
      "no-core",
      parseSmokeResult(sequence.noCoreRun.stdout),
      sequence.noCoreRun,
    );
  } else {
    log(
      sequence.stopReason === "exit-unconfirmed"
        ? "no-core: not-run — previous application owned exit was not confirmed; " +
            "no further transports launched"
        : "no-core: not-run — previous application close/stdio was not confirmed " +
            "(exit may be known); no further transports launched",
    );
  }

  if (readyPass && noCorePass && runnerPass) {
    log("SMOKE PASS");
    if (process.platform === "win32") {
      log("Windows packaged WSL evidence: resources + real hello + same-startup Core runner=model");
    } else {
      log("Non-Windows coverage only: WSL execution not run");
    }
    process.exit(0);
  }
  log("SMOKE FAIL");
  process.exit(1);
}

main().catch((error) => {
  fail(error instanceof Error ? (error.stack ?? error.message) : String(error));
});

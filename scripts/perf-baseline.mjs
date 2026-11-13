#!/usr/bin/env node
/**
 * 非模型性能/离线/稳定性基线（macOS arm64 优先）。
 *
 * 只测量打包态应用壳与 Core 的本地行为，不测量模型/GPU/Windows：
 * - 首次启动（onboarding）与写入已引导标记后的工作台冷启动耗时；
 * - 工作台可见后 60 秒内的进程树 RSS（main/renderer/gpu/utility/core/other）；
 * - 应用进程树的非回环连接观察（lsof 采样，短连接可能漏采）；
 * - 可选 Core 本地回环请求延迟（直接拉起包内 Core，隔离数据目录）；
 * - 重复运行 desktop/scripts/smoke.mjs 的启动稳定性（不是崩溃率）。
 *
 * 用法：
 *   node scripts/perf-baseline.mjs [--app <可执行文件>] [--runs 3] [--idle-seconds 60]
 *     [--stability-runs 10] [--core-latency 50] [--json <path>] [--keep-temp]
 *
 * 前置：bash scripts/pack-dir.sh（产物在 desktop/dist-app/）。
 * 约束：不修改产品代码与网络设置；不触碰真实用户数据目录；仅使用隔离临时目录。
 *
 * 结论边界：本脚本输出“当前环境基线”，不判定 NFR-01/02/04/05 达标。
 */
import { execFileSync, spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { _electron as electron } from "@playwright/test";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const distAppDir = path.join(repoRoot, "desktop", "dist-app");
const workbenchPredicate = () =>
  document.readyState === "complete" &&
  location.hash !== "#/onboarding" &&
  !!document.querySelector(".wb-stats") &&
  !!document.querySelector("h1.page__title");

const LOOPBACK_RE = /^(127\.\d+\.\d+\.\d+|::1|localhost|\[::1\])(:.*)?$/i;

function log(message) {
  console.log(`[perf] ${message}`);
}

/** 去除报告/JSON 中的本机绝对路径（本地控制台日志不强制）。 */
function redact(text) {
  if (!text) return text;
  return String(text)
    .split(repoRoot + path.sep)
    .join("<repo>/")
    .split(repoRoot)
    .join("<repo>")
    .split(os.tmpdir())
    .join("<tmp>")
    .replace(/\/Users\/[^/\s]+/g, "<home>");
}

function fail(message) {
  console.error(`[perf] FAIL: ${message}`);
  process.exit(1);
}

function parseArgs(argv) {
  const options = {
    app: undefined,
    runs: 3,
    idleSeconds: 60,
    stabilityRuns: 10,
    coreLatency: 50,
    json: path.join(repoRoot, ".design-build", "perf", "perf-baseline.json"),
    keepTemp: false,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    const next = () => {
      const value = argv[index + 1];
      if (value === undefined) fail(`${arg} 缺少参数`);
      index += 1;
      return value;
    };
    switch (arg) {
      case "--app":
        options.app = path.resolve(next());
        break;
      case "--runs":
        options.runs = Number(next());
        break;
      case "--idle-seconds":
        options.idleSeconds = Number(next());
        break;
      case "--stability-runs":
        options.stabilityRuns = Number(next());
        break;
      case "--core-latency":
        options.coreLatency = Number(next());
        break;
      case "--json":
        options.json = path.resolve(next());
        break;
      case "--keep-temp":
        options.keepTemp = true;
        break;
      case "--help":
        console.log(
          "用法: node scripts/perf-baseline.mjs [--app <exe>] [--runs 3] [--idle-seconds 60] " +
            "[--stability-runs 10] [--core-latency 50] [--json <path>] [--keep-temp]",
        );
        process.exit(0);
        break;
      default:
        fail(`未知参数: ${arg}`);
    }
  }
  if (!Number.isFinite(options.runs) || options.runs < 1) fail("--runs 必须 >= 1");
  if (!Number.isFinite(options.idleSeconds) || options.idleSeconds < 1) {
    fail("--idle-seconds 必须 >= 1");
  }
  if (!Number.isFinite(options.stabilityRuns) || options.stabilityRuns < 0) {
    fail("--stability-runs 必须 >= 0");
  }
  if (!Number.isFinite(options.coreLatency) || options.coreLatency < 0) {
    fail("--core-latency 必须 >= 0");
  }
  return options;
}

function tryExec(command, args, options = {}) {
  try {
    return execFileSync(command, args, {
      encoding: "utf8",
      maxBuffer: 16 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
      ...options,
    }).trim();
  } catch {
    return undefined;
  }
}

function tryExecWithStderr(command, args) {
  try {
    const result = spawnSync(command, args, { encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
    return `${result.stdout ?? ""}${result.stderr ?? ""}`.trim();
  } catch {
    return undefined;
  }
}

/** 解析打包产物：macOS .app / Linux *-unpacked。 */
function resolveAppArtifact(explicit) {
  if (explicit) {
    if (!fs.existsSync(explicit)) fail(`--app 不存在: ${explicit}`);
    return explicit;
  }
  if (!fs.existsSync(distAppDir)) {
    fail(`未找到打包产物 ${distAppDir}；先运行 bash scripts/pack-dir.sh`);
  }
  const findFirst = (dir, predicate, maxDepth) => {
    const visit = (current, depth) => {
      let entries;
      try {
        entries = fs.readdirSync(current, { withFileTypes: true });
      } catch {
        return undefined;
      }
      for (const entry of entries) {
        if (predicate(entry)) return path.join(current, entry.name);
      }
      if (depth >= maxDepth) return undefined;
      for (const entry of entries) {
        if (!entry.isDirectory() || entry.name.endsWith(".app")) continue;
        const found = visit(path.join(current, entry.name), depth + 1);
        if (found) return found;
      }
      return undefined;
    };
    return visit(dir, 0);
  };

  if (process.platform === "darwin") {
    const appDir = findFirst(distAppDir, (entry) => entry.isDirectory() && entry.name.endsWith(".app"), 2);
    if (!appDir) fail(`在 ${distAppDir} 下找不到 .app 产物`);
    const macosDir = path.join(appDir, "Contents", "MacOS");
    const entries = fs.readdirSync(macosDir, { withFileTypes: true }).filter((entry) => entry.isFile());
    if (entries.length === 0) fail(`在 ${macosDir} 下找不到可执行文件`);
    return path.join(macosDir, entries[0].name);
  }

  const unpacked = findFirst(
    distAppDir,
    (entry) => entry.isDirectory() && /-unpacked$/.test(entry.name),
    1,
  );
  if (!unpacked) fail(`在 ${distAppDir} 下找不到 *-unpacked 产物`);
  const candidates = fs
    .readdirSync(unpacked, { withFileTypes: true })
    .filter((entry) => entry.isFile() && !/uninstall/i.test(entry.name))
    .map((entry) => {
      const full = path.join(unpacked, entry.name);
      try {
        fs.accessSync(full, fs.constants.X_OK);
        return { full, size: fs.statSync(full).size };
      } catch {
        return undefined;
      }
    })
    .filter(Boolean)
    .sort((a, b) => b.size - a.size);
  if (candidates.length === 0) fail(`在 ${unpacked} 下找不到可执行文件`);
  return candidates[0].full;
}

function resolveCoreBinary(appExecutable) {
  if (process.platform === "darwin") {
    const appDir = appExecutable.replace(/\/Contents\/MacOS\/[^/]+$/, "");
    return path.join(appDir, "Contents", "Resources", "core", "yaomu-core");
  }
  return path.join(path.dirname(appExecutable), "resources", "core", "yaomu-core");
}

/** 进程树：基于一次 ps 快照，按 ppid 递推。 */
function processTree(rootPid) {
  const raw = tryExec("ps", ["-Ao", "pid=,ppid=,rss=,command="]);
  if (!raw) return [];
  const rows = [];
  for (const line of raw.split(/\r?\n/)) {
    const match = line.match(/^\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/);
    if (!match) continue;
    rows.push({
      pid: Number(match[1]),
      ppid: Number(match[2]),
      rssBytes: Number(match[3]) * 1024,
      command: match[4],
    });
  }
  const byPid = new Map(rows.map((row) => [row.pid, row]));
  const children = new Map();
  for (const row of rows) {
    const list = children.get(row.ppid) ?? [];
    list.push(row.pid);
    children.set(row.ppid, list);
  }
  const tree = [];
  const queue = [rootPid];
  const seen = new Set();
  while (queue.length > 0) {
    const pid = queue.shift();
    if (seen.has(pid)) continue;
    seen.add(pid);
    const row = byPid.get(pid);
    if (row) tree.push(row);
    for (const child of children.get(pid) ?? []) queue.push(child);
  }
  return tree;
}

function classifyProcess(row, rootPid) {
  if (row.pid === rootPid) return "main";
  if (/--type=renderer/.test(row.command)) return "renderer";
  if (/--type=gpu-process/.test(row.command)) return "gpu";
  if (/--type=utility/.test(row.command)) return "utility";
  if (/yaomu-core(\.exe)?(\s|$)/.test(row.command)) return "core";
  return "other";
}

function summarizeSamples(samples, key) {
  const values = samples.map((sample) => sample[key]).filter((value) => Number.isFinite(value));
  if (values.length === 0) return { median: null, peak: null, min: null };
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  const median = sorted.length % 2 === 0 ? Math.round((sorted[mid - 1] + sorted[mid]) / 2) : sorted[mid];
  return { median, peak: sorted[sorted.length - 1], min: sorted[0] };
}

/** lsof 机器可读输出：p<pid> / c<command> / n<address>。 */
function listSockets(pids) {
  if (pids.length === 0) return { sockets: [], listen: [] };
  const pidList = pids.join(",");
  const parse = (raw) => {
    if (!raw) return [];
    const records = [];
    let current = { pid: undefined, command: undefined, addresses: [] };
    for (const line of raw.split(/\r?\n/)) {
      if (!line) continue;
      const field = line[0];
      const value = line.slice(1);
      if (field === "p") {
        if (current.pid !== undefined) records.push(current);
        current = { pid: Number(value), command: undefined, addresses: [] };
      } else if (field === "c") {
        current.command = value;
      } else if (field === "n") {
        current.addresses.push(value);
      }
    }
    if (current.pid !== undefined) records.push(current);
    return records;
  };

  const all = parse(tryExec("lsof", ["-nP", "-i", "-F", "pcn", "-a", "-p", pidList]));
  const listenRaw = tryExec("lsof", ["-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn", "-a", "-p", pidList]);
  const listen = parse(listenRaw);
  return {
    sockets: all,
    listen,
  };
}

function isLoopbackAddress(address) {
  const endpoints = String(address).split("->");
  return endpoints.every((endpoint) => {
    const value = endpoint.trim();
    if (value === "*" || value === "*:*") return false; // 监听所有接口，按非回环暴露处理
    return LOOPBACK_RE.test(value) || LOOPBACK_RE.test(value.replace(/:\d+$/, ""));
  });
}

/** 非回环连接（含监听所有接口），按 pid/address 去重。 */
function nonLoopbackSockets(sockets) {
  const found = new Map();
  for (const record of sockets) {
    for (const address of record.addresses) {
      if (isLoopbackAddress(address)) continue;
      const key = `${record.pid}|${address}`;
      if (!found.has(key)) {
        found.set(key, { pid: record.pid, command: record.command, address });
      }
    }
  }
  return [...found.values()];
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function killTree(rootPid) {
  if (!Number.isFinite(rootPid)) return;
  const tree = processTree(rootPid);
  for (const row of tree.reverse()) {
    try {
      process.kill(row.pid, "SIGKILL");
    } catch {
      // 已退出
    }
  }
}

async function closeElectronApp(app) {
  const pid = app.process()?.pid;
  try {
    await Promise.race([
      app.close(),
      delay(15_000).then(() => {
        throw new Error("Electron close timeout");
      }),
    ]);
  } catch {
    if (pid !== undefined) killTree(pid);
  }
}

function waitForWorkbench(window, timeoutMs = 60_000) {
  return window.waitForFunction(workbenchPredicate, null, { timeout: timeoutMs, polling: 100 });
}

function readWorkbenchTitle(window) {
  return window
    .evaluate(() => document.querySelector("h1.page__title")?.textContent?.trim() ?? "")
    .catch(() => "");
}

function launchApp(executable, env, args) {
  return electron.launch({
    executablePath: executable,
    args,
    env,
    timeout: 120_000,
  });
}

async function captureDiagnostics(app) {
  const chunks = { stdout: [], stderr: [] };
  const child = app.process();
  child?.stdout?.on("data", (chunk) => {
    chunks.stdout.push(chunk.toString());
    if (chunks.stdout.length > 50) chunks.stdout.shift();
  });
  child?.stderr?.on("data", (chunk) => {
    chunks.stderr.push(chunk.toString());
    if (chunks.stderr.length > 50) chunks.stderr.shift();
  });
  return chunks;
}

function tempDirListing(root) {
  try {
    return fs
      .readdirSync(root)
      .sort()
      .slice(0, 20);
  } catch {
    return [];
  }
}

async function seedProfile(executable, env, args, tempRoot) {
  log("first launch: waiting for onboarding");
  const startedAt = performance.now();
  const app = await launchApp(executable, env, args);
  const diagnostics = await captureDiagnostics(app);
  try {
    const window = await app.firstWindow({ timeout: 120_000 });
    await window.waitForFunction(
      () =>
        document.readyState === "complete" &&
        !!document.querySelector("#app") &&
        document.querySelector("#app").children.length > 0 &&
        location.hash.includes("onboarding"),
      null,
      { timeout: 60_000, polling: 100 },
    );
    const onboardingVisibleMs = Math.round(performance.now() - startedAt);
    log(`onboarding visible in ${onboardingVisibleMs}ms`);

    await window.evaluate(() => {
      localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: true, locale: "zh-CN" }));
    });
    const reloadStartedAt = performance.now();
    await window.reload();
    await waitForWorkbench(window);
    const seededWorkbenchMs = Math.round(performance.now() - reloadStartedAt);
    log(`seeded in-session workbench visible in ${seededWorkbenchMs}ms`);
    return {
      onboardingVisibleMs,
      seededWorkbenchMs,
      workbenchTitle: await readWorkbenchTitle(window),
      userDataEntries: tempDirListing(path.join(tempRoot, "user")),
      diagnostics: redact(diagnostics.stderr.join("").slice(-2_000)) || undefined,
    };
  } finally {
    await closeElectronApp(app);
  }
}

async function measureRun(executable, env, args, index, options) {
  log(`cold start run ${index + 1}/${options.runs}`);
  const startedAt = performance.now();
  const app = await launchApp(executable, env, args);
  const diagnostics = await captureDiagnostics(app);
  const mainPid = app.process()?.pid;
  if (!Number.isFinite(mainPid)) {
    throw new Error("无法获取 Electron 主进程 PID，拒绝记录空内存样本");
  }
  try {
    const window = await app.firstWindow({ timeout: 120_000 });
    await waitForWorkbench(window);
    const coldStartMs = Math.round(performance.now() - startedAt);
    const title = await readWorkbenchTitle(window);
    log(`run ${index + 1}: workbench in ${coldStartMs}ms (title=${JSON.stringify(title)})`);

    // 让 Core 与首屏渲染稳定，再开始空闲采样。
    await delay(2_000);

    const samples = [];
    const networkSamples = [];
    const sampleCount = 5;
    const intervalMs = Math.max(1_000, Math.floor((options.idleSeconds * 1000) / (sampleCount - 1)));
    for (let sampleIndex = 0; sampleIndex < sampleCount; sampleIndex += 1) {
      const elapsedSec = Number(((sampleIndex * intervalMs) / 1000).toFixed(1));
      const tree = processTree(mainPid);
      if (sampleIndex === 0 && tree.length === 0) {
        throw new Error("ps 进程树为空，拒绝记录零值内存样本");
      }
      const classes = { main: 0, renderer: 0, gpu: 0, utility: 0, core: 0, other: 0 };
      const processes = tree.map((row) => {
        const kind = classifyProcess(row, mainPid);
        classes[kind] += row.rssBytes;
        return { pid: row.pid, kind, rssBytes: row.rssBytes, command: row.command.slice(0, 160) };
      });
      const electronTotal =
        classes.main + classes.renderer + classes.gpu + classes.utility;
      const total = Object.values(classes).reduce((sum, value) => sum + value, 0);
      samples.push({
        elapsedSec,
        ...classes,
        electronTotal,
        total,
        processCount: tree.length,
        processes,
      });

      if (sampleIndex === 0 || sampleIndex === 2) {
        const pids = tree.map((row) => row.pid);
        const sockets = listSockets(pids);
        networkSamples.push({
          elapsedSec,
          sampledPids: pids.length,
          nonLoopback: nonLoopbackSockets(sockets.sockets),
          listening: nonLoopbackSockets(sockets.listen),
          listenAddresses: sockets.listen.flatMap((record) =>
            record.addresses.map((address) => ({ pid: record.pid, command: record.command, address })),
          ),
        });
      }
      if (sampleIndex < sampleCount - 1) await delay(intervalMs);
    }

    const memory = {
      samples,
      medians: {
        main: summarizeSamples(samples, "main").median,
        renderer: summarizeSamples(samples, "renderer").median,
        gpu: summarizeSamples(samples, "gpu").median,
        utility: summarizeSamples(samples, "utility").median,
        core: summarizeSamples(samples, "core").median,
        other: summarizeSamples(samples, "other").median,
        electronTotal: summarizeSamples(samples, "electronTotal").median,
        total: summarizeSamples(samples, "total").median,
      },
      peaks: {
        main: summarizeSamples(samples, "main").peak,
        renderer: summarizeSamples(samples, "renderer").peak,
        gpu: summarizeSamples(samples, "gpu").peak,
        utility: summarizeSamples(samples, "utility").peak,
        core: summarizeSamples(samples, "core").peak,
        other: summarizeSamples(samples, "other").peak,
        electronTotal: summarizeSamples(samples, "electronTotal").peak,
        total: summarizeSamples(samples, "total").peak,
      },
      totalProcessPeak: Math.max(...samples.map((sample) => sample.processCount)),
    };

    return {
      index,
      coldStartMs,
      workbenchTitle: title,
      memory,
      network: {
        samples: networkSamples,
        note: "lsof 采样只覆盖采样瞬间；短连接可能漏采。监听 * 视为非回环暴露。",
      },
      diagnostics: redact(diagnostics.stderr.join("").slice(-2_000)) || undefined,
    };
  } finally {
    await closeElectronApp(app);
  }
}

function findFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (address === null || typeof address === "string") {
        server.close(() => reject(new Error("无法分配回环端口")));
        return;
      }
      server.close(() => resolve(address.port));
    });
  });
}

async function measureCoreLatency(coreBinary, dataDir, count) {
  if (count === 0 || !fs.existsSync(coreBinary)) {
    return { skipped: true, reason: fs.existsSync(coreBinary) ? "count=0" : "core binary not found" };
  }
  log(`core latency: ${count} requests against packaged Core`);
  fs.mkdirSync(dataDir, { recursive: true });
  const port = await findFreePort();
  const token = `perf-${process.pid}-${Date.now()}`;
  const startedAt = performance.now();
  const child = spawn(coreBinary, [], {
    env: {
      ...process.env,
      YAOMU_DATA_DIR: dataDir,
      YAOMU_CORE_PORT: String(port),
      YAOMU_INSTANCE_TOKEN: token,
    },
    stdio: ["ignore", "pipe", "pipe"],
    detached: process.platform !== "win32",
  });
  const stderr = [];
  // Core 可能向 stdout 打日志；持续排空，避免管道写满阻塞。
  child.stdout?.on("data", () => undefined);
  child.stderr?.on("data", (chunk) => {
    stderr.push(chunk.toString());
    if (stderr.length > 40) stderr.shift();
  });
  const baseUrl = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 30_000;
  let ready = false;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) break;
    try {
      const response = await fetch(`${baseUrl}/health/live`, {
        headers: { Accept: "application/json", "X-YaoMu-Token": token },
        signal: AbortSignal.timeout(2_000),
      });
      if (response.ok) {
        const body = await response.json();
        if (body.status === "ok") {
          ready = true;
          break;
        }
      }
    } catch {
      // 继续等待
    }
    await delay(250);
  }
  if (!ready) {
    try {
      process.kill(-child.pid, "SIGKILL");
    } catch {
      try {
        child.kill("SIGKILL");
      } catch {
        // 已退出
      }
    }
    return {
      skipped: false,
      ok: false,
      error: "Core 未在 30s 内就绪",
      stderr: stderr.join("").slice(-2_000),
    };
  }
  const readyMs = Math.round(performance.now() - startedAt);

  const measure = async (url) => {
    const values = [];
    for (let index = 0; index < count; index += 1) {
      const requestStartedAt = performance.now();
      const response = await fetch(url, {
        headers: { Accept: "application/json", "X-YaoMu-Token": token },
        signal: AbortSignal.timeout(10_000),
      });
      await response.arrayBuffer();
      values.push(performance.now() - requestStartedAt);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
    }
    values.sort((a, b) => a - b);
    const percentile = (p) => values[Math.min(values.length - 1, Math.floor((p / 100) * values.length))];
    return {
      count: values.length,
      medianMs: Number(percentile(50).toFixed(2)),
      p95Ms: Number(percentile(95).toFixed(2)),
      minMs: Number(values[0].toFixed(2)),
      maxMs: Number(values[values.length - 1].toFixed(2)),
    };
  };

  try {
    let health;
    try {
      health = await measure(`${baseUrl}/health/live`);
    } catch (error) {
      return {
        skipped: false,
        ok: false,
        readyMs,
        error: error instanceof Error ? error.message : String(error),
      };
    }
    let projects;
    try {
      projects = await measure(`${baseUrl}/api/v1/projects`);
    } catch (error) {
      projects = { error: error instanceof Error ? error.message : String(error) };
    }
    return { skipped: false, ok: true, readyMs, health, projects };
  } finally {
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
}

function runSmokeOnce() {
  const startedAt = performance.now();
  const result = spawnSync("node", ["desktop/scripts/smoke.mjs"], {
    cwd: repoRoot,
    env: { ...process.env, SMOKE_SKIP_BUILD: "1" },
    encoding: "utf8",
    timeout: 240_000,
    maxBuffer: 32 * 1024 * 1024,
  });
  const durationMs = Math.round(performance.now() - startedAt);
  const output = `${result.stdout ?? ""}${result.stderr ?? ""}`;
  return {
    exitCode: result.status,
    signal: result.signal ?? undefined,
    durationMs,
    pass: result.status === 0 && output.includes("SMOKE PASS"),
    tail: output
      .split(/\r?\n/)
      .filter(Boolean)
      .slice(-6)
      .map((line) => redact(line)),
  };
}

async function measureStability(runs) {
  const results = [];
  for (let index = 0; index < runs; index += 1) {
    log(`stability run ${index + 1}/${runs} (SMOKE_SKIP_BUILD=1 smoke.mjs)`);
    const run = runSmokeOnce();
    log(`  exit=${String(run.exitCode)} pass=${run.pass} duration=${run.durationMs}ms`);
    results.push({ index, ...run });
  }
  const durations = results.map((run) => run.durationMs).sort((a, b) => a - b);
  const passCount = results.filter((run) => run.pass).length;
  return {
    runs: results,
    passCount,
    failCount: results.length - passCount,
    medianMs: durations.length ? durations[Math.floor(durations.length / 2)] : null,
    minMs: durations.length ? durations[0] : null,
    maxMs: durations.length ? durations[durations.length - 1] : null,
    note: `${runs} 次启动冒烟只能证明短时启动稳定性，不能推算崩溃率。`,
  };
}

function collectEnvironment(appExecutable) {
  const appDir =
    process.platform === "darwin"
      ? appExecutable.replace(/\/Contents\/MacOS\/[^/]+$/, "")
      : path.dirname(appExecutable);
  const gitCommit = tryExec("git", ["rev-parse", "HEAD"], { cwd: repoRoot });
  const gitBranch = tryExec("git", ["rev-parse", "--abbrev-ref", "HEAD"], { cwd: repoRoot });
  const gitStatus = tryExec("git", ["status", "--porcelain"], { cwd: repoRoot });
  const electronVersion = (() => {
    try {
      return JSON.parse(
        fs.readFileSync(path.join(repoRoot, "node_modules", "electron", "package.json"), "utf8"),
      ).version;
    } catch {
      return undefined;
    }
  })();
  const sysctl = (key) => tryExec("sysctl", ["-n", key]);
  let codesign;
  if (process.platform === "darwin") {
    const text = tryExecWithStderr("codesign", ["-dv", "--verbose=2", appDir]);
    codesign = /Signature=adhoc/i.test(text ?? "")
      ? "adhoc"
      : /code object is not signed at all/i.test(text ?? "")
        ? "unsigned"
        : text
          ? "signed"
          : "unknown";
  }
  return {
    generatedAt: new Date().toISOString(),
    platform: process.platform,
    arch: process.arch,
    osVersion: tryExec("sw_vers", ["-productVersion"]),
    osBuild: tryExec("sw_vers", ["-buildVersion"]),
    kernel: os.release(),
    cpu: sysctl("machdep.cpu.brand_string") ?? os.cpus()[0]?.model,
    cpuCount: os.cpus().length,
    memoryBytes: Number(sysctl("hw.memsize")) || os.totalmem(),
    node: process.version,
    npm: tryExec("npm", ["-v"]),
    electronVersion,
    gitCommit,
    gitBranch,
    gitDirty: Boolean(gitStatus),
    gitStatusEntries: gitStatus ? gitStatus.split(/\r?\n/).length : 0,
    appExecutable: path.basename(appExecutable),
    appBundle: path.basename(appDir),
    buildType: "electron-builder --dir (unsigned local build; mac.identity=null)",
    codesign,
    loadAverage: os.loadavg().map((value) => Number(value.toFixed(2))),
    power: tryExec("pmset", ["-g", "batt"])?.split(/\r?\n/)[0] ?? undefined,
    runs: undefined,
    idleSeconds: undefined,
    note: "macOS 无权限清空系统文件缓存；样本为进程冷启动 + 系统页缓存已热，不代表真正冷缓存。",
  };
}

function renderSummary(result) {
  const lines = [];
  lines.push(`# 非模型性能基线（${result.environment.generatedAt}）`);
  lines.push("");
  lines.push(
    `环境：${result.environment.cpu} / ${result.environment.cpuCount} 核 / ${(
      result.environment.memoryBytes /
      1024 ** 3
    ).toFixed(0)}GB / macOS ${result.environment.osVersion} / Electron ${
      result.environment.electronVersion ?? "?"
    }`,
  );
  lines.push(`构建：${result.environment.buildType}；签名：${result.environment.codesign}`);
  lines.push(`commit：${result.environment.gitCommit}（dirty=${result.environment.gitDirty}）`);
  lines.push(
    `负载：${result.environment.loadAverage.join(", ")}；电源：${
      result.environment.power ?? "?"
    }`,
  );
  lines.push("");
  lines.push("## 冷启动");
  if (result.firstLaunch) {
    lines.push(`- 首次启动到 onboarding 可见：${result.firstLaunch.onboardingVisibleMs}ms`);
    lines.push(`- 同次会话写入已引导标记后到工作台：${result.firstLaunch.seededWorkbenchMs}ms`);
  }
  for (const run of result.coldStarts) {
    lines.push(`- 第 ${run.index + 1} 次（已引导 profile）：${run.coldStartMs}ms`);
  }
  lines.push("");
  lines.push("## 空闲内存（RSS）");
  for (const run of result.memory) {
    lines.push(
      `- 第 ${run.index + 1} 次：electronTotal 中位数 ${(
        run.medians.electronTotal /
        1024 ** 2
      ).toFixed(1)}MB / 峰值 ${(run.peaks.electronTotal / 1024 ** 2).toFixed(
        1,
      )}MB；Core 中位数 ${(run.medians.core / 1024 ** 2).toFixed(1)}MB；总中位数 ${(
        run.medians.total /
        1024 ** 2
      ).toFixed(1)}MB / 峰值 ${(run.peaks.total / 1024 ** 2).toFixed(1)}MB`,
    );
  }
  lines.push("");
  lines.push("## 网络观察");
  const networkSamples = result.networkObservations.flatMap((observation) => observation.samples ?? []);
  const nonLoopback = networkSamples.flatMap((sample) => sample.nonLoopback ?? []);
  const nonLoopbackListening = networkSamples.flatMap((sample) => sample.listening ?? []);
  lines.push(`- 非回环连接（lsof 采样）：${nonLoopback.length} 条`);
  for (const entry of nonLoopback.slice(0, 20)) {
    lines.push(`  - pid ${entry.pid} ${entry.command ?? ""} ${entry.address}`);
  }
  lines.push(`- 非回环监听（含 * 监听所有接口）：${nonLoopbackListening.length} 条`);
  for (const entry of nonLoopbackListening.slice(0, 20)) {
    lines.push(`  - pid ${entry.pid} ${entry.command ?? ""} ${entry.address}`);
  }
  lines.push(`- 说明：${result.networkObservations[0]?.note ?? ""}`);
  lines.push("");
  if (result.coreLatency) {
    lines.push("## Core 回环延迟");
    if (result.coreLatency.ok) {
      lines.push(`- Core 就绪：${result.coreLatency.readyMs}ms`);
      lines.push(
        `- /health/live：中位数 ${result.coreLatency.health.medianMs}ms，p95 ${result.coreLatency.health.p95Ms}ms`,
      );
      if (result.coreLatency.projects?.medianMs !== undefined) {
        lines.push(
          `- /api/v1/projects：中位数 ${result.coreLatency.projects.medianMs}ms，p95 ${result.coreLatency.projects.p95Ms}ms`,
        );
      }
    } else {
      lines.push(
        `- 未完成：${result.coreLatency.error ?? result.coreLatency.reason ?? "skipped"}`,
      );
    }
    lines.push("");
  }
  lines.push("## 启动稳定性");
  if (result.stability.runs.length === 0) {
    lines.push("- 本轮跳过（--stability-runs 0）");
  } else {
    lines.push(
      `- ${result.stability.passCount}/${result.stability.runs.length} 次 smoke 通过；耗时中位数 ${
        result.stability.medianMs
      }ms，范围 ${result.stability.minMs}–${result.stability.maxMs}ms`,
    );
  }
  lines.push(`- ${result.stability.note}`);
  lines.push("");
  lines.push("## 限制");
  for (const limitation of result.limitations) lines.push(`- ${limitation}`);
  return lines.join("\n");
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const appExecutable = resolveAppArtifact(options.app);
  const coreBinary = resolveCoreBinary(appExecutable);
  const environment = collectEnvironment(appExecutable);
  environment.runs = options.runs;
  environment.idleSeconds = options.idleSeconds;

  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), "yaomu-perf-"));
  const userData = path.join(tempRoot, "user");
  const coreData = path.join(tempRoot, "core-data");
  fs.mkdirSync(userData, { recursive: true });
  fs.mkdirSync(coreData, { recursive: true });

  const env = { ...process.env, YAOMU_DATA_DIR: coreData };
  for (const key of ["YAOMU_SMOKE", "YAOMU_SMOKE_USER_DATA", "YAOMU_CORE_BIN", "YAOMU_RUNNER_BIN", "VITE_DEV_SERVER_URL"]) {
    delete env[key];
  }
  const args = [`--user-data-dir=${userData}`];

  log(`app: ${appExecutable}`);
  log(`core: ${coreBinary}`);
  log(`isolated temp root: ${tempRoot}`);
  log(
    `env: ${environment.cpu} / ${environment.cpuCount} cores / macOS ${environment.osVersion} / Electron ${environment.electronVersion}`,
  );
  log(`load average: ${environment.loadAverage.join(", ")}`);
  log(
    "注意：本脚本只产出当前环境基线，不判定 NFR-01/02/04/05 达标。",
  );

  const result = {
    environment,
    firstLaunch: undefined,
    coldStarts: [],
    memory: [],
    networkObservations: [],
    coreLatency: undefined,
    stability: undefined,
    limitations: [
      "只测 macOS arm64 本机；不覆盖 Windows/Linux、安装器、签名或更新通道。",
      "不测量模型 RAM/VRAM、GPU、4K/120 分钟吞吐、WER/BLEU/COMET/MOS/克隆相似度。",
      "系统文件缓存无法在无 sudo 条件下清空；冷启动是“进程冷 + 页缓存热”，不是严格冷缓存。",
      "lsof 采样只覆盖采样瞬间；未观察到非回环连接不等于证明零外联。",
      "重复启动 smoke（默认 10 次）只证明短时启动稳定性，不能推算崩溃率 NFR-05。",
      "未判定 NFR-01/02/04/05 达标；没有模型/FlashCLI 准入，也没有长时稳定性测试。",
    ],
  };

  try {
    result.firstLaunch = await seedProfile(appExecutable, env, args, tempRoot);
    for (let index = 0; index < options.runs; index += 1) {
      const run = await measureRun(appExecutable, env, args, index, options);
      result.coldStarts.push({ index: run.index, coldStartMs: run.coldStartMs, workbenchTitle: run.workbenchTitle });
      result.memory.push({
        index: run.index,
        samples: run.memory.samples.map((sample) => ({
          elapsedSec: sample.elapsedSec,
          main: sample.main,
          renderer: sample.renderer,
          gpu: sample.gpu,
          utility: sample.utility,
          core: sample.core,
          other: sample.other,
          electronTotal: sample.electronTotal,
          total: sample.total,
          processCount: sample.processCount,
        })),
        medians: run.memory.medians,
        peaks: run.memory.peaks,
        totalProcessPeak: run.memory.totalProcessPeak,
      });
      result.networkObservations.push({
        run: run.index,
        note: run.network.note,
        samples: run.network.samples,
      });
      if (run.diagnostics) log(`run ${run.index + 1} stderr tail: ${run.diagnostics.split(/\r?\n/).slice(-3).join(" | ")}`);
    }

    result.coreLatency = await measureCoreLatency(
      coreBinary,
      path.join(tempRoot, "core-latency"),
      options.coreLatency,
    );
    log(
      result.coreLatency.skipped
        ? `core latency skipped: ${result.coreLatency.reason}`
        : `core latency: ${result.coreLatency.ok ? "ok" : "failed"}`,
    );

    result.stability = await measureStability(options.stabilityRuns);
  } finally {
    if (options.keepTemp) {
      log(`temp retained: ${tempRoot}`);
    } else {
      try {
        fs.rmSync(tempRoot, { recursive: true, force: true });
      } catch {
        log(`temp cleanup failed: ${tempRoot}`);
      }
    }
  }

  const summary = renderSummary(result);
  console.log("");
  console.log(summary);

  fs.mkdirSync(path.dirname(options.json), { recursive: true });
  fs.writeFileSync(options.json, `${JSON.stringify(result, null, 2)}\n`);
  log(`json written: ${options.json}`);
}

main().catch((error) => {
  fail(error instanceof Error ? (error.stack ?? error.message) : String(error));
});

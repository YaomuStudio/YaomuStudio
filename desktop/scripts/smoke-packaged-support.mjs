#!/usr/bin/env node
/**
 * 打包态冒烟支撑：来源清理、资源定位与真实 WSL hello 验证。
 *
 * 边界（contracts/packaged-runner-resources.md）：
 * - 清理会改变启动来源的开发/外部/演示覆盖（大小写不敏感），不记录清理后的环境。
 * - WSL hello 只使用调用方给出的生产 argv；本模块不推导 checkout/宿主入口。
 * - stdout/stderr 有界；超时、溢出、非法帧、提前退出、清理未确认一律失败。
 * - 只确认本进程拉起的 wsl.exe 退出，不宣称所有 Linux 后代已回收，
 *   也不执行 wsl --shutdown。
 */
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

export const HELLO_REQ_ID = "smoke-hello";
export const SHUTDOWN_REQ_ID = "smoke-shutdown";

/** 会改变 Runner/Core 启动来源的已知覆盖；严格冒烟必须清掉。 */
export const SOURCE_OVERRIDE_KEYS = [
  "YAOMU_CORE_BIN",
  "YAOMU_RUNNER_BIN",
  "YAOMU_WSL_RUNNER_COMMAND",
  "YAOMU_WSL_RUNNER_ARGS",
  "VITE_DEV_SERVER_URL",
  "YAOMU_MOCK_MODEL_SLOTS",
];

/**
 * 复制环境并删除已知来源覆盖（Windows 环境变量名大小写不敏感）。
 * 保留明确选择的镜像/解释器配置（如 YAOMU_WSL_DISTRO / YAOMU_WSL_PYTHON）。
 */
export function cleanSourceEnv(env = process.env) {
  const cleaned = { ...env };
  const blocked = new Set(SOURCE_OVERRIDE_KEYS.map((key) => key.toUpperCase()));
  for (const key of Object.keys(cleaned)) {
    if (blocked.has(key.toUpperCase())) delete cleaned[key];
  }
  return cleaned;
}

/**
 * 从应用可执行文件定位包内 resources 目录；显式 --app 同样适用。
 * 找不到时返回 null（调用方必须失败，不能跳过资源检查）。
 */
export function resolveResourcesDir(executable, platform = process.platform) {
  const resolved = path.resolve(executable);
  const candidates = [];
  if (platform === "darwin" && /\.app$/i.test(resolved)) {
    candidates.push(path.join(resolved, "Contents", "Resources"));
  }
  const dir = path.dirname(resolved);
  if (platform === "darwin") {
    candidates.push(path.join(dir, "..", "Resources"));
  } else {
    candidates.push(path.join(dir, "resources"), path.join(dir, "..", "resources"));
  }
  for (const candidate of candidates) {
    try {
      if (fs.statSync(candidate).isDirectory()) return path.resolve(candidate);
    } catch {
      // 继续尝试下一个候选。
    }
  }
  return null;
}

/** 在 maxDepth 层内查找第一个满足条件的条目（含目录自身）。 */
export function findFirst(dir, predicate, maxDepth) {
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
}

export function pickExecutable(dir) {
  const entries = fs.readdirSync(dir, { withFileTypes: true }).filter((entry) => entry.isFile());
  // macOS：MacOS 目录里通常只有应用主二进制；Linux 走 pickLinuxExecutable。
  const entry = entries.find((item) => !/uninstall/i.test(item.name));
  return entry ? path.join(dir, entry.name) : undefined;
}

/**
 * Linux 解包目录根层同时有应用主二进制和 Chromium 辅助可执行文件
 * （chrome-sandbox、chrome_crashpad_handler、共享库等）。按“可执行普通文件中最大者”
 * 选择主二进制，避免依赖 readdir 顺序导致运行错文件。
 */
export function pickLinuxExecutable(unpacked) {
  const candidates = fs
    .readdirSync(unpacked, { withFileTypes: true })
    .filter((entry) => entry.isFile() && !/uninstall/i.test(entry.name))
    .map((entry) => {
      const full = path.join(unpacked, entry.name);
      try {
        fs.accessSync(full, fs.constants.X_OK);
        return { name: entry.name, size: fs.statSync(full).size };
      } catch {
        return undefined;
      }
    })
    .filter((item) => item !== undefined)
    .sort((a, b) => b.size - a.size);
  return candidates[0]?.name;
}

/**
 * 解析打包产物：返回 { executable, resourcesDir }。
 * 显式 --app 也必须携带同级 resources；找不到即抛错，绝不跳过资源/hello 检查。
 * 由调用方决定如何报告（冒烟脚本 fail；artifact-only 检查直接失败）。
 */
export function resolvePackagedArtifacts({
  explicit,
  outDir,
  platform = process.platform,
}) {
  if (explicit) {
    const executable = path.resolve(explicit);
    let stat;
    try {
      stat = fs.statSync(executable);
    } catch {
      throw new Error(`--app 指向的文件不存在：${executable}`);
    }
    if (!stat.isFile()) throw new Error(`--app 指向的文件不存在：${executable}`);
    const resourcesDir = resolveResourcesDir(executable, platform);
    if (!resourcesDir) {
      throw new Error(`--app 未解析到包内 resources 目录：${path.dirname(executable)}`);
    }
    return { executable, resourcesDir };
  }

  if (!fs.existsSync(outDir)) {
    throw new Error(`未找到打包产物 ${outDir}；先运行 npm run pack:dir`);
  }
  if (platform === "darwin") {
    const appDir = findFirst(
      outDir,
      (entry) => entry.isDirectory() && entry.name.endsWith(".app"),
      2,
    );
    if (!appDir) throw new Error(`在 ${outDir} 下找不到 .app 产物`);
    const executable = pickExecutable(path.join(appDir, "Contents", "MacOS"));
    if (!executable) throw new Error(`在 ${appDir}/Contents/MacOS 下找不到可执行文件`);
    return { executable, resourcesDir: path.join(appDir, "Contents", "Resources") };
  }
  const unpacked = findFirst(
    outDir,
    (entry) => entry.isDirectory() && /-unpacked$/.test(entry.name),
    1,
  );
  if (!unpacked) throw new Error(`在 ${outDir} 下找不到 *-unpacked 产物`);
  let executableName;
  if (platform === "win32") {
    const entries = fs
      .readdirSync(unpacked, { withFileTypes: true })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".exe"));
    const executableEntry = entries.find((entry) => !/uninstall/i.test(entry.name)) ?? entries[0];
    if (!executableEntry) throw new Error(`在 ${unpacked} 下找不到应用可执行文件`);
    executableName = executableEntry.name;
  } else {
    executableName = pickLinuxExecutable(unpacked);
    if (!executableName) throw new Error(`在 ${unpacked} 下找不到应用可执行文件`);
  }
  return {
    executable: path.join(unpacked, executableName),
    resourcesDir: path.join(unpacked, "resources"),
  };
}

/**
 * 校验随包资源布局并返回生产清单校验所需的 core 目录。
 * 只做文件存在/执行位检查；清单与逐文件哈希由 runner-resources 负责。
 */
export function verifyPackagedLayout(resourcesDir, platform = process.platform) {
  const coreDir = path.join(resourcesDir, "core");
  const coreBinary = path.join(
    coreDir,
    platform === "win32" ? "yaomu-core.exe" : "yaomu-core",
  );
  if (!fs.existsSync(coreBinary)) throw new Error(`包内缺少 Core 二进制：${coreBinary}`);
  if (platform !== "win32") {
    try {
      fs.accessSync(coreBinary, fs.constants.X_OK);
    } catch {
      throw new Error(`包内 Core 不可执行（缺少执行位）：${coreBinary}`);
    }
  }
  const runner = path.join(
    coreDir,
    platform === "win32" ? "yaomu-runner.cmd" : "yaomu-runner",
  );
  const runnerSrc = path.join(coreDir, "runner_src");
  const manifest = path.join(coreDir, "runner-manifest.json");
  // 宿主启动器只是兼容/资源项，不是 Windows WSL 证据；这里只要求随包存在。
  if (!fs.existsSync(runner)) throw new Error(`包内缺少 Runner 启动器：${runner}`);
  if (!fs.existsSync(runnerSrc)) throw new Error(`包内缺少 Runner 源码目录：${runnerSrc}`);
  if (!fs.existsSync(manifest)) throw new Error(`包内缺少 Runner 资源清单：${manifest}`);
  if (platform !== "win32") {
    for (const [relative, label] of [
      [path.join("python", "bin", "python3"), "随包 Python"],
      ["ffmpeg", "随包 FFmpeg"],
      ["ffprobe", "随包 ffprobe"],
    ]) {
      const file = path.join(coreDir, relative);
      if (!fs.existsSync(file)) throw new Error(`包内缺少${label}：${file}`);
      try {
        fs.accessSync(file, fs.constants.X_OK);
      } catch {
        throw new Error(`包内${label}不可执行（缺少执行位）：${file}`);
      }
    }
  }
  return { coreDir, coreBinary, runner, runnerSrc, manifest };
}

/** 有界字符串缓冲：超限后清空并标记，避免截断内容被当成功。 */
export class BoundedSink {
  constructor(limit) {
    this.limit = limit;
    this.chunks = [];
    this.size = 0;
    this.overflow = false;
  }

  push(chunk) {
    if (this.overflow) return true;
    const text = String(chunk);
    const bytes = Buffer.byteLength(text, "utf8");
    if (this.size + bytes > this.limit) {
      this.overflow = true;
      this.chunks = [];
      this.size = 0;
      return true;
    }
    this.chunks.push(text);
    this.size += bytes;
    return false;
  }

  text() {
    return this.overflow ? "" : this.chunks.join("");
  }
}

function errorCodeOf(error) {
  return error && typeof error === "object" && typeof error.code === "string"
    ? error.code
    : null;
}

function observeChild(child) {
  const spawnedPid = child.pid;
  let exited = child.exitCode !== null || child.signalCode !== null;
  let settled = false;
  let liveErrorReported = false;
  let closeInfo = null;
  let resolveEvent;
  let resolveLiveError;
  const event = new Promise((resolve) => {
    resolveEvent = resolve;
  });
  const liveError = new Promise((resolve) => {
    resolveLiveError = resolve;
  });
  const dispose = () => {
    child.removeListener("error", onError);
    child.removeListener("exit", onExit);
    child.removeListener("close", onClose);
  };
  const finish = (outcome) => {
    if (settled) return;
    settled = true;
    dispose();
    resolveEvent(outcome);
  };
  function onError(error) {
    const code = errorCodeOf(error);
    if (spawnedPid === undefined) {
      // spawn 成功前失败：没有自有进程可清理，error 就是终止事实。
      exited = true;
      finish({ kind: "error", code });
      return;
    }
    // 活 child 的 error（EPERM/EPIPE 等）不是退出证据，也不是关闭确认。
    if (liveErrorReported) return;
    liveErrorReported = true;
    resolveLiveError({ kind: "live-error", code });
  }
  function onExit() {
    exited = true;
    // 等 close 收齐 stdio；close 才是正常完成证据。
  }
  function onClose(code, signal) {
    exited = true;
    closeInfo = { code, signal };
    finish({ kind: "exit", code, signal });
  }
  child.on("error", onError);
  child.once("exit", onExit);
  child.once("close", onClose);
  return {
    event,
    liveError,
    exited: () => exited || settled,
    closeInfo: () => closeInfo,
    dispose,
  };
}

function settleWithin(promise, ms) {
  return new Promise((resolve) => {
    let settled = false;
    let timer;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      resolve(value);
    };
    timer = setTimeout(() => finish(false), ms);
    void promise.then(
      () => finish(true),
      () => finish(true),
    );
  });
}

/** 先等协议层 shutdown 后的自然 close；只有确认不到才升级到 kill。 */
async function settleClose(transport, ms) {
  if (transport.closeInfo()) return true;
  return settleWithin(transport.event, ms);
}

/**
 * 只停止本进程拉起的 hello transport：SIGTERM → 有界等待 → SIGKILL → 有界等待。
 * 只有 exit/close 证据才返回 true；发送信号本身不算已停止。
 */
async function stopOwned(child, transport, graceMs) {
  if (transport.exited()) return true;
  try {
    child.kill();
  } catch {
    // 已退出或无法发送；由退出证据判断。
  }
  if ((await settleWithin(transport.event, graceMs)) || transport.exited()) return true;
  try {
    child.kill("SIGKILL");
  } catch {
    // 同上。
  }
  return (await settleWithin(transport.event, graceMs)) || transport.exited();
}

/**
 * 校验 I03 hello 结果。只返回固定 reason，不回显原始帧。
 */
export function parseHelloResult(frame, options) {
  const {
    reqId = HELLO_REQ_ID,
    expectedVersion,
    expectedProtocol = 1,
    requiredAdapters = ["ffmpeg", "flashcli"],
    verifyAdapters,
  } = options;
  if (!frame || typeof frame !== "object" || Array.isArray(frame)) {
    return { ok: false, detail: "malformed-result" };
  }
  if (frame.op !== "result" || frame.req_id !== reqId) {
    return { ok: false, detail: "malformed-result" };
  }
  const payload = frame.payload;
  if (!payload || typeof payload !== "object" || payload.status !== "ok") {
    return { ok: false, detail: "malformed-result" };
  }
  const data = payload.data;
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return { ok: false, detail: "malformed-result" };
  }
  if (typeof expectedVersion !== "string" || data.runner_version !== expectedVersion) {
    return { ok: false, detail: "version-mismatch" };
  }
  if (data.protocol_version !== expectedProtocol) {
    return { ok: false, detail: "protocol-mismatch" };
  }
  if (!Array.isArray(data.adapters)) {
    return { ok: false, detail: "adapters-missing" };
  }
  const ids = [];
  for (const adapter of data.adapters) {
    if (!adapter || typeof adapter !== "object" || typeof adapter.id !== "string") {
      return { ok: false, detail: "adapters-missing" };
    }
    ids.push(adapter.id);
  }
  if (new Set(ids).size !== ids.length) {
    return { ok: false, detail: "adapters-duplicate" };
  }
  for (const id of requiredAdapters) {
    if (!ids.includes(id)) return { ok: false, detail: "adapters-missing" };
  }
  if (typeof verifyAdapters === "function") {
    const verdict = verifyAdapters(data);
    if (!verdict || verdict.ok !== true) {
      return { ok: false, detail: verdict?.detail ?? "adapter-check-failed" };
    }
  }
  return { ok: true, detail: "hello-ok" };
}

/**
 * 通过生产 argv 拉起真实 WSL Runner hello，并完成有界 shutdown/退出确认。
 * spawnImpl 仅用于负向测试工具；真实验收必须走 wsl.exe。
 */
export async function runWslHello(options) {
  const {
    command,
    args,
    env,
    expectedVersion,
    expectedProtocol = 1,
    requiredAdapters = ["ffmpeg", "flashcli"],
    verifyAdapters,
    timeoutMs = 60_000,
    stopGraceMs = 5_000,
    stdoutLimit = 256 * 1024,
    stderrLimit = 64 * 1024,
    cwd,
    spawnImpl = spawn,
  } = options;
  if (!command || !Array.isArray(args) || typeof expectedVersion !== "string") {
    return { ok: false, detail: "invalid-argv" };
  }

  let child;
  try {
    child = spawnImpl(command, args, {
      env,
      cwd,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"],
    });
  } catch {
    return { ok: false, detail: "spawn-error" };
  }
  if (!child || !child.stdout || !child.stderr || !child.stdin) {
    try {
      child?.kill?.();
    } catch {
      // 无自有 transport 可清理。
    }
    return { ok: false, detail: "spawn-error" };
  }

  const stdout = new BoundedSink(stdoutLimit);
  const stderr = new BoundedSink(stderrLimit);
  const transport = observeChild(child);
  let overflow = false;
  let stdinBroken = false;
  let streamError = false;
  let hello = null;
  let resolveHello;
  const helloPromise = new Promise((resolve) => {
    resolveHello = resolve;
  });
  const settleHello = (result) => {
    if (hello !== null) return;
    hello = result;
    resolveHello(result);
  };

  // stdio 错误既不能当退出证据，也不能被忽略；由失败路径统一收口。
  child.stdin.on("error", () => {
    stdinBroken = true;
  });
  child.stdout.on("error", () => {
    streamError = true;
    settleHello({ ok: false, detail: "stream-error" });
  });
  child.stderr.on("error", () => {
    streamError = true;
    settleHello({ ok: false, detail: "stream-error" });
  });

  let buffer = "";
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    if (stdout.push(chunk) || overflow) {
      overflow = true;
      settleHello({ ok: false, detail: "output-overflow" });
      return;
    }
    if (hello !== null) return;
    buffer += chunk;
    for (;;) {
      const newline = buffer.indexOf("\n");
      if (newline < 0) break;
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (!line) continue;
      let frame;
      try {
        frame = JSON.parse(line);
      } catch {
        settleHello({ ok: false, detail: "invalid-frame" });
        return;
      }
      if (frame && frame.op === "result" && frame.req_id === HELLO_REQ_ID) {
        settleHello(
          parseHelloResult(frame, {
            expectedVersion,
            expectedProtocol,
            requiredAdapters,
            verifyAdapters,
          }),
        );
        return;
      }
    }
  });
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => {
    if (stderr.push(chunk)) overflow = true;
  });

  try {
    child.stdin.write(
      `${JSON.stringify({ v: 1, op: "hello", req_id: HELLO_REQ_ID, payload: {} })}\n`,
    );
  } catch {
    const confirmed = await stopOwned(child, transport, stopGraceMs);
    return { ok: false, detail: confirmed ? "hello-write-failed" : "cleanup-unconfirmed" };
  }

  const timer = setTimeout(() => resolveHello("timeout"), timeoutMs);
  let outcome;
  try {
    outcome = await Promise.race([helloPromise, transport.event, transport.liveError]);
  } finally {
    clearTimeout(timer);
  }

  const failAfterCleanup = async (detail) => {
    const confirmed = await stopOwned(child, transport, stopGraceMs);
    return { ok: false, detail: confirmed ? detail : "cleanup-unconfirmed" };
  };

  try {
    if (outcome === "timeout") return await failAfterCleanup("hello-timeout");
    if (outcome && outcome.kind === "live-error") {
      // 活 child 的 error 不是退出证据；先做有界清理，再按失败上报。
      return await failAfterCleanup("live-error");
    }
    if (!hello) {
      // transport 先结束（提前退出/spawn 错误）：没有可用 hello。
      const confirmed = transport.exited();
      if (!confirmed) return { ok: false, detail: "cleanup-unconfirmed" };
      return {
        ok: false,
        detail: outcome?.kind === "error" ? "spawn-error" : "exited-before-hello",
      };
    }
    if (streamError) return await failAfterCleanup("stream-error");
    if (overflow || stdout.overflow || stderr.overflow) {
      return await failAfterCleanup("output-overflow");
    }
    if (!hello.ok) {
      const confirmed = await stopOwned(child, transport, stopGraceMs);
      return confirmed ? hello : { ok: false, detail: "cleanup-unconfirmed" };
    }

    // hello 成功只证明协议可用；Runner 必须保持存活直到协议 shutdown。
    if (transport.exited()) return { ok: false, detail: "exited-before-shutdown" };
    if (stdinBroken) return await failAfterCleanup("shutdown-write-failed");
    try {
      child.stdin.write(
        `${JSON.stringify({ v: 1, op: "shutdown", req_id: SHUTDOWN_REQ_ID, payload: {} })}\n`,
      );
    } catch {
      return await failAfterCleanup("shutdown-write-failed");
    }
    // 只接受协议 shutdown 后的真实 close/exit；kill ACK、error 事件都不算关闭。
    if (!(await settleClose(transport, stopGraceMs))) {
      const confirmed = await stopOwned(child, transport, stopGraceMs);
      return {
        ok: false,
        detail: confirmed ? "shutdown-not-observed" : "cleanup-unconfirmed",
      };
    }
    const closed = transport.closeInfo();
    if (!closed) return await failAfterCleanup("shutdown-not-observed");
    if (closed.signal !== null) return { ok: false, detail: "exited-by-signal" };
    if (closed.code !== 0) return { ok: false, detail: "exit-nonzero" };
    if (stdinBroken) return { ok: false, detail: "shutdown-write-failed" };
    if (streamError) return { ok: false, detail: "stream-error" };
    if (stdout.overflow || stderr.overflow) return { ok: false, detail: "output-overflow" };
    return { ok: true, detail: "hello-ok" };
  } finally {
    transport.dispose();
  }
}

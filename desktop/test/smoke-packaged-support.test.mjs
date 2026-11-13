import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { PassThrough } from "node:stream";
import test from "node:test";
import {
  BoundedSink,
  cleanSourceEnv,
  parseHelloResult,
  resolveResourcesDir,
  runWslHello,
} from "../scripts/smoke-packaged-support.mjs";

function createFakeChild() {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.stdin = new PassThrough();
  child.pid = 6100;
  child.exitCode = null;
  child.signalCode = null;
  child.kills = [];
  child.kill = (signal = "SIGTERM") => {
    child.kills.push(signal);
    return true;
  };
  child.finish = (code = 0, signal = null) => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    child.exitCode = code;
    child.signalCode = signal;
    child.stdout.end();
    child.stderr.end();
    child.emit("exit", code, signal);
    child.emit("close", code, signal);
  };
  return child;
}

function helloFrame({ version = "0.1.0", protocol = 1, adapters, status = "ok" } = {}) {
  return `${JSON.stringify({
    v: 1,
    op: "result",
    req_id: "smoke-hello",
    request_op: "hello",
    payload: {
      status,
      data: {
        runner_version: version,
        protocol_version: protocol,
        adapters:
          adapters ?? [
            { id: "flashcli", ready: false },
            { id: "ffmpeg", ready: true },
            { id: "separate", ready: true },
            { id: "voice_clone", ready: true },
          ],
      },
    },
  })}\n`;
}

function baseOptions(overrides = {}) {
  return {
    command: "wsl.exe",
    args: ["-d", "yaomu", "-e", "env", "PYTHONPATH=/mnt/c/x", "python3", "-B", "-P", "-s", "-m", "yaomustudio_runner"],
    env: {},
    expectedVersion: "0.1.0",
    timeoutMs: 500,
    stopGraceMs: 30,
    stdoutLimit: 64 * 1024,
    stderrLimit: 16 * 1024,
    ...overrides,
  };
}

/** 收到 hello 后回帧；收到 shutdown 后按策略退出。 */
function protocolChild({ hello = helloFrame(), exitOnShutdown = true, finishCode = 0, finishSignal = null } = {}) {
  const child = createFakeChild();
  child.stdin.on("data", (chunk) => {
    for (const raw of chunk.toString().split("\n")) {
      if (!raw.trim()) continue;
      let frame;
      try {
        frame = JSON.parse(raw);
      } catch {
        continue;
      }
      if (frame.op === "hello") {
        child.stdout.write(hello);
      } else if (frame.op === "shutdown" && exitOnShutdown) {
        queueMicrotask(() => child.finish(finishCode, finishSignal));
      }
    }
  });
  return child;
}

function stopOnKill(child) {
  child.kill = (signal = "SIGTERM") => {
    child.kills.push(signal);
    queueMicrotask(() => child.finish(null, signal));
    return true;
  };
  return child;
}

test("cleanSourceEnv removes source overrides case-insensitively and keeps image config", () => {
  const env = {
    PATH: "/usr/bin",
    yaomu_core_bin: "/tmp/core",
    YAOMU_RUNNER_BIN: "/tmp/runner",
    Yaomu_Wsl_Runner_Command: "wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: "[]",
    VITE_DEV_SERVER_URL: "http://localhost:5173",
    yaomu_mock_model_slots: "separation",
    YAOMU_WSL_DISTRO: "yaomu",
    YAOMU_WSL_PYTHON: "/usr/bin/python3.12",
  };
  const cleaned = cleanSourceEnv(env);
  assert.deepEqual(cleaned, {
    PATH: "/usr/bin",
    YAOMU_WSL_DISTRO: "yaomu",
    YAOMU_WSL_PYTHON: "/usr/bin/python3.12",
  });
  assert.equal(env.YAOMU_RUNNER_BIN, "/tmp/runner");
});

test("resolveResourcesDir requires sibling resources for explicit apps", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-resources-dir-"));
  try {
    const unpacked = path.join(root, "win-unpacked");
    await mkdir(path.join(unpacked, "resources", "core"), { recursive: true });
    const executable = path.join(unpacked, "YaoMu Studio.exe");
    await writeFile(executable, "fake");
    assert.equal(
      resolveResourcesDir(executable, "win32"),
      path.join(unpacked, "resources"),
    );
    // 缺少 resources 的显式 app 必须解析失败（旧脚本会 SKIP 后 PASS）。
    const bare = path.join(root, "bare", "fake-app.cjs");
    await mkdir(path.dirname(bare), { recursive: true });
    await writeFile(bare, "console.log('ok')");
    assert.equal(resolveResourcesDir(bare, "win32"), null);
    assert.equal(resolveResourcesDir(bare, "linux"), null);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("resolveResourcesDir understands macOS app bundles", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-resources-mac-"));
  try {
    const appDir = path.join(root, "YaoMu Studio.app");
    const resources = path.join(appDir, "Contents", "Resources");
    const macos = path.join(appDir, "Contents", "MacOS");
    await mkdir(resources, { recursive: true });
    await mkdir(macos, { recursive: true });
    const executable = path.join(macos, "YaoMu Studio");
    await writeFile(executable, "fake");
    assert.equal(resolveResourcesDir(executable, "darwin"), resources);
    assert.equal(resolveResourcesDir(appDir, "darwin"), resources);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("BoundedSink invalidates content after overflow", () => {
  const sink = new BoundedSink(8);
  assert.equal(sink.push("abc"), false);
  assert.equal(sink.push("def"), false);
  assert.equal(sink.push("ghij"), true);
  assert.equal(sink.overflow, true);
  assert.equal(sink.text(), "");
});

test("parseHelloResult accepts the expected I03 hello shape", () => {
  const frame = JSON.parse(helloFrame());
  assert.deepEqual(parseHelloResult(frame, { expectedVersion: "0.1.0" }), {
    ok: true,
    detail: "hello-ok",
  });
});

test("parseHelloResult rejects version, protocol and adapter mismatches", () => {
  assert.equal(
    parseHelloResult(JSON.parse(helloFrame({ version: "9.9.9" })), {
      expectedVersion: "0.1.0",
    }).detail,
    "version-mismatch",
  );
  assert.equal(
    parseHelloResult(JSON.parse(helloFrame({ protocol: 2 })), {
      expectedVersion: "0.1.0",
    }).detail,
    "protocol-mismatch",
  );
  assert.equal(
    parseHelloResult(JSON.parse(helloFrame({ adapters: [{ id: "ffmpeg" }] })), {
      expectedVersion: "0.1.0",
    }).detail,
    "adapters-missing",
  );
  assert.equal(
    parseHelloResult(
      JSON.parse(helloFrame({ adapters: [{ id: "ffmpeg" }, { id: "ffmpeg" }, { id: "flashcli" }] })),
      { expectedVersion: "0.1.0" },
    ).detail,
    "adapters-duplicate",
  );
  assert.equal(
    parseHelloResult(JSON.parse(helloFrame({ status: "error" })), {
      expectedVersion: "0.1.0",
    }).detail,
    "malformed-result",
  );
});

test("parseHelloResult can apply a host compatibility adapter rule", () => {
  const frame = JSON.parse(helloFrame());
  const accepted = parseHelloResult(frame, {
    expectedVersion: "0.1.0",
    verifyAdapters: (data) => ({
      ok: data.adapters.some((item) => item.id === "flashcli" && item.ready === false),
      detail: "host-adapter-check-failed",
    }),
  });
  assert.deepEqual(accepted, { ok: true, detail: "hello-ok" });
  const rejected = parseHelloResult(frame, {
    expectedVersion: "0.1.0",
    verifyAdapters: () => ({ ok: false, detail: "host-adapter-check-failed" }),
  });
  assert.deepEqual(rejected, { ok: false, detail: "host-adapter-check-failed" });
});

test("runWslHello passes with real protocol frames and confirmed exit", async () => {
  const child = protocolChild();
  const result = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => child,
  });
  assert.deepEqual(result, { ok: true, detail: "hello-ok" });
  assert.equal(child.exitCode, 0);
});

test("nonzero exit code after shutdown is a failure", async () => {
  const child = protocolChild({ finishCode: 7 });
  const result = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => child,
  });
  assert.deepEqual(result, { ok: false, detail: "exit-nonzero" });
});

test("signal exit after shutdown is a failure", async () => {
  const child = protocolChild({ finishCode: null, finishSignal: "SIGTERM" });
  const result = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => child,
  });
  assert.deepEqual(result, { ok: false, detail: "exited-by-signal" });
});

test("a live child error is not exit evidence and still requires confirmed cleanup", async () => {
  const stoppable = stopOnKill(createFakeChild());
  const confirmed = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => {
      queueMicrotask(() =>
        stoppable.emit("error", Object.assign(new Error("EPERM"), { code: "EPERM" })),
      );
      return stoppable;
    },
  });
  assert.deepEqual(confirmed, { ok: false, detail: "live-error" });

  const unkillable = createFakeChild();
  unkillable.kill = () => true;
  const unconfirmed = await runWslHello({
    ...baseOptions({ stopGraceMs: 15 }),
    spawnImpl: () => {
      queueMicrotask(() =>
        unkillable.emit("error", Object.assign(new Error("EPERM"), { code: "EPERM" })),
      );
      return unkillable;
    },
  });
  assert.deepEqual(unconfirmed, { ok: false, detail: "cleanup-unconfirmed" });
});

test("stdout/stderr stream errors invalidate the verifier", async () => {
  const beforeHello = createFakeChild();
  beforeHello.stdin.on("data", (chunk) => {
    if (chunk.toString().includes('"hello"')) {
      queueMicrotask(() => beforeHello.stdout.emit("error", new Error("stream boom")));
    }
  });
  const pre = await runWslHello({
    ...baseOptions({ stopGraceMs: 100 }),
    spawnImpl: () => stopOnKill(beforeHello),
  });
  assert.deepEqual(pre, { ok: false, detail: "stream-error" });

  const afterHello = protocolChild();
  afterHello.stdin.on("data", (chunk) => {
    if (chunk.toString().includes('"shutdown"')) {
      queueMicrotask(() => {
        afterHello.stdout.emit("error", new Error("stream boom"));
        afterHello.finish(0);
      });
    }
  });
  const post = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => afterHello,
  });
  assert.deepEqual(post, { ok: false, detail: "stream-error" });
});

test("hello success with unconfirmed cleanup fails", async () => {
  const child = protocolChild({ exitOnShutdown: false });
  // kill 不产生退出证据：不得把已发送信号当作清理完成。
  child.kill = (signal = "SIGTERM") => {
    child.kills.push(signal);
    return true;
  };
  const result = await runWslHello({
    ...baseOptions({ stopGraceMs: 20 }),
    spawnImpl: () => child,
  });
  assert.deepEqual(result, { ok: false, detail: "cleanup-unconfirmed" });
});

test("hello timeout without confirmed cleanup fails distinctly", async () => {
  const silent = createFakeChild();
  silent.kill = () => true;
  const unconfirmed = await runWslHello({
    ...baseOptions({ timeoutMs: 30, stopGraceMs: 15 }),
    spawnImpl: () => silent,
  });
  assert.deepEqual(unconfirmed, { ok: false, detail: "cleanup-unconfirmed" });

  const stoppable = stopOnKill(createFakeChild());
  const timedOut = await runWslHello({
    ...baseOptions({ timeoutMs: 30, stopGraceMs: 100 }),
    spawnImpl: () => stoppable,
  });
  assert.deepEqual(timedOut, { ok: false, detail: "hello-timeout" });
});

test("early exit before hello is a failure, never a skip", async () => {
  const child = createFakeChild();
  const result = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => {
      queueMicrotask(() => child.finish(1));
      return child;
    },
  });
  assert.deepEqual(result, { ok: false, detail: "exited-before-hello" });
});

test("output overflow invalidates even a valid hello", async () => {
  const child = createFakeChild();
  child.stdin.on("data", (chunk) => {
    if (!chunk.toString().includes('"hello"')) return;
    child.stdout.write(helloFrame());
    child.stdout.write("x".repeat(2048));
  });
  const result = await runWslHello({
    ...baseOptions({ stdoutLimit: 256, stopGraceMs: 100 }),
    spawnImpl: () => stopOnKill(child),
  });
  assert.deepEqual(result, { ok: false, detail: "output-overflow" });
});

test("malformed frame and version mismatch fail with fixed reasons", async () => {
  const invalid = createFakeChild();
  invalid.stdin.on("data", (chunk) => {
    if (chunk.toString().includes('"hello"')) invalid.stdout.write("{not json}\n");
  });
  const invalidResult = await runWslHello({
    ...baseOptions({ stopGraceMs: 100 }),
    spawnImpl: () => stopOnKill(invalid),
  });
  assert.deepEqual(invalidResult, { ok: false, detail: "invalid-frame" });

  const wrongVersion = protocolChild({ hello: helloFrame({ version: "9.9.9" }) });
  const versionResult = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => stopOnKill(wrongVersion),
  });
  assert.deepEqual(versionResult, { ok: false, detail: "version-mismatch" });
});

test("spawn failure is reported without pretending a runner exists", async () => {
  const result = await runWslHello({
    ...baseOptions(),
    spawnImpl: () => {
      throw new Error("ENOENT");
    },
  });
  assert.deepEqual(result, { ok: false, detail: "spawn-error" });

  const invalidArgv = await runWslHello({ ...baseOptions(), args: undefined });
  assert.deepEqual(invalidArgv, { ok: false, detail: "invalid-argv" });
});

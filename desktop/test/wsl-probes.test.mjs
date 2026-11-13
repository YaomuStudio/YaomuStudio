import assert from "node:assert/strict";
import { spawn as realSpawn } from "node:child_process";
import { EventEmitter } from "node:events";
import { createRequire } from "node:module";
import { PassThrough } from "node:stream";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  WslProbeCleanupError,
  parseWslToolsOutput,
  prepareWslEnvironment,
  probeWslTools,
  probeWslRuntime,
  warmWsl,
} = require("../dist/wsl.js");

const emptyTools = { flashcli: null, mlstools: null, ffmpeg: null, ffprobe: null, python3: null };
const allTools = {
  flashcli: "/usr/local/bin/flashcli",
  mlstools: "/usr/local/bin/mlstools",
  ffmpeg: "/opt/bin/ffmpeg",
  ffprobe: "/opt/bin/ffprobe",
  python3: "/usr/bin/python3",
};

function createFakeChild() {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.pid = 4242;
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

/** 按调用顺序注入可控子进程；handler 在 spawn 返回前同步执行。 */
function createController(handlers = []) {
  const calls = [];
  const spawnImpl = (command, args, options) => {
    const child = createFakeChild();
    const call = { command, args, options, child };
    calls.push(call);
    const handler = handlers[calls.length - 1];
    if (handler) handler(call);
    return child;
  };
  return { spawnImpl, calls };
}

function complete(call, { code = 0, stdout = "", stderr = "" } = {}) {
  const { child } = call;
  queueMicrotask(() => {
    if (stdout) child.stdout.write(stdout);
    if (stderr) child.stderr.write(stderr);
    child.finish(code);
  });
}

/** 收到停止信号后在 microtask 中退出，模拟可确认清理的自有 transport。 */
function stopOnKill(call) {
  call.child.kill = (signal = "SIGTERM") => {
    call.child.kills.push(signal);
    queueMicrotask(() => call.child.finish(null, signal));
    return true;
  };
}

function deferred() {
  let resolve;
  const promise = new Promise((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

const win32 = { platform: "win32", env: {} };

test("parseWslToolsOutput accepts only the five own tools with absolute paths", () => {
  const parsed = parseWslToolsOutput(
    [
      "flashcli=/usr/local/bin/flashcli",
      "mlstools=/usr/local/bin/mlstools",
      "ffmpeg=/opt/bin/ffmpeg",
      "ffprobe=/opt/bin/ffprobe",
      "python3=/usr/bin/python3",
      "toString=/usr/bin/toString",
      "constructor=/usr/bin/constructor",
      "__proto__=/usr/bin/proto",
      "ffmpeg=relative/ffmpeg",
      "python3=/usr/bin/python three",
    ].join("\n"),
  );

  assert.deepEqual(parsed, allTools);
  assert.equal(Object.hasOwn(parsed, "toString"), false);
  assert.equal(typeof parsed.toString, "function");
});

test("probeWslTools parses five tools from a controlled transport", async () => {
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { stdout: Object.entries(allTools).map(([k, v]) => `${k}=${v}`).join("\n") }),
  ]);
  const result = await probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(calls.length, 1);
  assert.equal(result.ok, true);
  assert.deepEqual(result.tools, allTools);
  assert.equal(result.detail, null);
});

test("probeWslTools fails read on stdout overflow instead of returning a truncated list", async () => {
  const { spawnImpl } = createController([
    (call) =>
      complete(call, {
        stdout: "ffmpeg=/usr/bin/ffmpeg\n" + "x".repeat(70 * 1024),
      }),
  ]);
  const result = await probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "output-overflow");
  assert.deepEqual(result.tools, emptyTools);
});

test("probeWslRuntime fails read on output overflow", async () => {
  const { spawnImpl } = createController([
    (call) => complete(call, { stderr: "x".repeat(70 * 1024) }),
  ]);
  const result = await probeWslRuntime({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "output-overflow");
});

test("tools stdout limit counts UTF-8 bytes, not JS string length", async () => {
  // 30k 个中文字符：string.length 只有 30000，但 UTF-8 为 90000 字节。
  const multibyte = "中".repeat(30_000);
  const { spawnImpl } = createController([
    (call) => complete(call, { stdout: "ffmpeg=/usr/bin/ffmpeg\n" + multibyte }),
  ]);
  const result = await probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "output-overflow");
  assert.deepEqual(result.tools, emptyTools);
});

test("warm fails on bounded stderr overflow even with exit 0", async () => {
  const { spawnImpl } = createController([
    (call) => complete(call, { code: 0, stderr: "中".repeat(30_000) }),
  ]);
  const result = await warmWsl({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "output-overflow");
});

test("tools fails when stderr overflows even if stdout is valid", async () => {
  const toolOutput = Object.entries(allTools).map(([k, v]) => `${k}=${v}`).join("\n");
  const { spawnImpl } = createController([
    (call) => complete(call, { stdout: toolOutput, stderr: "中".repeat(30_000) }),
  ]);
  const result = await probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "output-overflow");
  assert.deepEqual(result.tools, emptyTools);
});

test("warm failure reports a fixed reason without echoing stderr", async () => {
  const secret = "SECRET-STDERR /home/private";
  const { spawnImpl } = createController([
    (call) => complete(call, { code: 1, stderr: secret }),
  ]);
  const result = await warmWsl({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "exit=1 signal=null");
  assert.ok(!result.detail.includes(secret));
});

test("spawn error reports only the safe code, not the underlying message", async () => {
  const secret = "spawn wsl.exe ENOENT /home/private/wsl.exe";
  const { spawnImpl } = createController([
    (call) => {
      // spawn 成功前失败：没有自有 pid，error 才是终止事实。
      call.child.pid = undefined;
      queueMicrotask(() => {
        call.child.emit("error", Object.assign(new Error(secret), { code: "ENOENT" }));
      });
    },
  ]);
  const result = await warmWsl({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "spawn-error ENOENT");
  assert.ok(!result.detail.includes(secret));
});

test("live child error is not exit evidence and forces bounded cleanup", async () => {
  const { spawnImpl, calls } = createController([
    (call) => {
      call.child.kill = (signal = "SIGTERM") => {
        call.child.kills.push(signal);
        return false;
      };
      queueMicrotask(() => {
        call.child.emit(
          "error",
          Object.assign(new Error("EPERM must not leak"), { code: "EPERM" }),
        );
      });
    },
  ]);
  await assert.rejects(
    warmWsl({
      ...win32,
      spawnImpl,
      timeoutMs: 5_000,
      stopGraceMs: 5,
    }),
    (error) => {
      assert.ok(error instanceof WslProbeCleanupError);
      assert.equal(error.reason, "error");
      return true;
    },
  );
  assert.deepEqual(calls[0].child.kills, ["SIGTERM", "SIGKILL"]);
  calls[0].child.finish(0);
  await new Promise((resolve) => setImmediate(resolve));
});

test("live child error with confirmed cleanup is an ordinary transport failure", async () => {
  const { spawnImpl } = createController([
    (call) => {
      call.child.kill = (signal = "SIGTERM") => {
        call.child.kills.push(signal);
        queueMicrotask(() => call.child.finish(null, signal));
        return true;
      };
      queueMicrotask(() => {
        call.child.emit("error", Object.assign(new Error("EPERM"), { code: "EPERM" }));
      });
    },
  ]);
  const result = await warmWsl({
    ...win32,
    spawnImpl,
    timeoutMs: 5_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "spawn-error EPERM");
});

test("tools probe does not treat a non-zero exit as success", async () => {
  const { spawnImpl } = createController([
    (call) => complete(call, { code: 127, stdout: "ffmpeg=/usr/bin/ffmpeg\n" }),
  ]);
  const result = await probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "exit=127 signal=null");
  assert.deepEqual(result.tools, emptyTools);
});

test("runtime probe reports a fixed non-zero exit reason", async () => {
  const { spawnImpl } = createController([(call) => complete(call, { code: 3 })]);
  const result = await probeWslRuntime({
    ...win32,
    spawnImpl,
    timeoutMs: 1_000,
    stopGraceMs: 100,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "exit=3 signal=null");
});

test("timeout stops the transport and waits for exit before reporting", async () => {
  const { spawnImpl, calls } = createController([(call) => stopOnKill(call)]);
  const result = await warmWsl({
    ...win32,
    spawnImpl,
    timeoutMs: 20,
    stopGraceMs: 1_000,
  });

  assert.equal(result.ok, false);
  assert.equal(result.detail, "timeout after 20ms");
  assert.deepEqual(calls[0].child.kills, ["SIGTERM"]);
});

test("timeout without exit evidence rejects instead of reporting a normal timeout", async () => {
  const { spawnImpl, calls } = createController([() => undefined]);
  await assert.rejects(
    warmWsl({
      ...win32,
      spawnImpl,
      timeoutMs: 10,
      stopGraceMs: 5,
    }),
    (error) => {
      assert.ok(error instanceof WslProbeCleanupError);
      assert.equal(error.reason, "timeout");
      return true;
    },
  );
  assert.deepEqual(calls[0].child.kills, ["SIGTERM", "SIGKILL"]);
  // 晚到的退出不得改变已上报的失败。
  calls[0].child.finish(0);
  await new Promise((resolve) => setImmediate(resolve));
});

test("unconfirmed cleanup releases the parent-exit fallback on a late close", async () => {
  const before = process.listenerCount("exit");
  const { spawnImpl, calls } = createController([() => undefined]);
  await assert.rejects(
    warmWsl({
      ...win32,
      spawnImpl,
      timeoutMs: 5,
      stopGraceMs: 5,
    }),
    (error) => error.name === "WslProbeCleanupError",
  );
  // 未确认退出时保留父进程 exit 兜底。
  assert.equal(process.listenerCount("exit"), before + 1);

  // 迟到 close 解绑兜底，不永久泄漏 listener。
  calls[0].child.finish(0);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(process.listenerCount("exit"), before);
});

test("abort before spawn rejects without spawning", async () => {
  const controller = new AbortController();
  controller.abort();
  const { spawnImpl, calls } = createController([]);
  await assert.rejects(
    probeWslTools({ ...win32, spawnImpl, signal: controller.signal, stopGraceMs: 100 }),
    (error) => error.name === "AbortError",
  );
  assert.equal(calls.length, 0);
});

test("abort during warm stops before tools and runtime probes", async () => {
  const controller = new AbortController();
  const { spawnImpl, calls } = createController([(call) => stopOnKill(call)]);
  const pending = prepareWslEnvironment({
    ...win32,
    spawnImpl,
    signal: controller.signal,
    warmTimeoutMs: 5_000,
    toolsTimeoutMs: 5_000,
    runtimeTimeoutMs: 5_000,
    stopGraceMs: 1_000,
    log: () => undefined,
  });

  assert.equal(calls.length, 1);
  controller.abort();
  await assert.rejects(pending, (error) => error.name === "AbortError");
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].child.kills, ["SIGTERM"]);
});

test("abort during tools stops before the runtime probe", async () => {
  const controller = new AbortController();
  const toolsSpawned = deferred();
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { code: 0 }),
    (call) => {
      stopOnKill(call);
      toolsSpawned.resolve();
    },
  ]);
  const pending = prepareWslEnvironment({
    ...win32,
    spawnImpl,
    signal: controller.signal,
    warmTimeoutMs: 5_000,
    toolsTimeoutMs: 5_000,
    runtimeTimeoutMs: 5_000,
    stopGraceMs: 1_000,
    log: () => undefined,
  });

  await toolsSpawned.promise;
  assert.equal(calls.length, 2);
  controller.abort();
  await assert.rejects(pending, (error) => error.name === "AbortError");
  assert.equal(calls.length, 2);
});

test("abort during runtime does not start anything after the runtime probe", async () => {
  const controller = new AbortController();
  const runtimeSpawned = deferred();
  const toolOutput = Object.entries(allTools).map(([k, v]) => `${k}=${v}`).join("\n");
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { code: 0 }),
    (call) => complete(call, { stdout: toolOutput }),
    (call) => {
      stopOnKill(call);
      runtimeSpawned.resolve();
    },
  ]);
  const pending = prepareWslEnvironment({
    ...win32,
    spawnImpl,
    signal: controller.signal,
    warmTimeoutMs: 5_000,
    toolsTimeoutMs: 5_000,
    runtimeTimeoutMs: 5_000,
    stopGraceMs: 1_000,
    log: () => undefined,
  });

  await runtimeSpawned.promise;
  assert.equal(calls.length, 3);
  controller.abort();
  await assert.rejects(pending, (error) => error.name === "AbortError");
  assert.equal(calls.length, 3);
});

test("prepareWslEnvironment runs warm, tools and runtime in order", async () => {
  const logs = [];
  const secret = "SECRET-STDERR should not be logged";
  const toolOutput = Object.entries(allTools).map(([k, v]) => `${k}=${v}`).join("\n");
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { code: 0, stderr: secret }),
    (call) => complete(call, { stdout: toolOutput }),
    (call) => complete(call, { code: 0 }),
  ]);
  const result = await prepareWslEnvironment({
    ...win32,
    spawnImpl,
    warmTimeoutMs: 1_000,
    toolsTimeoutMs: 1_000,
    runtimeTimeoutMs: 1_000,
    stopGraceMs: 100,
    log: (line) => logs.push(line),
  });

  assert.equal(calls.length, 3);
  assert.match(calls[0].args.join(" "), /-e true$/);
  assert.match(calls[1].args.join(" "), /command -v/);
  assert.match(calls[2].args.join(" "), /check-runtime$/);
  assert.deepEqual(result, { tools: allTools, runtimeReady: true });
  assert.ok(logs.some((line) => line.includes("wsl warm ok")));
  assert.ok(logs.some((line) => line.includes("yaomu runtime: ok")));
  assert.ok(!logs.some((line) => line.includes(secret)));
});

test("prepareWslEnvironment skips probes on non-Windows", async () => {
  const { spawnImpl, calls } = createController([]);
  const result = await prepareWslEnvironment({
    platform: "darwin",
    env: {},
    spawnImpl,
    log: () => undefined,
  });

  assert.equal(calls.length, 0);
  assert.deepEqual(result, { tools: emptyTools, runtimeReady: true });
});

test("a real slow child does not block the event loop or settle early", async () => {
  const spawnImpl = (_command, _args, options) =>
    realSpawn(process.execPath, ["-e", "setTimeout(() => process.exit(0), 200)"], options);
  const timerFired = deferred();
  setTimeout(() => timerFired.resolve(), 0);
  const pending = probeWslTools({
    ...win32,
    spawnImpl,
    timeoutMs: 5_000,
    stopGraceMs: 1_000,
  });

  await timerFired.promise;
  // 真实 child 仍在 200ms 睡眠时，probe 不应已完成；同步 spawn 会卡住这里。
  const settledEarly = await Promise.race([pending.then(() => true), Promise.resolve(false)]);
  assert.equal(settledEarly, false);

  const result = await pending;
  assert.equal(result.ok, true);
  assert.deepEqual(result.tools, emptyTools);
});

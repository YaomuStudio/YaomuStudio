import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const {
  CoreBootstrapError,
  isProcessAlive,
  startCore,
  terminateProcessTree,
} = require("../dist/bootstrap.js");

const fakeCore = fileURLToPath(new URL("./fixtures/fake-core.cjs", import.meta.url));
const slowCore = fileURLToPath(new URL("./fixtures/fake-core-slow.cjs", import.meta.url));
const token = "test-token-not-a-secret";

async function withTempDir(fn) {
  const tmp = await mkdtemp(path.join(os.tmpdir(), "yaomu-lifecycle-"));
  try {
    return await fn(tmp);
  } finally {
    await rm(tmp, { recursive: true, force: true });
  }
}

async function waitUntil(check, timeoutMs = 5_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error("等待条件超时");
}

/** 推进若干轮事件循环；不用固定 sleep 猜测并发顺序。 */
async function drain(rounds = 12) {
  for (let i = 0; i < rounds; i += 1) {
    await new Promise((resolve) => setImmediate(resolve));
  }
}

/**
 * 用模块 stub 给第一次 bootstrap.json 写加确定性 gate。
 * 编译后的 bootstrap 在调用时读取 fs/promises.writeFile，因此这里可无侵入注入。
 */
function installBootstrapWriteGate() {
  const fsp = require("node:fs/promises");
  const original = fsp.writeFile;
  const states = [];
  let active = 0;
  let maxActive = 0;
  let gateNext = true;
  let markStarted;
  const firstStarted = new Promise((resolve) => {
    markStarted = resolve;
  });
  let releaseGate;
  const gateReleased = new Promise((resolve) => {
    releaseGate = resolve;
  });

  fsp.writeFile = async (target, data, options) => {
    if (typeof target !== "string" || path.basename(target) !== "bootstrap.json") {
      return original(target, data, options);
    }
    states.push(JSON.parse(String(data)).state);
    active += 1;
    maxActive = Math.max(maxActive, active);
    try {
      if (gateNext) {
        gateNext = false;
        markStarted();
        await gateReleased;
      }
      return await original(target, data, options);
    } finally {
      active -= 1;
    }
  };

  return {
    states,
    firstStarted,
    releaseFirst: () => releaseGate(),
    maxActive: () => maxActive,
    restore: () => {
      fsp.writeFile = original;
    },
  };
}

function captureEnv(tmp) {
  return { YAOMU_TEST_CAPTURE_ENV: path.join(tmp, "capture.json") };
}

test("正常启动写入 bootstrap.json，成功时保留父进程 exit 兜底直到 child 退出", async () => {
  await withTempDir(async (tmp) => {
    const baseline = process.listenerCount("exit");
    const info = await startCore({
      dataDir: path.join(tmp, "data"),
      packaged: false,
      token,
      launch: { command: process.execPath, args: [fakeCore], cwd: tmp },
      env: captureEnv(tmp),
      healthTimeoutMs: 15_000,
      healthIntervalMs: 25,
      log: () => undefined,
    });

    // 就绪后仍需父进程 exit 兜底清理。
    assert.equal(process.listenerCount("exit"), baseline + 1);
    const bootstrap = JSON.parse(
      await readFile(path.join(tmp, "data", "bootstrap.json"), "utf8"),
    );
    assert.equal(bootstrap.core_pid, info.pid);
    assert.equal(bootstrap.core_port, info.port);

    await terminateProcessTree(info.child, 2_000);
    // child 退出后兜底监听必须解绑，不留下跨实例泄漏。
    await waitUntil(() => process.listenerCount("exit") === baseline);
  });
});

test("startCore 遇到 ENOENT 失败后不遗留父进程 exit 监听", async () => {
  await withTempDir(async (tmp) => {
    const baseline = process.listenerCount("exit");
    await assert.rejects(
      startCore({
        dataDir: path.join(tmp, "data"),
        packaged: false,
        token,
        launch: { command: path.join(tmp, "missing-core-binary"), args: [], cwd: tmp },
        healthTimeoutMs: 1_000,
        healthIntervalMs: 25,
        log: () => undefined,
      }),
      (error) => error instanceof CoreBootstrapError && error.code === "CORE_NOT_READY",
    );
    await waitUntil(() => process.listenerCount("exit") === baseline);
  });
});

test("健康检查失败会终止自有 child，并清理父进程 exit 监听", async () => {
  await withTempDir(async (tmp) => {
    const pidFile = path.join(tmp, "core.pid");
    const baseline = process.listenerCount("exit");
    await assert.rejects(
      startCore({
        dataDir: path.join(tmp, "data"),
        packaged: false,
        token,
        launch: { command: process.execPath, args: [slowCore], cwd: tmp },
        env: { YAOMU_TEST_PID_FILE: pidFile },
        healthTimeoutMs: 300,
        healthIntervalMs: 25,
        log: () => undefined,
      }),
      (error) => error instanceof CoreBootstrapError && error.code === "CORE_NOT_READY",
    );

    const pid = Number(await readFile(pidFile, "utf8"));
    assert.equal(isProcessAlive(pid), false);
    await waitUntil(() => process.listenerCount("exit") === baseline);
  });
});

test("启动取消在 spawn 前生效：预取消不创建子进程", async () => {
  await withTempDir(async (tmp) => {
    const pidFile = path.join(tmp, "core.pid");
    const baseline = process.listenerCount("exit");
    const controller = new AbortController();
    controller.abort();

    await assert.rejects(
      startCore({
        dataDir: path.join(tmp, "data"),
        packaged: false,
        token,
        launch: { command: process.execPath, args: [slowCore], cwd: tmp },
        env: { YAOMU_TEST_PID_FILE: pidFile },
        signal: controller.signal,
        healthTimeoutMs: 1_000,
        healthIntervalMs: 25,
        log: () => undefined,
      }),
      (error) => error instanceof Error && error.name === "AbortError",
    );

    assert.equal(existsSync(pidFile), false);
    assert.equal(process.listenerCount("exit"), baseline);
  });
});

test("启动取消会真正终止自有慢启动 child，并在确认退出后才 reject", async () => {
  await withTempDir(async (tmp) => {
    const pidFile = path.join(tmp, "core.pid");
    const controller = new AbortController();
    const baseline = process.listenerCount("exit");

    const promise = startCore({
      dataDir: path.join(tmp, "data"),
      packaged: false,
      token,
      launch: { command: process.execPath, args: [slowCore], cwd: tmp },
      env: { YAOMU_TEST_PID_FILE: pidFile },
      signal: controller.signal,
      healthTimeoutMs: 5_000,
      healthIntervalMs: 25,
      log: () => undefined,
    });
    // 先挂 handler，避免取消与断言之间出现未处理 rejection。
    promise.catch(() => undefined);

    await waitUntil(() => existsSync(pidFile));
    const pid = Number(await readFile(pidFile, "utf8"));
    assert.equal(isProcessAlive(pid), true);

    controller.abort();
    await assert.rejects(promise, (error) => error instanceof Error && error.name === "AbortError");

    // reject 时自有 child 必须已经退出，而不是只发出了 abort 信号。
    assert.equal(isProcessAlive(pid), false);
    // 取消发生在 ready 发布前：不创建 bootstrap.json。
    assert.equal(existsSync(path.join(tmp, "data", "bootstrap.json")), false);
    await waitUntil(() => process.listenerCount("exit") === baseline);
  });
});

test("取消在 ready 写期间：startCore 等写完成才 reject，且不覆盖新代次状态", async () => {
  const gate = installBootstrapWriteGate();
  let newCore = null;
  try {
    await withTempDir(async (tmp) => {
      const dataDir = path.join(tmp, "data");
      const controller = new AbortController();
      let settled = false;
      const promise = startCore({
        dataDir,
        packaged: false,
        token,
        launch: { command: process.execPath, args: [fakeCore], cwd: tmp },
        env: captureEnv(tmp),
        signal: controller.signal,
        healthTimeoutMs: 15_000,
        healthIntervalMs: 20,
        log: () => undefined,
      });
      promise.then(
        () => {
          settled = true;
        },
        () => {
          settled = true;
        },
      );
      promise.catch(() => undefined);

      // 第一次 ready 写已被 gate 卡住。
      await gate.firstStarted;
      controller.abort();
      await drain();
      // 写未完成前 startCore 不得先 settle，否则 Supervisor 会放行新代次。
      assert.equal(settled, false);

      gate.releaseFirst();
      await assert.rejects(
        promise,
        (error) => error instanceof Error && error.name === "AbortError",
      );

      // 串行：ready 完成后才写 stopped，没有并行写同一文件。
      assert.deepEqual(gate.states, ["ready", "stopped"]);
      assert.equal(gate.maxActive(), 1);
      const stopped = JSON.parse(
        await readFile(path.join(dataDir, "bootstrap.json"), "utf8"),
      );
      assert.equal(stopped.state, "stopped");

      // 旧代次已在 reject 前结束所有写；新代次 ready 不得被旧写覆盖。
      newCore = await startCore({
        dataDir,
        packaged: false,
        token: "new-generation-token",
        launch: { command: process.execPath, args: [fakeCore], cwd: tmp },
        env: captureEnv(tmp),
        healthTimeoutMs: 15_000,
        healthIntervalMs: 20,
        log: () => undefined,
      });
      const ready = JSON.parse(
        await readFile(path.join(dataDir, "bootstrap.json"), "utf8"),
      );
      assert.equal(ready.state, "ready");
      assert.equal(ready.core_pid, newCore.pid);
      await drain();
      const afterDrain = JSON.parse(
        await readFile(path.join(dataDir, "bootstrap.json"), "utf8"),
      );
      assert.equal(afterDrain.state, "ready");
      assert.equal(afterDrain.core_pid, newCore.pid);
    });
  } finally {
    if (newCore) await terminateProcessTree(newCore.child, 2_000).catch(() => undefined);
    gate.restore();
  }
});

test("ready 写期间 child 先退出：等写完成并串行纠正 crashed，不留 ready 残留", async () => {
  const gate = installBootstrapWriteGate();
  try {
    await withTempDir(async (tmp) => {
      const dataDir = path.join(tmp, "data");
      const capturePath = path.join(tmp, "capture.json");
      let settled = false;
      const promise = startCore({
        dataDir,
        packaged: false,
        token,
        launch: { command: process.execPath, args: [fakeCore], cwd: tmp },
        env: { YAOMU_TEST_CAPTURE_ENV: capturePath },
        healthTimeoutMs: 15_000,
        healthIntervalMs: 20,
        log: () => undefined,
      });
      promise.then(
        () => {
          settled = true;
        },
        () => {
          settled = true;
        },
      );
      promise.catch(() => undefined);

      await gate.firstStarted;
      const captured = JSON.parse(await readFile(capturePath, "utf8"));
      process.kill(captured.pid, "SIGTERM");
      await waitUntil(() => !isProcessAlive(captured.pid));
      await drain();
      // 写被 gate 卡住时 child 已退出：startCore 仍不能先 settle。
      assert.equal(settled, false);

      gate.releaseFirst();
      await assert.rejects(promise, /在就绪前退出/);

      // ready 写完成后串行纠正为 crashed，不留下 ready 残留。
      assert.deepEqual(gate.states, ["ready", "crashed"]);
      assert.equal(gate.maxActive(), 1);
      const snapshot = JSON.parse(
        await readFile(path.join(dataDir, "bootstrap.json"), "utf8"),
      );
      assert.equal(snapshot.state, "crashed");
      await drain();
      const afterDrain = JSON.parse(
        await readFile(path.join(dataDir, "bootstrap.json"), "utf8"),
      );
      assert.equal(afterDrain.state, "crashed");
    });
  } finally {
    gate.restore();
  }
});

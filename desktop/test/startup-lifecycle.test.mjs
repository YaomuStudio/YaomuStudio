import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  StartupLifecycle,
  StartupSettleTimeoutError,
  convergeShutdown,
} = require("../dist/startup-lifecycle.js");

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((next, fail) => {
    resolve = next;
    reject = fail;
  });
  return { promise, resolve, reject };
}

/** 不睡眠：只在 signal 已取消或收到 abort 事件时继续。 */
function aborted(signal) {
  if (signal.aborted) return Promise.resolve();
  return new Promise((resolve) => {
    signal.addEventListener("abort", resolve, { once: true });
  });
}

test("run executes the initialization once and shares the in-flight promise", async () => {
  const lifecycle = new StartupLifecycle();
  const gate = deferred();
  let calls = 0;
  const first = lifecycle.run(async () => {
    calls += 1;
    await gate.promise;
  });
  const second = lifecycle.run(async () => {
    calls += 1;
  });

  assert.equal(lifecycle.started, true);
  assert.equal(first, second);
  gate.resolve();
  await first;
  assert.equal(calls, 1);
});

test("cancel before run passes an already aborted signal", async () => {
  const lifecycle = new StartupLifecycle();
  lifecycle.cancel();
  let sawAborted = null;
  await lifecycle.run(async (signal) => {
    sawAborted = signal.aborted;
  });

  assert.equal(lifecycle.cancelled, true);
  assert.equal(sawAborted, true);
});

test("settle waits for the cancelled initialization to converge", async () => {
  const lifecycle = new StartupLifecycle();
  const cleanupGate = deferred();
  let settled = false;
  const task = lifecycle.run(async (signal) => {
    await aborted(signal);
    await cleanupGate.promise;
  });

  lifecycle.cancel();
  const settledPromise = lifecycle.settle().then(() => {
    settled = true;
  });
  // 让取消事件与 settle 的等待都进入队列；此时清理尚未完成。
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(settled, false);

  cleanupGate.resolve();
  await settledPromise;
  assert.equal(settled, true);
  await task;
  assert.equal(lifecycle.cancelled, true);
});

test("settle before run returns immediately", async () => {
  const lifecycle = new StartupLifecycle();
  await lifecycle.settle();
  assert.equal(lifecycle.started, false);
  assert.equal(lifecycle.cancelled, false);
});

test("settle swallows initialization rejection so before-quit can continue", async () => {
  const lifecycle = new StartupLifecycle();
  const run = lifecycle.run(async () => {
    throw new Error("init failed");
  });
  await assert.rejects(run, /init failed/);
  await lifecycle.settle();
});

test("settle times out, preserves the task, and a later settle can converge", async () => {
  const lifecycle = new StartupLifecycle();
  const gate = deferred();
  lifecycle.run(() => gate.promise);
  lifecycle.cancel();

  await assert.rejects(lifecycle.settle(5), (error) => {
    assert.ok(error instanceof StartupSettleTimeoutError);
    assert.equal(error.timeoutMs, 5);
    return true;
  });
  // 未收敛事实保留：启动标记不清、task 仍在。
  assert.equal(lifecycle.started, true);
  assert.equal(lifecycle.pending, true);

  gate.resolve();
  await lifecycle.settle(1_000);
  assert.equal(lifecycle.pending, false);
});

test("convergeShutdown bounds pending init and observes stop rejection", async () => {
  const lifecycle = new StartupLifecycle();
  const pending = deferred();
  lifecycle.run(() => pending.promise);
  lifecycle.cancel();

  const stopError = new Error("supervisor stop failed");
  const unhandled = [];
  const onUnhandled = (error) => unhandled.push(error);
  process.on("unhandledRejection", onUnhandled);
  try {
    const convergence = await convergeShutdown({
      startup: lifecycle,
      stopSupervisor: () => Promise.reject(stopError),
      settleTimeoutMs: 5,
    });

    assert.equal(convergence.startup.kind, "timeout");
    assert.equal(convergence.startup.error.name, "StartupSettleTimeoutError");
    assert.equal(convergence.supervisor.kind, "failed");
    assert.equal(convergence.supervisor.error, stopError);
    assert.equal(lifecycle.pending, true);

    // 等待一轮事件循环：stop 拒绝必须已被立即接收，不能变成未处理拒绝。
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(unhandled, []);
  } finally {
    process.removeListener("unhandledRejection", onUnhandled);
    pending.resolve();
    await lifecycle.settle(1_000);
  }
});

test("convergeShutdown reports settled init and stopped supervisor", async () => {
  const lifecycle = new StartupLifecycle();
  lifecycle.run(async () => undefined);
  const convergence = await convergeShutdown({
    startup: lifecycle,
    stopSupervisor: async () => undefined,
    settleTimeoutMs: 100,
  });

  assert.equal(convergence.startup.kind, "settled");
  assert.equal(convergence.supervisor.kind, "stopped");
});

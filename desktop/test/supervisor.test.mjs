import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const { RuntimeSupervisor } = require("../dist/supervisor.js");
const { CoreBootstrapError } = require("../dist/bootstrap.js");

const STOP_TIMEOUT_MS = 200;
const SETTLE_TIMEOUT_MS = 1_000;
// 远高于常见 pid 上限，process.kill(-pid) 必然失败并落到 child.kill，
// 让假 child 可以像真实进程一样在收到信号后发出 exit。
const FAKE_PID = 2_000_000_000;

class FakeChild extends EventEmitter {
  constructor() {
    super();
    this.pid = FAKE_PID;
    this.exitCode = null;
    this.signalCode = null;
  }

  kill(signal = "SIGTERM") {
    if (this.exitCode !== null || this.signalCode !== null) return false;
    setImmediate(() => this.exit(signal));
    return true;
  }

  exit(signal = "SIGTERM") {
    if (this.exitCode !== null || this.signalCode !== null) return;
    this.signalCode = signal;
    this.emit("exit", null, signal);
  }
}

function makeInfo(child, id) {
  const port = 41_000 + id;
  return {
    child,
    port,
    token: `token-${id}`,
    baseUrl: `http://127.0.0.1:${port}`,
    pid: child.pid,
    startedAt: new Date().toISOString(),
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** 每次 launch 都创建一个可控 deferred 与自己的假 child。 */
function controlledLauncher() {
  const calls = [];
  const launcher = (signal) => {
    const call = { signal, deferred: deferred(), child: new FakeChild() };
    calls.push(call);
    return call.deferred.promise;
  };
  return { launcher, calls };
}

/** 遵循 Bootstrap 契约：收到 abort 后确认清理并 reject AbortError。 */
function abortAwareLauncher() {
  const calls = [];
  const launcher = (signal) => {
    let resolve;
    let reject;
    const promise = new Promise((res, rej) => {
      resolve = res;
      reject = rej;
    });
    const call = { signal, child: new FakeChild(), aborted: false, resolve, reject, promise };
    const onAbort = () => {
      call.aborted = true;
      reject(Object.assign(new Error("启动已取消"), { name: "AbortError" }));
    };
    if (signal.aborted) onAbort();
    else signal.addEventListener("abort", onAbort, { once: true });
    calls.push(call);
    return promise;
  };
  return { launcher, calls };
}

/** kill 只记录信号，退出由测试显式 release；用于观察 stop 是否共享同一个等待。 */
class GatedFakeChild extends FakeChild {
  constructor() {
    super();
    this.kills = 0;
    this.pendingSignal = null;
  }

  kill(signal = "SIGTERM") {
    if (this.exitCode !== null || this.signalCode !== null) return false;
    this.kills += 1;
    this.pendingSignal = signal;
    return true;
  }

  release() {
    this.exit(this.pendingSignal ?? "SIGTERM");
  }
}

/** 推进若干轮事件循环，不依赖固定 sleep 猜测并发顺序。 */
async function drain(rounds = 8) {
  for (let i = 0; i < rounds; i += 1) {
    await new Promise((resolve) => setImmediate(resolve));
  }
}

async function waitUntil(check, timeoutMs = 2_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setImmediate(resolve));
  }
  throw new Error("等待条件超时");
}

test("ready 后的 start 返回同一个 CoreInfo，不重复拉起", async () => {
  let launches = 0;
  const supervisor = new RuntimeSupervisor({
    launcher: async () => {
      launches += 1;
      return makeInfo(new FakeChild(), launches);
    },
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = await supervisor.start();
  const second = await supervisor.start();

  assert.equal(launches, 1);
  assert.equal(second, first);
  assert.equal(supervisor.getState(), "ready");
  assert.equal(supervisor.getConnection()?.token, first.token);
});

test("并发 start 共享同一次启动", async () => {
  const { launcher, calls } = controlledLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const a = supervisor.start();
  const b = supervisor.start();
  assert.equal(calls.length, 1);

  calls[0].deferred.resolve(makeInfo(calls[0].child, 1));
  const [infoA, infoB] = await Promise.all([a, b]);

  assert.equal(calls.length, 1);
  assert.equal(infoA, infoB);
  assert.equal(supervisor.getState(), "ready");
});

test("自动重启与显式 start 合流，不产生第二个 Core", async () => {
  const { launcher, calls } = controlledLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = supervisor.start();
  calls[0].deferred.resolve(makeInfo(calls[0].child, 1));
  await first;

  // 意外退出触发自动重启；重启仍在进行中时显式 start 必须共享它。
  calls[0].child.exit("SIGKILL");
  assert.equal(calls.length, 2);

  const retried = supervisor.start();
  assert.equal(calls.length, 2);

  calls[1].deferred.resolve(makeInfo(calls[1].child, 2));
  const info = await retried;

  assert.equal(calls.length, 2);
  assert.equal(info.port, 41_002);
  assert.equal(supervisor.getState(), "ready");
});

test("外部 stop 优先于更早的自动重启，迟到的 child 会被终止", async () => {
  const { launcher, calls } = controlledLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = supervisor.start();
  calls[0].deferred.resolve(makeInfo(calls[0].child, 1));
  await first;

  calls[0].child.exit("SIGKILL");
  assert.equal(calls.length, 2);

  let stopped = false;
  const stopPromise = supervisor.stop().then(() => {
    stopped = true;
  });

  await drain();
  // 自动重启的 launcher 尚未返回，stop 不能先宣称已完成。
  assert.equal(stopped, false);
  assert.equal(supervisor.getConnection(), null);

  const lateChild = calls[1].child;
  calls[1].deferred.resolve(makeInfo(lateChild, 2));
  await stopPromise;

  assert.equal(stopped, true);
  assert.equal(supervisor.getState(), "stopped");
  assert.equal(supervisor.getConnection(), null);
  assert.notEqual(lateChild.signalCode, null);

  await drain();
  // 迟到的 ready/crashed 回调不能污染已停止状态。
  assert.equal(supervisor.getState(), "stopped");
});

test("stop 在 stopping 状态回调时已经断开连接", async () => {
  let supervisor;
  let stoppingConnection = "unset";
  supervisor = new RuntimeSupervisor({
    launcher: async () => makeInfo(new FakeChild(), 1),
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
    onState: (state) => {
      if (state === "stopping" && stoppingConnection === "unset") {
        stoppingConnection = supervisor.getConnection();
      }
    },
  });

  await supervisor.start();
  assert.notEqual(supervisor.getConnection(), null);
  await supervisor.stop();

  assert.equal(stoppingConnection, null);
  assert.equal(supervisor.getConnection(), null);
});

test("stop 等待进行中启动的 cleanup，期间拒绝新 start", async () => {
  const cleanupGate = deferred();
  const launcher = (signal) => {
    return new Promise((resolve, reject) => {
      signal.addEventListener(
        "abort",
        () => {
          // 真实 cleanup 需要时间：stop 必须等它完成。
          void cleanupGate.promise.then(() => {
            reject(Object.assign(new Error("启动已取消"), { name: "AbortError" }));
          });
        },
        { once: true },
      );
    });
  };
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);

  const stopPromise = supervisor.stop();
  let stopped = false;
  void stopPromise.then(() => {
    stopped = true;
  });

  await drain();
  assert.equal(stopped, false);
  assert.equal(supervisor.getConnection(), null);
  await assert.rejects(supervisor.start(), /正在停止|停止尚未确认/);

  cleanupGate.resolve();
  await stopPromise;
  assert.equal(supervisor.getState(), "stopped");
});

test("并发 stop 共享同一次等待", async () => {
  const cleanupGate = deferred();
  let aborts = 0;
  const launcher = (signal) => {
    return new Promise((resolve, reject) => {
      signal.addEventListener(
        "abort",
        () => {
          aborts += 1;
          void cleanupGate.promise.then(() => {
            reject(Object.assign(new Error("启动已取消"), { name: "AbortError" }));
          });
        },
        { once: true },
      );
    });
  };
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);

  let firstDone = false;
  let secondDone = false;
  const first = supervisor.stop().then(() => {
    firstDone = true;
  });
  const second = supervisor.stop().then(() => {
    secondDone = true;
  });

  await drain();
  assert.equal(firstDone, false);
  assert.equal(secondDone, false);

  cleanupGate.resolve();
  await Promise.all([first, second]);
  assert.equal(aborts, 1);
  assert.equal(supervisor.getState(), "stopped");
});

test("启动清理无法确认后，第二次 stop 与 restart 都不能解锁新启动", async () => {
  let launches = 0;
  const launcher = (signal) => {
    launches += 1;
    return new Promise((resolve, reject) => {
      signal.addEventListener(
        "abort",
        () => {
          setImmediate(() => {
            reject(
              new CoreBootstrapError("CORE_CLEANUP_FAILED", "无法确认测试进程已退出"),
            );
          });
        },
        { once: true },
      );
    });
  };
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);

  await assert.rejects(supervisor.stop(), /无法确认测试进程已退出/);
  assert.notEqual(supervisor.getState(), "stopped");
  assert.equal(supervisor.getConnection(), null);

  // 没有退出证据时，空 stop 不得清除隔离事实。
  await assert.rejects(supervisor.stop(), /无法确认测试进程已退出/);
  assert.notEqual(supervisor.getState(), "stopped");
  await assert.rejects(supervisor.start(), /停止尚未确认/);
  await assert.rejects(supervisor.restart(), /无法确认测试进程已退出/);
  assert.equal(launches, 1);
});

test("健康失败清理无法确认时，后续 start/stop 都保持隔离", async () => {
  let launches = 0;
  const launcher = async () => {
    launches += 1;
    throw new CoreBootstrapError("CORE_CLEANUP_FAILED", "健康失败后无法确认进程退出");
  };
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  await assert.rejects(supervisor.start(), /健康失败后无法确认进程退出/);
  assert.equal(supervisor.getState(), "crashed");
  assert.equal(supervisor.getConnection(), null);

  await assert.rejects(supervisor.start(), /停止尚未确认/);
  await assert.rejects(supervisor.stop(), /健康失败后无法确认进程退出/);
  await assert.rejects(supervisor.stop(), /健康失败后无法确认进程退出/);
  assert.equal(launches, 1);
});

test("onState(starting) 重入 stop 使用已发布的启动 Promise", async () => {
  let supervisor;
  let stopPromise = null;
  let launches = 0;
  supervisor = new RuntimeSupervisor({
    launcher: (signal) => {
      launches += 1;
      return new Promise((resolve, reject) => {
        const cancel = () =>
          reject(Object.assign(new Error("启动已取消"), { name: "AbortError" }));
        if (signal.aborted) cancel();
        else signal.addEventListener("abort", cancel, { once: true });
      });
    },
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
    onState: (state) => {
      if (state === "starting" && !stopPromise) {
        stopPromise = supervisor.stop();
        stopPromise.catch(() => undefined);
      }
    },
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);
  assert.ok(stopPromise);
  // stop 必须拿到已发布的 attempt Promise，而不是 undefined 并抛 TypeError。
  await stopPromise;
  assert.equal(supervisor.getState(), "stopped");
  assert.equal(launches, 0);
  await assert.rejects(startPromise, (error) => error.name === "AbortError");
});

test("自动重启 onState(restarting) 重入 start 不产生第二次 launch", async () => {
  const { launcher, calls } = controlledLauncher();
  let supervisor;
  let reentered = false;
  let reentrantStart = null;
  supervisor = new RuntimeSupervisor({
    launcher,
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
    onState: (state) => {
      if (state === "restarting" && !reentered) {
        reentered = true;
        reentrantStart = supervisor.start();
        reentrantStart.catch(() => undefined);
      }
    },
  });

  const first = supervisor.start();
  calls[0].deferred.resolve(makeInfo(calls[0].child, 1));
  await first;

  calls[0].child.exit("SIGKILL");
  await drain();
  // 重入 start 必须共享已发布的自动重启，而不是再拉起一次。
  assert.equal(calls.length, 2);

  calls[1].deferred.resolve(makeInfo(calls[1].child, 2));
  const info = await reentrantStart;
  assert.equal(calls.length, 2);
  assert.equal(info.port, 41_002);
  assert.equal(supervisor.getState(), "ready");
});

test("自动重启 onState(restarting) 重入 stop 后不再拉起新 Core", async () => {
  const { launcher, calls } = controlledLauncher();
  let supervisor;
  let stopPromise = null;
  supervisor = new RuntimeSupervisor({
    launcher,
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
    onState: (state) => {
      if (state === "restarting" && !stopPromise) {
        stopPromise = supervisor.stop();
        stopPromise.catch(() => undefined);
      }
    },
  });

  const first = supervisor.start();
  calls[0].deferred.resolve(makeInfo(calls[0].child, 1));
  await first;

  calls[0].child.exit("SIGKILL");
  await drain();
  assert.ok(stopPromise);
  await stopPromise;
  assert.equal(calls.length, 1);
  assert.equal(supervisor.getState(), "stopped");
  assert.equal(supervisor.getConnection(), null);
});

test("launcher 永不 settle 时 stop 有界失败，迟到 child 仍被清理", async () => {
  const { launcher, calls } = controlledLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: 20,
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);

  await assert.rejects(supervisor.stop(), /停止未确认|未完成清理/);
  assert.notEqual(supervisor.getState(), "stopped");
  assert.equal(supervisor.getConnection(), null);
  await assert.rejects(supervisor.start(), /停止尚未确认/);

  // 超时后不能清空对启动的拥有权：迟返回的 child 仍必须终止。
  const lateChild = calls[0].child;
  calls[0].deferred.resolve(makeInfo(lateChild, 1));
  await waitUntil(() => lateChild.signalCode !== null);
  await drain();
  assert.notEqual(supervisor.getState(), "stopped");

  // 迟到清理确认后，再次 stop 才能恢复 stopped 并允许新启动。
  await supervisor.stop();
  assert.equal(supervisor.getState(), "stopped");

  const secondStart = supervisor.start();
  await drain();
  assert.equal(calls.length, 2);
  calls[1].deferred.resolve(makeInfo(calls[1].child, 2));
  await secondStart;
});

test("初始 start pending 时 restart 取消旧启动，只拉起一次新 Core", async () => {
  const { launcher, calls } = abortAwareLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const startPromise = supervisor.start();
  startPromise.catch(() => undefined);
  assert.equal(calls.length, 1);

  const restartPromise = supervisor.restart();
  restartPromise.catch(() => undefined);
  await drain();

  assert.equal(calls.length, 2);
  assert.equal(calls[0].aborted, true);
  await assert.rejects(startPromise, (error) => error.name === "AbortError");

  calls[1].resolve(makeInfo(calls[1].child, 2));
  const info = await restartPromise;
  assert.equal(info.port, 41_002);
  assert.equal(calls.length, 2);
  assert.equal(supervisor.getState(), "ready");
});

test("自动重启 pending 时 restart 取消它并只拉起一次新 Core", async () => {
  const { launcher, calls } = abortAwareLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = supervisor.start();
  calls[0].resolve(makeInfo(calls[0].child, 1));
  await first;
  calls[0].child.exit("SIGKILL");
  assert.equal(calls.length, 2);

  const restartPromise = supervisor.restart();
  restartPromise.catch(() => undefined);
  await drain();

  assert.equal(calls[1].aborted, true);
  assert.equal(calls.length, 3);

  calls[2].resolve(makeInfo(calls[2].child, 3));
  const info = await restartPromise;
  assert.equal(info.port, 41_003);
  assert.equal(calls.length, 3);
  assert.equal(supervisor.getState(), "ready");
  assert.notEqual(calls[0].child.signalCode, null);
});

test("两次并发 restart 只产生一次新启动，旧 child 被清理", async () => {
  const { launcher, calls } = abortAwareLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = supervisor.start();
  calls[0].resolve(makeInfo(calls[0].child, 1));
  await first;

  const r1 = supervisor.restart();
  const r2 = supervisor.restart();
  r1.catch(() => undefined);
  r2.catch(() => undefined);
  await drain();

  assert.equal(calls.length, 2);
  assert.notEqual(calls[0].child.signalCode, null);

  calls[1].resolve(makeInfo(calls[1].child, 2));
  const results = await Promise.allSettled([r1, r2]);
  const fulfilled = results.filter((result) => result.status === "fulfilled");
  const rejected = results.filter((result) => result.status === "rejected");
  assert.equal(fulfilled.length, 1);
  assert.equal(rejected.length, 1);
  assert.match(String(rejected[0].reason?.message), /被后续停止请求取代/);
  assert.equal(fulfilled[0].value.port, 41_002);
  assert.equal(calls.length, 2);
  assert.equal(supervisor.getState(), "ready");
});

test("restart pending 后外部 stop 优先，不再拉起新 Core", async () => {
  const { launcher, calls } = abortAwareLauncher();
  const supervisor = new RuntimeSupervisor({
    launcher,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
  });

  const first = supervisor.start();
  calls[0].resolve(makeInfo(calls[0].child, 1));
  await first;

  const restartPromise = supervisor.restart();
  restartPromise.catch(() => undefined);
  const externalStop = supervisor.stop();
  externalStop.catch(() => undefined);

  await assert.rejects(restartPromise, /被后续停止请求取代/);
  await externalStop;
  assert.equal(calls.length, 1);
  assert.equal(supervisor.getState(), "stopped");
  assert.equal(supervisor.getConnection(), null);
  assert.notEqual(calls[0].child.signalCode, null);
});

test("onState(stopping) 重入 stop 共享同一次等待", async () => {
  const child = new GatedFakeChild();
  let supervisor;
  let reentrantStop = null;
  supervisor = new RuntimeSupervisor({
    launcher: async () => makeInfo(child, 1),
    stopTimeoutMs: 5_000,
    settleTimeoutMs: 5_000,
    onState: (state) => {
      if (state === "stopping" && !reentrantStop) {
        reentrantStop = supervisor.stop();
        reentrantStop.catch(() => undefined);
      }
    },
  });

  await supervisor.start();
  assert.equal(reentrantStop, null);

  let firstSettled = false;
  let secondSettled = false;
  const firstStop = supervisor.stop();
  firstStop.then(
    () => {
      firstSettled = true;
    },
    () => {
      firstSettled = true;
    },
  );
  firstStop.catch(() => undefined);

  assert.ok(reentrantStop);
  reentrantStop.then(
    () => {
      secondSettled = true;
    },
    () => {
      secondSettled = true;
    },
  );

  await drain();
  // child 尚未退出：两次 stop 必须等待同一次清理，且只终止一次。
  assert.equal(firstSettled, false);
  assert.equal(secondSettled, false);
  assert.equal(child.kills, 1);

  child.release();
  await Promise.all([firstStop, reentrantStop]);
  assert.equal(firstSettled, true);
  assert.equal(secondSettled, true);
  assert.equal(child.kills, 1);
  assert.equal(supervisor.getState(), "stopped");
});

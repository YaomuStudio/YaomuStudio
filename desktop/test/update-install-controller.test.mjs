import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  UpdateInstallController,
  createUpdatePreparationClient,
  isUpdateInstallSnapshot,
} = require("../dist/update-install.js");

const TOKEN_A = ["fake", "instance", "token"].join("-");
const TOKEN_B = ["rotated", "instance", "token"].join("-");

const flush = () => new Promise((resolve) => setImmediate(resolve));

function runtimeAccess(overrides = {}) {
  return {
    identity: {
      workspaceId: "ws-1",
      instanceId: "instance-1",
      serverEpoch: 3,
      generation: 1,
      ...(overrides.identity ?? {}),
    },
    connection: {
      baseUrl: "http://127.0.0.1:4310",
      token: TOKEN_A,
      ...(overrides.connection ?? {}),
    },
  };
}

/** 受控定时器：测试显式触发下一次 check，不依赖真实 2s 间隔。 */
function manualScheduler() {
  const pending = new Map();
  let nextId = 0;
  return {
    schedule(callback) {
      const id = ++nextId;
      pending.set(id, callback);
      return id;
    },
    cancel(handle) {
      pending.delete(handle);
    },
    get size() {
      return pending.size;
    },
    runNext() {
      const entry = pending.entries().next().value;
      if (!entry) return false;
      const [id, callback] = entry;
      pending.delete(id);
      callback();
      return true;
    },
  };
}

/** 受控 prepare 客户端：调用方显式 resolve 有限结果。 */
function controlledPrepare() {
  const calls = [];
  return {
    calls,
    fn(connection, request, signal) {
      return new Promise((resolve) => {
        calls.push({ connection, request, signal, resolve });
      });
    },
  };
}

function controlledStop() {
  const calls = [];
  return {
    calls,
    fn: () =>
      new Promise((resolve, reject) => {
        calls.push({ resolve, reject });
      }),
  };
}

function runtimeCell(initial = runtimeAccess()) {
  let current = initial;
  return {
    get: () => current,
    set: (next) => {
      current = next;
    },
  };
}

function setup(overrides = {}) {
  const scheduler = manualScheduler();
  const prepare = controlledPrepare();
  const stop = controlledStop();
  const cell = runtimeCell();
  const installs = [];
  const snapshots = [];
  let downloaded = Object.prototype.hasOwnProperty.call(overrides, "downloaded")
    ? overrides.downloaded
    : "1.2.3";
  const controller = new UpdateInstallController({
    getRuntimeAccess: () => cell.get(),
    prepare: prepare.fn,
    stopRuntime: stop.fn,
    getDownloadedVersion: () => downloaded,
    installer: {
      quitAndInstall: () => installs.push("install"),
    },
    schedule: (callback) => scheduler.schedule(callback),
    cancelSchedule: (handle) => scheduler.cancel(handle),
    operationId: () => "op-1",
    ...overrides.controller,
  });
  controller.subscribe((snapshot) => snapshots.push(snapshot));
  return {
    controller,
    scheduler,
    prepare,
    stop,
    cell,
    installs,
    snapshots,
    setDownloaded: (value) => {
      downloaded = value;
    },
    getDownloaded: () => downloaded,
  };
}

function assertValidSnapshots(snapshots) {
  assert.ok(snapshots.length > 0);
  for (const snapshot of snapshots) {
    assert.equal(isUpdateInstallSnapshot(snapshot), true, JSON.stringify(snapshot));
    assert.equal(Object.isFrozen(snapshot), false);
  }
  for (let index = 1; index < snapshots.length; index += 1) {
    assert.ok(snapshots[index].revision > snapshots[index - 1].revision, "revision must increase");
  }
}

test("install waits for explicit active work, never calls stop or the installer", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  const initial = controller.install();
  assert.deepEqual(initial, {
    schemaVersion: 1,
    revision: 2,
    phase: "waiting",
    reason: null,
    version: "1.2.3",
    canCancel: true,
  });
  assert.equal(prepare.calls.length, 0, "no request is sent synchronously");
  assert.equal(scheduler.size, 1);

  // 重复 install 只加入当前同意，不启动第二个 operation。
  const joined = controller.install();
  assert.equal(joined.revision, initial.revision);

  assert.equal(scheduler.runNext(), true);
  assert.equal(prepare.calls.length, 1);
  const check = prepare.calls[0];
  assert.equal(check.request.mode, "check");
  assert.equal(check.request.operation_id, "op-1");
  assert.equal(check.request.workspace_id, "ws-1");
  assert.equal(check.request.instance_id, "instance-1");
  assert.equal(check.request.server_epoch, 3);
  assert.equal(check.connection.token, TOKEN_A);

  check.resolve({ kind: "result", state: "blocked", reason: "active_work" });
  await flush();
  assert.equal(controller.getSnapshot().phase, "waiting");
  assert.equal(controller.getSnapshot().reason, "active_work");
  assert.equal(controller.getSnapshot().canCancel, true);
  assert.equal(scheduler.size, 1, "next read-only check is scheduled");
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);

  // 等待不修改 Core 任务：不存在任何取消/暂停调用入口，取消也只清本地同意。
  assert.equal(controller.cancelInstall().phase, "idle");
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);
  assertValidSnapshots(snapshots);
});

test("idle check -> synchronous preparing -> prepared proof -> stop -> exactly one install", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();

  const preparing = controller.getSnapshot();
  assert.equal(preparing.phase, "preparing", "state moves to noncancelable before prepare is sent");
  assert.equal(preparing.canCancel, false);
  assert.equal(prepare.calls.length, 2);
  assert.equal(prepare.calls[1].request.mode, "prepare");

  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(controller.getSnapshot().phase, "stopping");
  assert.equal(controller.getSnapshot().canCancel, false);
  assert.equal(stop.calls.length, 1);
  assert.equal(installs.length, 0, "installer only after confirmed stop");

  // 重复同意/取消在 stopping 不得产生第二次停止或安装。
  controller.install();
  controller.cancelInstall();
  assert.equal(stop.calls.length, 1);

  stop.calls[0].resolve();
  await flush();
  assert.equal(installs.length, 1);
  assert.equal(controller.getSnapshot().phase, "installing");

  controller.install();
  controller.cancelInstall();
  controller.cancelConsent();
  await flush();
  assert.equal(installs.length, 1, "quit/duplicate events do not install again");
  assertValidSnapshots(snapshots);
});

test("cancel while waiting drops the attempt and ignores late replies", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  controller.install();
  scheduler.runNext();
  assert.equal(prepare.calls.length, 1);

  const cancelled = controller.cancelInstall();
  assert.equal(cancelled.phase, "idle");
  assert.equal(cancelled.reason, null);
  assert.equal(cancelled.version, null);
  assert.equal(cancelled.canCancel, false);

  // 取消时终止在飞 check；迟到 idle 不得开始 prepare。
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  assert.equal(prepare.calls.length, 1);
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);
  assert.equal(scheduler.size, 0);
  assertValidSnapshots(snapshots);
});

test("ordinary quit cancels consent before preparing completes and never installs", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  assert.equal(controller.getSnapshot().phase, "preparing");

  controller.cancelConsent();
  assert.equal(controller.getSnapshot().phase, "blocked");
  assert.equal(controller.getSnapshot().reason, "restart_required");

  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);

  // dispose/重复退出同样不安装。
  controller.cancelConsent();
  await flush();
  assert.equal(installs.length, 0);
  assertValidSnapshots(snapshots);
});

test("downloaded version changes invalidate consent (waiting -> idle, preparing -> fail closed)", async () => {
  const { controller, scheduler, prepare, installs, snapshots } = setup();
  controller.install();
  controller.noteDownloadedVersion("9.9.9");
  assert.equal(controller.getSnapshot().phase, "idle");
  assert.equal(controller.getSnapshot().version, null);
  assert.equal(scheduler.size, 0, "pending check timer is cleared");
  assert.equal(installs.length, 0);

  // 新版本下重新显式同意，并在 preparing 时再次失效。
  scheduler.runNext();
  assert.equal(prepare.calls.length, 0, "late check after version change never starts");
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  assert.equal(controller.getSnapshot().phase, "preparing");
  controller.noteDownloadedVersion("10.0.0");
  assert.equal(controller.getSnapshot().phase, "blocked");
  assert.equal(controller.getSnapshot().reason, "restart_required");
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(installs.length, 0);
  assertValidSnapshots(snapshots);
});

test("runtime generation or connection changes fail closed before and after prepare", async () => {
  for (const [label, identity] of [
    ["generation", { generation: 2 }],
    ["server epoch", { serverEpoch: 4 }],
    ["instance", { instanceId: "instance-2" }],
  ]) {
    const before = setup();
    before.controller.install();
    before.cell.set(runtimeAccess({ identity }));
    before.scheduler.runNext();
    await flush();
    assert.equal(before.controller.getSnapshot().phase, "blocked", label);
    assert.equal(before.controller.getSnapshot().reason, "runtime_changed", label);
    assert.equal(before.prepare.calls.length, 0, label);
    assert.equal(before.installs.length, 0, label);
    assertValidSnapshots(before.snapshots);

    // 身份恢复后允许用户重新显式同意（旧同意已丢弃，不是自动安装）。
    before.cell.set(runtimeAccess());
    assert.equal(before.controller.install().phase, "waiting", `${label} retry`);
  }

  // 独立 operation：prepare 已发出后连接令牌变化。
  const after = setup();
  after.controller.install();
  after.scheduler.runNext();
  after.prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  assert.equal(after.controller.getSnapshot().phase, "preparing");
  after.cell.set(runtimeAccess({ connection: { token: TOKEN_B } }));
  after.prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(after.controller.getSnapshot().reason, "runtime_changed");
  assert.equal(after.stop.calls.length, 0);
  assert.equal(after.installs.length, 0);
  assertValidSnapshots(after.snapshots);
});

test("late prepared replies from an older attempt are discarded", async () => {
  const { controller, scheduler, prepare, stop, installs } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  assert.equal(controller.getSnapshot().phase, "preparing");

  controller.cancelConsent();
  controller.install(); // 退出后不得重新开始
  assert.equal(controller.getSnapshot().phase, "blocked");
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);
});

test("stop failure keeps zero installer calls and a finite blocked reason", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  stop.calls[0].reject(new Error("cleanup unconfirmed"));
  await flush();
  assert.equal(controller.getSnapshot().phase, "blocked");
  assert.equal(controller.getSnapshot().reason, "stop_unconfirmed");
  assert.equal(installs.length, 0);

  // 失败后不允许不经用户新确认自动重试。
  controller.install();
  assert.equal(controller.getSnapshot().reason, "stop_unconfirmed");
  assert.equal(installs.length, 0);
  assertValidSnapshots(snapshots);
});

test("ordinary quit during stopping resolves later but never installs", async () => {
  const { controller, scheduler, prepare, stop, installs } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  assert.equal(controller.getSnapshot().phase, "stopping");
  assert.equal(stop.calls.length, 1);

  controller.cancelConsent();
  assert.equal(controller.getSnapshot().reason, "stop_unconfirmed");
  stop.calls[0].resolve();
  await flush();
  assert.equal(installs.length, 0);
});

test("prepare blocked by new work returns to cancelable waiting with no stop", async () => {
  const { controller, scheduler, prepare, stop, installs, snapshots } = setup();
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  prepare.calls[1].resolve({ kind: "result", state: "blocked", reason: "active_work" });
  await flush();
  const snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "waiting");
  assert.equal(snapshot.reason, "active_work");
  assert.equal(snapshot.canCancel, true);
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);
  assert.equal(controller.cancelInstall().phase, "idle");
  assertValidSnapshots(snapshots);
});

test("core blocked reasons map fail closed and never imply prepared", async () => {
  for (const [reason, expected] of [
    ["executor_unconfirmed", "executor_unconfirmed"],
    ["preparing", "restart_required"],
    ["restart_required", "restart_required"],
  ]) {
    const { controller, scheduler, prepare, installs } = setup();
    controller.install();
    scheduler.runNext();
    prepare.calls[0].resolve({ kind: "result", state: "blocked", reason });
    await flush();
    assert.equal(controller.getSnapshot().phase, "blocked", reason);
    assert.equal(controller.getSnapshot().reason, expected, reason);
    assert.equal(installs.length, 0);
  }
});

test("transport, 503 and identity failures produce zero installs and safe finite states", async () => {
  for (const [outcome, expected] of [
    [{ kind: "transport" }, "runtime_unavailable"],
    [{ kind: "unavailable" }, "runtime_unavailable"],
    [{ kind: "identity" }, "runtime_changed"],
    [{ kind: "conflict" }, "runtime_changed"],
    [{ kind: "invalid" }, "prepare_failed"],
  ]) {
    const { controller, scheduler, prepare, stop, installs } = setup();
    controller.install();
    scheduler.runNext();
    prepare.calls[0].resolve(outcome);
    await flush();
    assert.equal(controller.getSnapshot().phase, "blocked", JSON.stringify(outcome));
    assert.equal(controller.getSnapshot().reason, expected, JSON.stringify(outcome));
    assert.equal(stop.calls.length, 0);
    assert.equal(installs.length, 0);
  }
});

test("install without a downloaded version is blocked and does not touch Core", () => {
  const { controller, prepare, stop, installs } = setup({ downloaded: null });
  const snapshot = controller.install();
  assert.equal(snapshot.phase, "blocked");
  assert.equal(snapshot.reason, "update_unavailable");
  assert.equal(snapshot.version, null);
  assert.equal(prepare.calls.length, 0);
  assert.equal(stop.calls.length, 0);
  assert.equal(installs.length, 0);
});

test("installer exception becomes install_failed without a second call", async () => {
  const { controller, scheduler, prepare, stop } = setup({
    controller: {
      installer: {
        quitAndInstall() {
          throw new Error("installer unavailable");
        },
      },
    },
  });
  controller.install();
  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  stop.calls[0].resolve();
  await flush();
  assert.equal(controller.getSnapshot().phase, "blocked");
  assert.equal(controller.getSnapshot().reason, "install_failed");
});

// ---------------------------------------------------------------------------
// prepare 客户端：精确回显、有限状态码与有界读取
// ---------------------------------------------------------------------------

function request(overrides = {}) {
  return {
    mode: "check",
    operation_id: "op-1",
    workspace_id: "ws-1",
    instance_id: "instance-1",
    server_epoch: 3,
    ...overrides,
  };
}

function resultBody(overrides = {}) {
  return {
    schema_version: 1,
    operation_id: "op-1",
    workspace_id: "ws-1",
    instance_id: "instance-1",
    server_epoch: 3,
    state: "blocked",
    reason: "active_work",
    ...overrides,
  };
}

function fetchStub(response) {
  const calls = [];
  return {
    calls,
    fetch: async (url, init) => {
      calls.push({ url, init });
      return response();
    },
  };
}

test("preparation client posts exact wire body/headers and accepts the matching echo", async () => {
  const stub = fetchStub(() => Response.json(resultBody()));
  const client = createUpdatePreparationClient(stub.fetch, { requestId: () => "req-1" });
  const outcome = await client(
    { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
    request(),
    AbortSignal.timeout(1000),
  );
  assert.deepEqual(outcome, { kind: "result", state: "blocked", reason: "active_work" });
  assert.equal(stub.calls.length, 1);
  assert.equal(stub.calls[0].url, "http://127.0.0.1:4310/api/v1/system/update-preparation");
  assert.equal(stub.calls[0].init.method, "POST");
  assert.equal(stub.calls[0].init.headers["X-YaoMu-Token"], "token-1");
  assert.equal(stub.calls[0].init.headers.request_id, "req-1");
  assert.deepEqual(JSON.parse(stub.calls[0].init.body), {
    mode: "check",
    operation_id: "op-1",
    workspace_id: "ws-1",
    instance_id: "instance-1",
    server_epoch: 3,
  });
});

test("preparation client rejects extra keys, wrong operation/epoch and invalid state combos", async () => {
  for (const body of [
    { ...resultBody(), extra: true },
    resultBody({ operation_id: "op-2" }),
    resultBody({ server_epoch: 4 }),
    resultBody({ state: "prepared", reason: "active_work" }),
    resultBody({ state: "idle", reason: "active_work" }),
    resultBody({ state: "prepared", reason: null, schema_version: 2 }),
    resultBody({ state: "blocked", reason: "unknown_reason" }),
  ]) {
    const stub = fetchStub(() => Response.json(body));
    const client = createUpdatePreparationClient(stub.fetch);
    const outcome = await client(
      { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
      request(),
      AbortSignal.timeout(1000),
    );
    assert.deepEqual(outcome, { kind: "invalid" }, JSON.stringify(body));
  }
});

test("preparation client maps 409/401/503 and network failures to finite outcomes", async () => {
  for (const [status, expected] of [
    [409, { kind: "conflict" }],
    [401, { kind: "identity" }],
    [403, { kind: "identity" }],
    [503, { kind: "unavailable" }],
    [500, { kind: "invalid" }],
    [400, { kind: "invalid" }],
  ]) {
    const stub = fetchStub(() => new Response("{}", { status }));
    const client = createUpdatePreparationClient(stub.fetch);
    const outcome = await client(
      { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
      request(),
      AbortSignal.timeout(1000),
    );
    assert.deepEqual(outcome, expected, String(status));
  }

  const client = createUpdatePreparationClient(async () => {
    throw new Error("ECONNREFUSED");
  });
  assert.deepEqual(
    await client(
      { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
      request(),
      AbortSignal.timeout(1000),
    ),
    { kind: "transport" },
  );
});

test("preparation client bounds the response body instead of unbounded json()", async () => {
  let cancelled = false;
  const oversized = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(70 * 1024));
    },
    cancel() {
      cancelled = true;
    },
  });
  const client = createUpdatePreparationClient(async () =>
    new Response(oversized, { status: 200, headers: { "Content-Type": "application/json" } }),
  );
  const outcome = await client(
    { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
    request(),
    AbortSignal.timeout(1000),
  );
  assert.deepEqual(outcome, { kind: "invalid" });
  assert.equal(cancelled, true);
});

test("preparation client settles on abort when the response body never produces", async () => {
  const body = new ReadableStream({
    pull() {
      return new Promise(() => undefined);
    },
  });
  const client = createUpdatePreparationClient(async () => new Response(body, { status: 200 }));
  const outcome = await Promise.race([
    client(
      { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
      request(),
      AbortSignal.timeout(30),
    ),
    new Promise((resolve) => setTimeout(() => resolve("UNSETTLED_AFTER_ABORT"), 500)),
  ]);
  assert.deepEqual(outcome, { kind: "transport" });
});

test("preparation client bounds a reader.cancel that never settles", async () => {
  const body = new ReadableStream({
    pull() {
      return new Promise(() => undefined);
    },
    cancel() {
      return new Promise(() => undefined);
    },
  });
  const client = createUpdatePreparationClient(async () => new Response(body, { status: 200 }));
  const started = Date.now();
  const outcome = await Promise.race([
    client(
      { baseUrl: "http://127.0.0.1:4310", token: "token-1" },
      request(),
      AbortSignal.timeout(30),
    ),
    new Promise((resolve) => setTimeout(() => resolve("UNSETTLED_AFTER_CANCEL"), 1500)),
  ]);
  assert.deepEqual(outcome, { kind: "transport" });
  assert.ok(Date.now() - started < 1200, "cancel wait must stay bounded");
});

test("thrown prepare clients fail closed without unhandled rejections", async () => {
  const rejections = [];
  const onRejection = (reason) => rejections.push(reason);
  process.on("unhandledRejection", onRejection);
  try {
    const checkThrow = setup({
      controller: {
        prepare: async () => {
          throw new Error("check client exploded");
        },
      },
    });
    checkThrow.controller.install();
    checkThrow.scheduler.runNext();
    await flush();
    await flush();
    assert.equal(checkThrow.controller.getSnapshot().phase, "blocked");
    assert.equal(checkThrow.controller.getSnapshot().reason, "runtime_unavailable");
    assert.equal(checkThrow.installs.length, 0);

    const prepareThrow = setup({
      controller: {
        prepare: async (_connection, req) => {
          if (req.mode === "check") return { kind: "result", state: "idle", reason: null };
          throw new Error("prepare client exploded");
        },
      },
    });
    prepareThrow.controller.install();
    prepareThrow.scheduler.runNext();
    await flush();
    await flush();
    assert.equal(prepareThrow.controller.getSnapshot().phase, "blocked");
    assert.equal(prepareThrow.controller.getSnapshot().reason, "prepare_failed");
    assert.equal(prepareThrow.installs.length, 0);

    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.deepEqual(rejections, []);
  } finally {
    process.off("unhandledRejection", onRejection);
  }
});

test("updater error event after quitAndInstall becomes finite install_failed and never reinstalls", async () => {
  const { EventEmitter } = await import("node:events");
  const updater = new EventEmitter();
  const installCalls = [];
  updater.quitAndInstall = (silent, forceRunAfter) => {
    installCalls.push({ silent, forceRunAfter });
    // 复刻 electron-updater 6.8.11 BaseUpdater：内部 catch 后 dispatch "error" 而不 throw。
    updater.emit("error", new Error("BaseUpdater install failed"));
  };
  const { controller, scheduler, prepare, stop, snapshots } = setup({
    controller: {
      installer: {
        quitAndInstall: () => updater.quitAndInstall(false, true),
      },
    },
  });
  updater.on("error", () => controller.noteInstallerError());

  controller.install();
  // waiting 阶段的 updater 错误不得推进安装状态。
  updater.emit("error", new Error("download error"));
  assert.equal(controller.getSnapshot().phase, "waiting");

  scheduler.runNext();
  prepare.calls[0].resolve({ kind: "result", state: "idle", reason: null });
  await flush();
  prepare.calls[1].resolve({ kind: "result", state: "prepared", reason: null });
  await flush();
  stop.calls[0].resolve();
  await flush();

  assert.equal(installCalls.length, 1);
  assert.deepEqual(installCalls[0], { silent: false, forceRunAfter: true });
  assert.equal(controller.getSnapshot().phase, "blocked");
  assert.equal(controller.getSnapshot().reason, "install_failed");
  assert.equal(controller.getSnapshot().canCancel, false);

  // Core 已停止：重复 install/error 不重装、不回到可重试状态。
  controller.install();
  controller.noteInstallerError();
  updater.emit("error", new Error("late error"));
  await flush();
  assert.equal(installCalls.length, 1);
  assert.equal(controller.getSnapshot().reason, "install_failed");
  assertValidSnapshots(snapshots);
});

import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import test from "node:test";

const require = createRequire(import.meta.url);
const Ajv2020 = require("ajv/dist/2020.js");
const {
  RuntimeController,
  RuntimeHandshakeError,
  fetchSystemIdentity,
} = require("../dist/runtime.js");
const { CoreBootstrapError } = require("../dist/bootstrap.js");

const STOP_TIMEOUT_MS = 200;
const SETTLE_TIMEOUT_MS = 1_000;
// 远高于常见 pid 上限：process.kill(-pid) 失败后落到 child.kill，假 child 可发出 exit。
const FAKE_PID = 2_000_000_000;

let nextPid = FAKE_PID;

class FakeChild extends EventEmitter {
  constructor() {
    super();
    this.pid = nextPid;
    nextPid += 1;
    this.exitCode = null;
    this.signalCode = null;
    this.kills = [];
  }

  kill(signal = "SIGTERM") {
    if (this.exitCode !== null || this.signalCode !== null) return false;
    this.kills.push(signal);
    setImmediate(() => this.exit(signal));
    return true;
  }

  exit(signal = "SIGTERM") {
    if (this.exitCode !== null || this.signalCode !== null) return;
    this.signalCode = signal;
    this.emit("exit", null, signal);
  }
}

let nextPort = 41_000;
function makeInfo(child) {
  const port = nextPort;
  nextPort += 1;
  return {
    child,
    port,
    token: `token-${port}`,
    baseUrl: `http://127.0.0.1:${port}`,
    pid: child.pid,
    startedAt: new Date().toISOString(),
  };
}

function identity(instanceId, workspaceId = "workspace-1", serverEpoch = 7) {
  return { appId: "yaomu", protocolVersion: "0.2", instanceId, workspaceId, serverEpoch };
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

async function drain(rounds = 12) {
  for (let i = 0; i < rounds; i += 1) await new Promise((resolve) => setImmediate(resolve));
}

async function waitUntil(check, timeoutMs = 2_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setImmediate(resolve));
  }
  throw new Error("等待条件超时");
}

const schema = JSON.parse(
  readFileSync(new URL("../../contracts/desktop-runtime.schema.json", import.meta.url), "utf8"),
);
// 已有 ajv/dist/2020 传递依赖；只用于校验契约，不新增根依赖。
const schemaValidate = new Ajv2020({ strict: true, allErrors: true }).compile(schema);

function assertSchemaValid(snapshot, label = "snapshot") {
  assert.equal(
    schemaValidate(snapshot),
    true,
    `${label}: ${JSON.stringify(schemaValidate.errors)}`,
  );
}

/** 按契约 schema 的 required/enum/const 约束校验快照形状（不引入运行时 ajv 依赖）。 */
function assertSnapshotShape(snapshot) {
  assertSchemaValid(snapshot);
  assert.deepEqual(
    Object.keys(snapshot).sort(),
    [...schema.required].sort(),
    "snapshot keys must match the contract exactly",
  );
  assert.equal(snapshot.schemaVersion, schema.properties.schemaVersion.const);
  assert.ok(Number.isSafeInteger(snapshot.revision) && snapshot.revision >= 1);
  assert.ok(Number.isSafeInteger(snapshot.generation) && snapshot.generation >= 0);
  assert.ok(schema.properties.phase.enum.includes(snapshot.phase), snapshot.phase);
  assert.ok(schema.properties.reason.enum.includes(snapshot.reason), String(snapshot.reason));
  assert.equal(typeof snapshot.canRetry, "boolean");
  if (snapshot.phase === "failed") {
    assert.equal(typeof snapshot.reason, "string");
  } else {
    assert.equal(snapshot.reason, null);
    assert.equal(snapshot.canRetry, false);
  }
  if (snapshot.phase === "ready") assert.ok(snapshot.generation >= 1);
  for (const reason of ["cleanup_unconfirmed", "protocol_mismatch", "workspace_mismatch"]) {
    if (snapshot.reason === reason) assert.equal(snapshot.canRetry, false, reason);
  }
}

/** 默认依赖：可控 prepare/startCore/handshake，真实 Supervisor。 */
function makeController(overrides = {}) {
  const calls = { prepare: 0, startCore: 0, handshake: 0 };
  const children = [];
  const controller = new RuntimeController({
    prepare: async () => {
      calls.prepare += 1;
    },
    startCore: async () => {
      calls.startCore += 1;
      const child = new FakeChild();
      children.push(child);
      return makeInfo(child);
    },
    handshake: async () => {
      calls.handshake += 1;
      return identity(`instance-${calls.startCore}`);
    },
    maxRestarts: 1,
    stopTimeoutMs: STOP_TIMEOUT_MS,
    settleTimeoutMs: SETTLE_TIMEOUT_MS,
    log: () => undefined,
    ...overrides,
  });
  return { controller, calls, children };
}

test("contract schema accepts every Main snapshot and rejects invalid states", async () => {
  const prepareGate = deferred();
  const { controller } = makeController({
    prepare: async () => {
      await prepareGate.promise;
    },
  });
  const accepted = [controller.getSnapshot()];
  controller.subscribe((snapshot) => accepted.push(snapshot));
  const started = controller.start();
  await drain();
  prepareGate.resolve();
  await started;
  assert.equal(controller.getSnapshot().phase, "ready");

  const failed = makeController({
    prepare: undefined,
    startCore: async () => {
      throw new CoreBootstrapError("CORE_NOT_READY", "测试 Core 未就绪");
    },
  });
  const failedSnapshots = [failed.controller.getSnapshot()];
  failed.controller.subscribe((snapshot) => failedSnapshots.push(snapshot));
  await failed.controller.start();
  assert.equal(failed.controller.getSnapshot().phase, "failed");

  for (const [index, snapshot] of [...accepted, ...failedSnapshots].entries()) {
    assertSchemaValid(snapshot, `produced snapshot ${index}`);
    assert.ok(snapshot.revision >= 1);
  }

  const base = controller.getSnapshot();
  const invalid = [
    [{ ...base, port: 1234 }, "额外字段"],
    [{ ...base, revision: 0 }, "revision 必须 >= 1"],
    [{ ...base, phase: "failed" }, "failed 必须给出 reason"],
    [{ ...base, phase: "starting", reason: "startup_failed" }, "非 failed reason 必须 null"],
    [{ ...base, phase: "starting", canRetry: true }, "非 failed canRetry 必须 false"],
    [{ ...base, phase: "ready", generation: 0 }, "ready generation 必须 >= 1"],
    [
      { ...base, phase: "failed", reason: "cleanup_unconfirmed", canRetry: true },
      "cleanup_unconfirmed 不可重试",
    ],
    [
      { ...base, phase: "failed", reason: "workspace_mismatch", canRetry: true },
      "workspace_mismatch 不可重试",
    ],
    [{ ...base, reason: "protocol_mismatch" }, "协议不匹配必须 failed"],
    [{ ...base, schemaVersion: 2 }, "schemaVersion 固定 1"],
    [
      { ...base, phase: "ready", generation: 1, canRetry: true },
      "ready 不可重试",
    ],
  ];
  for (const [sample, label] of invalid) {
    assert.equal(schemaValidate(sample), false, `schema should reject: ${label}`);
  }
});

test("initial snapshot matches the contract and exposes no connection", () => {
  const { controller } = makeController();
  const snapshot = controller.getSnapshot();
  assertSnapshotShape(snapshot);
  assert.deepEqual(snapshot, {
    schemaVersion: 1,
    revision: 1,
    generation: 0,
    phase: "preparing",
    reason: null,
    canRetry: false,
  });
  assert.equal(controller.getConnection(), null);
});

test("preparing -> starting -> ready after prepare, Core and authenticated handshake", async () => {
  const prepareGate = deferred();
  let phaseAtStartCore = null;
  let phaseAtHandshake = null;
  const { controller, calls } = makeController({
    prepare: async () => {
      calls.prepare += 1;
      await prepareGate.promise;
    },
    startCore: async () => {
      calls.startCore += 1;
      phaseAtStartCore = controller.getSnapshot().phase;
      return makeInfo(new FakeChild());
    },
    handshake: async () => {
      calls.handshake += 1;
      phaseAtHandshake = controller.getSnapshot().phase;
      return identity("instance-1");
    },
  });
  const revisions = [];
  controller.subscribe((snapshot) => {
    assertSnapshotShape(snapshot);
    revisions.push(snapshot.revision);
  });

  const started = controller.start();
  await drain();
  assert.equal(controller.getSnapshot().phase, "preparing");
  assert.equal(calls.startCore, 0);

  prepareGate.resolve();
  await started;

  assert.equal(phaseAtStartCore, "starting");
  assert.equal(phaseAtHandshake, "starting");
  const snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "ready");
  assert.equal(snapshot.generation, 1);
  assert.equal(snapshot.reason, null);
  assert.equal(snapshot.canRetry, false);
  assert.ok(revisions.length >= 2);
  for (let i = 1; i < revisions.length; i += 1) {
    assert.ok(revisions[i] > revisions[i - 1], "revision must be strictly increasing");
  }
  assert.match(controller.getConnection()?.token ?? "", /^token-/);
});

test("snapshot never contains port, token, URL, identity or raw errors", async () => {
  const { controller } = makeController();
  await controller.start();
  const serialized = JSON.stringify(controller.getSnapshot());
  for (const forbidden of [
    "token-",
    "127.0.0.1",
    "baseUrl",
    "workspace-1",
    "instance-1",
    "Error",
    "http://",
  ]) {
    assert.ok(!serialized.includes(forbidden), `snapshot leaked ${forbidden}`);
  }
});

test("retry during preparing/starting/ready is a no-op and never starts a second Core", async () => {
  const prepareGate = deferred();
  const { controller, calls } = makeController({
    prepare: async () => {
      calls.prepare += 1;
      await prepareGate.promise;
    },
  });
  const started = controller.start();
  await drain();
  assert.equal(controller.getSnapshot().phase, "preparing");
  controller.retry();
  assert.equal(calls.startCore, 0);
  prepareGate.resolve();
  await started;
  assert.equal(controller.getSnapshot().phase, "ready");

  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
  assert.equal(controller.getSnapshot().phase, "ready");
});

test("ordinary startup failure is retryable; concurrent retries share one attempt", async () => {
  const second = deferred();
  const { controller, calls } = makeController({
    prepare: undefined,
    startCore: async () => {
      calls.startCore += 1;
      if (calls.startCore === 1) {
        throw new CoreBootstrapError("CORE_NOT_READY", "测试 Core 未就绪");
      }
      await second.promise;
      return makeInfo(new FakeChild());
    },
    handshake: async () => identity("instance-2"),
  });

  await controller.start();
  let snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "failed");
  assert.equal(snapshot.reason, "startup_failed");
  assert.equal(snapshot.canRetry, true);
  assert.equal(controller.getConnection(), null, "failed state must not expose a connection");

  const first = controller.retry();
  assert.equal(first.phase, "starting");
  const secondRetry = controller.retry();
  assert.equal(secondRetry.phase, "starting");
  assert.equal(calls.startCore, 2, "concurrent retry must not launch another Core");

  second.resolve();
  await waitUntil(() => controller.getSnapshot().phase === "ready");
  snapshot = controller.getSnapshot();
  assert.equal(snapshot.generation, 1);
  assert.equal(calls.startCore, 2);
});

test("handshake failure stops the owned Core and remains retryable", async () => {
  let attempts = 0;
  const { controller, calls } = makeController({
    prepare: undefined,
    handshake: async () => {
      attempts += 1;
      if (attempts === 1) throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手失败");
      return identity("instance-2");
    },
  });

  await controller.start();
  let snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "failed");
  assert.equal(snapshot.reason, "handshake_failed");
  assert.equal(snapshot.canRetry, true);
  await waitUntil(() => calls.startCore === 1);

  controller.retry();
  await waitUntil(() => controller.getSnapshot().phase === "ready");
  snapshot = controller.getSnapshot();
  assert.equal(snapshot.generation, 1);
  assert.equal(calls.startCore, 2);
});

test("protocol mismatch is non-retryable and terminates the owned Core", async () => {
  const { controller, calls } = makeController({
    prepare: undefined,
    handshake: async () => {
      throw new RuntimeHandshakeError("protocol_mismatch", "Core 应用或协议版本不兼容");
    },
  });

  await controller.start();
  const snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "failed");
  assert.equal(snapshot.reason, "protocol_mismatch");
  assert.equal(snapshot.canRetry, false);
  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
});

test("unconfirmed cleanup after handshake failure stays quarantined and unretryable", async () => {
  let child;
  const { controller, calls } = makeController({
    prepare: undefined,
    startCore: async () => {
      calls.startCore += 1;
      child = new FakeChild();
      return makeInfo(child);
    },
    handshake: async () => {
      throw new RuntimeHandshakeError("handshake_failed", "Core 系统握手失败");
    },
    terminateChild: async () => {
      throw new Error("无法确认测试进程退出");
    },
  });

  await controller.start();
  const snapshot = controller.getSnapshot();
  assert.equal(snapshot.phase, "failed");
  assert.equal(snapshot.reason, "cleanup_unconfirmed");
  assert.equal(snapshot.canRetry, false);
  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
  await assert.rejects(controller.stop(), /无法确认自有 Core 退出/);
  assert.equal(controller.getSnapshot().canRetry, false);
  // 假 child 仍可被测试显式收尾，避免残留监听。
  child.exit("SIGKILL");
});

test("workspace is pinned on first handshake; a different workspace fails closed", async () => {
  let handshakeCount = 0;
  const { controller, calls, children } = makeController({
    prepare: undefined,
    handshake: async () => {
      handshakeCount += 1;
      return handshakeCount === 1
        ? identity("instance-1", "workspace-1")
        : identity("instance-2", "workspace-2");
    },
  });

  await controller.start();
  assert.equal(controller.getSnapshot().generation, 1);

  children[0].exit("SIGKILL");
  await waitUntil(() => controller.getSnapshot().phase === "failed");
  const snapshot = controller.getSnapshot();
  assert.equal(snapshot.reason, "workspace_mismatch");
  assert.equal(snapshot.canRetry, false);
  assert.equal(controller.getConnection(), null, "mismatch must not expose the rejected connection");
  assert.equal(snapshot.generation, 1, "mismatched workspace must not advance generation");
  assert.notEqual(children[1].signalCode, null, "rejected replacement Core must be terminated");

  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
});

test("same workspace with a new Core instance advances generation", async () => {
  let handshakeCount = 0;
  const { controller, children } = makeController({
    prepare: undefined,
    handshake: async () => {
      handshakeCount += 1;
      return identity(`instance-${handshakeCount}`, "workspace-1");
    },
  });

  await controller.start();
  assert.equal(controller.getSnapshot().generation, 1);

  children[0].exit("SIGKILL");
  await waitUntil(() => controller.getSnapshot().generation === 2);
  assert.equal(controller.getSnapshot().phase, "ready");
});

test("exhausted Core restart becomes a retryable core_exited failure on the same owner", async () => {
  const { controller, calls, children } = makeController({
    maxRestarts: 0,
    prepare: undefined,
    handshake: async () => identity(`instance-${calls.startCore}`),
  });

  await controller.start();
  children[0].exit("SIGKILL");
  await waitUntil(() => controller.getSnapshot().phase === "failed");
  let snapshot = controller.getSnapshot();
  assert.equal(snapshot.reason, "core_exited");
  assert.equal(snapshot.canRetry, true);

  controller.retry();
  await waitUntil(() => controller.getSnapshot().phase === "ready");
  snapshot = controller.getSnapshot();
  assert.equal(snapshot.generation, 2);
  assert.equal(calls.startCore, 2);
});

test("stop during preparation cancels the attempt without spawning Core", async () => {
  const { controller, calls } = makeController({
    prepare: (signal) =>
      new Promise((_resolve, reject) => {
        const onAbort = () => reject(Object.assign(new Error("controlled prep canceled"), { name: "AbortError" }));
        if (signal.aborted) onAbort();
        else signal.addEventListener("abort", onAbort, { once: true });
      }),
  });

  const started = controller.start();
  await drain();
  await controller.stop();
  await started;
  assert.equal(calls.startCore, 0);
  assert.equal(controller.getSnapshot().phase, "stopped");
  assert.equal(controller.getConnection(), null);

  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
  assert.equal(controller.getSnapshot().phase, "stopped");
});

test("stop during handshake aborts, terminates the owned Core and never publishes ready", async () => {
  const handshakeStarted = deferred();
  let child;
  const { controller, calls } = makeController({
    prepare: undefined,
    startCore: async () => {
      calls.startCore += 1;
      child = new FakeChild();
      return makeInfo(child);
    },
    handshake: (_connection, signal) => {
      handshakeStarted.resolve();
      return new Promise((_resolve, reject) => {
        const onAbort = () => reject(Object.assign(new Error("handshake canceled"), { name: "AbortError" }));
        if (signal.aborted) onAbort();
        else signal.addEventListener("abort", onAbort, { once: true });
      });
    },
  });

  const started = controller.start();
  await handshakeStarted.promise;
  assert.equal(controller.getSnapshot().phase, "starting");
  const stop = controller.stop();
  await stop;
  await started;

  assert.equal(controller.getSnapshot().phase, "stopped");
  assert.equal(calls.startCore, 1);
  assert.notEqual(child.signalCode, null);
  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
});

test("markFailed gives a finite pre-launch failure without spawning Core", () => {
  const { controller, calls } = makeController();
  const snapshot = controller.markFailed("startup_failed", false);
  assertSnapshotShape(snapshot);
  assert.equal(snapshot.phase, "failed");
  assert.equal(snapshot.reason, "startup_failed");
  assert.equal(snapshot.canRetry, false);
  assert.equal(calls.startCore, 0);
  controller.retry();
  assert.equal(calls.startCore, 0);
});

// --- 认证 system 握手 -------------------------------------------------------

const connection = { baseUrl: "http://127.0.0.1:1234", token: "secret-instance-token" };

function systemResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

test("fetchSystemIdentity accepts only the expected app, protocol, readiness and identity", async () => {
  let seen;
  const result = await fetchSystemIdentity(connection, {
    fetchImpl: async (url, init) => {
      seen = { url, init };
      return systemResponse({
        app_id: "yaomu",
        protocol_version: "0.2",
        instance_id: "instance-1",
        workspace_id: "workspace-1",
        server_epoch: 7,
        core_ready: true,
      });
    },
  });
  assert.deepEqual(result, {
    appId: "yaomu",
    protocolVersion: "0.2",
    instanceId: "instance-1",
    workspaceId: "workspace-1",
    serverEpoch: 7,
  });
  assert.equal(seen.url, "http://127.0.0.1:1234/api/v1/system");
  assert.equal(seen.init.headers["X-YaoMu-Token"], "secret-instance-token");
  assert.equal(seen.init.redirect, "error");
});

test("fetchSystemIdentity maps incompatible app/protocol to protocol_mismatch", async () => {
  for (const body of [
    { app_id: "other", protocol_version: "0.2", core_ready: true, instance_id: "i", workspace_id: "w", server_epoch: 0 },
    { app_id: "yaomu", protocol_version: "9.9", core_ready: true, instance_id: "i", workspace_id: "w", server_epoch: 0 },
    { core_ready: true, instance_id: "i", workspace_id: "w", server_epoch: 0 },
  ]) {
    await assert.rejects(
      fetchSystemIdentity(connection, { fetchImpl: async () => systemResponse(body) }),
      (error) => error instanceof RuntimeHandshakeError && error.reason === "protocol_mismatch",
    );
  }
});

test("fetchSystemIdentity rejects not-ready or malformed identity as handshake_failed", async () => {
  const valid = {
    app_id: "yaomu",
    protocol_version: "0.2",
    instance_id: "instance-1",
    workspace_id: "workspace-1",
    server_epoch: 7,
    core_ready: true,
  };
  const cases = [
    { ...valid, core_ready: false },
    { ...valid, instance_id: "" },
    { ...valid, workspace_id: "" },
    { ...valid, instance_id: 42 },
    { ...valid, server_epoch: -1 },
    { ...valid, server_epoch: 1.5 },
    { ...valid, server_epoch: Number.MAX_SAFE_INTEGER + 1 },
    { ...valid, server_epoch: "7" },
  ];
  for (const body of cases) {
    await assert.rejects(
      fetchSystemIdentity(connection, { fetchImpl: async () => systemResponse(body) }),
      (error) => error instanceof RuntimeHandshakeError && error.reason === "handshake_failed",
    );
  }
  await assert.rejects(
    fetchSystemIdentity(connection, { fetchImpl: async () => systemResponse(valid, 503) }),
    (error) => error.reason === "handshake_failed",
  );
  await assert.rejects(
    fetchSystemIdentity(connection, {
      fetchImpl: async () => new Response("not json", { status: 200 }),
    }),
    (error) => error.reason === "handshake_failed",
  );
});

test("fetchSystemIdentity reports timeout and network failures without leaking details", async () => {
  const timeoutError = await fetchSystemIdentity(connection, {
    timeoutMs: 5,
    fetchImpl: (_url, init) =>
      new Promise((_resolve, reject) => {
        init.signal.addEventListener(
          "abort",
          () => reject(Object.assign(new Error("raw network detail"), { name: "AbortError" })),
          { once: true },
        );
      }),
  }).catch((error) => error);
  assert.equal(timeoutError.reason, "handshake_failed");
  assert.ok(!String(timeoutError.message).includes("raw network detail"));

  const network = await fetchSystemIdentity(connection, {
    fetchImpl: async () => {
      throw new Error("ECONNREFUSED 127.0.0.1:1234 token=secret-instance-token");
    },
  }).catch((error) => error);
  assert.equal(network.reason, "handshake_failed");
  assert.ok(!String(network.message).includes("127.0.0.1"));
  assert.ok(!String(network.message).includes("secret-instance-token"));
});

test("fetchSystemIdentity follows the external abort signal", async () => {
  const abort = new AbortController();
  const pending = fetchSystemIdentity(connection, {
    signal: abort.signal,
    fetchImpl: (_url, init) =>
      new Promise((_resolve, reject) => {
        init.signal.addEventListener(
          "abort",
          () => reject(Object.assign(new Error("aborted"), { name: "AbortError" })),
          { once: true },
        );
      }),
  });
  abort.abort();
  await assert.rejects(pending, (error) => error.name === "AbortError");
});

test("fetchSystemIdentity rejects an oversized system response", async () => {
  await assert.rejects(
    fetchSystemIdentity(connection, {
      fetchImpl: async () => new Response(`{"pad":"${"x".repeat(70 * 1024)}"}`, { status: 200 }),
    }),
    (error) => error instanceof RuntimeHandshakeError && error.reason === "handshake_failed",
  );
});

/** 受控流：记录 pull/cancel，用于证明超限后不再 read。 */
function controlledResponse(chunks) {
  const state = { pulls: 0, cancelCalls: 0, cancelled: false };
  // highWaterMark=0：只有实际 read 请求才触发 pull，便于断言超限后不再 read。
  const stream = new ReadableStream({
    pull(controller) {
      state.pulls += 1;
      if (state.pulls <= chunks.length) controller.enqueue(chunks[state.pulls - 1]);
      else controller.close();
    },
    cancel() {
      state.cancelCalls += 1;
      state.cancelled = true;
    },
  }, { highWaterMark: 0 });
  return { response: new Response(stream), state };
}

/** 永不产生数据的受控流：用于超时/取消清理。 */
function hangingResponse() {
  const state = { pulls: 0, cancelCalls: 0, cancelled: false };
  const stream = new ReadableStream({
    pull() {
      state.pulls += 1;
      // 故意不 enqueue/close：读取会挂起，直到握手超时或外部取消。
    },
    cancel() {
      state.cancelCalls += 1;
      state.cancelled = true;
    },
  }, { highWaterMark: 0 });
  return { response: new Response(stream), state };
}

test("stream reader cancels at the 64KiB limit without reading further chunks", async () => {
  const { response, state } = controlledResponse([
    new Uint8Array(32 * 1024),
    new Uint8Array(32 * 1024),
    new Uint8Array(1),
    new Uint8Array(1),
  ]);
  const error = await fetchSystemIdentity(connection, {
    fetchImpl: async () => response,
  }).catch((caught) => caught);

  assert.equal(error.reason, "handshake_failed");
  assert.match(error.message, /超限/);
  assert.equal(state.pulls, 3, "must stop reading immediately after exceeding the limit");
  assert.equal(state.cancelCalls, 1, "overflow must cancel the reader");
  assert.equal(state.cancelled, true);
});

test("stream reader accepts a body split across chunks", async () => {
  const body = new TextEncoder().encode(
    JSON.stringify({
      app_id: "yaomu",
      protocol_version: "0.2",
      instance_id: "instance-1",
      workspace_id: "workspace-1",
      server_epoch: 7,
      core_ready: true,
    }),
  );
  const chunks = [body.slice(0, 7), body.slice(7, 31), body.slice(31)];
  const { response, state } = controlledResponse(chunks);
  const result = await fetchSystemIdentity(connection, { fetchImpl: async () => response });
  assert.equal(result.workspaceId, "workspace-1");
  assert.equal(result.instanceId, "instance-1");
  assert.equal(state.cancelCalls, 0);
});

test("timeout cancels and releases a hanging response reader", async () => {
  const { response, state } = hangingResponse();
  const started = Date.now();
  const error = await fetchSystemIdentity(connection, {
    timeoutMs: 10,
    fetchImpl: async () => response,
  }).catch((caught) => caught);

  assert.equal(error.reason, "handshake_failed");
  assert.match(error.message, /超时/);
  assert.ok(Date.now() - started < 1_000, "timeout must not wait for the body reader");
  assert.equal(state.cancelCalls, 1);
  assert.equal(state.cancelled, true);
  assert.equal(state.pulls, 1, "cancel must not trigger further reads");
});

test("external abort cancels and releases a hanging response reader", async () => {
  const { response, state } = hangingResponse();
  const abort = new AbortController();
  const pending = fetchSystemIdentity(connection, {
    signal: abort.signal,
    fetchImpl: async () => response,
  });
  // 让读取真正进入 pending read 后再取消。
  await new Promise((resolve) => setImmediate(resolve));
  abort.abort();
  await assert.rejects(pending, (error) => error.name === "AbortError");
  assert.equal(state.cancelCalls, 1);
  assert.equal(state.cancelled, true);
  assert.equal(state.pulls, 1);
});

test("late handshake resolution after stop never publishes ready or pins an active session", async () => {
  const gate = deferred();
  const handshakeStarted = deferred();
  let child;
  const { controller, calls } = makeController({
    prepare: undefined,
    startCore: async () => {
      calls.startCore += 1;
      child = new FakeChild();
      return makeInfo(child);
    },
    handshake: () => {
      handshakeStarted.resolve();
      // 故意忽略 abort：模拟底层已在取消后才返回的响应。
      return gate.promise;
    },
  });

  const started = controller.start();
  await handshakeStarted.promise;
  const stop = controller.stop();
  await drain();
  gate.resolve(identity("instance-late", "workspace-late"));
  await stop;
  await started;

  assert.equal(controller.getSnapshot().phase, "stopped");
  assert.equal(controller.getConnection(), null);
  assert.notEqual(child.signalCode, null, "late Core must be terminated");
  const before = calls.startCore;
  controller.retry();
  assert.equal(calls.startCore, before);
});

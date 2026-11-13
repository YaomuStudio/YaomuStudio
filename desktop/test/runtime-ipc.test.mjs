import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  RUNTIME_ATTACH_CHANNEL,
  RUNTIME_CHANGED_CHANNEL,
  RUNTIME_DETACH_CHANNEL,
  RUNTIME_GET_CHANNEL,
  RUNTIME_RETRY_CHANNEL,
  RuntimeBridge,
  registerRuntimeIpc,
} = require("../dist/ipc/runtime.js");
const { DesktopAuthorization } = require("../dist/ipc/authorization.js");

const appURL = "file:///app/ui/dist/index.html";

function snapshot(overrides = {}) {
  return {
    schemaVersion: 1,
    revision: 1,
    generation: 0,
    phase: "preparing",
    reason: null,
    canRetry: false,
    ...overrides,
  };
}

function makeSender(id = 1) {
  const sent = [];
  let destroyed = false;
  const listeners = new Map();
  const contents = {
    id,
    mainFrame: { url: appURL },
    getURL() {
      return this.mainFrame.url;
    },
    isDestroyed() {
      return destroyed;
    },
    send(channel, payload) {
      sent.push({ channel, payload });
    },
    once(event, callback) {
      listeners.set(event, callback);
      return this;
    },
    destroy() {
      destroyed = true;
      listeners.get("destroyed")?.();
    },
  };
  return { contents, sent, event: { sender: contents, senderFrame: contents.mainFrame } };
}

function makeController(initial = snapshot()) {
  let current = { ...initial };
  const listeners = new Set();
  const calls = { get: 0, retry: 0 };
  return {
    controller: {
      getSnapshot() {
        calls.get += 1;
        return { ...current };
      },
      retry() {
        calls.retry += 1;
        return { ...current };
      },
      subscribe(callback) {
        listeners.add(callback);
        return () => listeners.delete(callback);
      },
      emit(next) {
        current = { ...next };
        for (const callback of [...listeners]) callback({ ...current });
      },
      listenerCount() {
        return listeners.size;
      },
    },
    calls,
  };
}

function register(authorization, target) {
  authorization.registerWindow(target.contents, appURL);
}

const denied = {
  ok: false,
  error: {
    code: "LOCAL_ACCESS_DENIED",
    messageKey: "api.accessDenied",
    retryable: false,
    actionIds: [],
  },
};

test("get/retry/attach/detach reject unregistered, forged, subframe or navigated senders", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller, calls } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);

  const fake = makeSender(99);
  const sameIdForged = makeSender(target.contents.id);
  const childFrame = { sender: target.contents, senderFrame: { url: appURL } };
  const sources = [
    [fake.event, "unregistered window"],
    [sameIdForged.event, "forged contents with same id"],
    [childFrame, "child frame"],
  ];
  for (const [event, label] of sources) {
    assert.deepEqual(bridge.handleGet(event), denied, `get: ${label}`);
    assert.deepEqual(bridge.handleRetry(event), denied, `retry: ${label}`);
    assert.throws(() => bridge.handleAttach(event), /LOCAL_ACCESS_DENIED/, `attach: ${label}`);
    assert.throws(() => bridge.handleDetach(event), /LOCAL_ACCESS_DENIED/, `detach: ${label}`);
  }

  target.contents.mainFrame.url = "https://untrusted.example/";
  assert.deepEqual(bridge.handleGet(target.event), denied, "navigated get");
  assert.deepEqual(bridge.handleRetry(target.event), denied, "navigated retry");
  assert.throws(() => bridge.handleAttach(target.event), /LOCAL_ACCESS_DENIED/);
  target.contents.mainFrame.url = appURL;

  target.contents.destroy();
  assert.deepEqual(bridge.handleGet(target.event), denied, "destroyed get");
  assert.deepEqual(bridge.handleRetry(target.event), denied, "destroyed retry");
  assert.throws(() => bridge.handleAttach(target.event), /LOCAL_ACCESS_DENIED/);

  assert.deepEqual(calls, { get: 0, retry: 0 });
  bridge.stop();
});

test("trusted get/retry return the current snapshot without starting extra work", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller, calls } = makeController(
    snapshot({ revision: 3, generation: 1, phase: "ready" }),
  );
  const bridge = new RuntimeBridge(controller, authorization);

  assert.deepEqual(bridge.handleGet(target.event), {
    ok: true,
    data: snapshot({ revision: 3, generation: 1, phase: "ready" }),
  });
  assert.deepEqual(bridge.handleRetry(target.event), {
    ok: true,
    data: snapshot({ revision: 3, generation: 1, phase: "ready" }),
  });
  assert.deepEqual(calls, { get: 1, retry: 1 });
  bridge.stop();
});

test("broadcast reaches attached windows only and is reference counted per window", () => {
  const authorization = new DesktopAuthorization();
  const a = makeSender(1);
  const b = makeSender(2);
  register(authorization, a);
  register(authorization, b);
  const { controller } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);

  bridge.handleAttach(a.event);
  bridge.handleAttach(a.event);
  controller.emit(snapshot({ revision: 2, phase: "starting" }));
  assert.equal(a.sent.length, 1, "one broadcast per window even with two subscriptions");
  assert.equal(a.sent[0].channel, RUNTIME_CHANGED_CHANNEL);
  assert.deepEqual(a.sent[0].payload, snapshot({ revision: 2, phase: "starting" }));
  assert.equal(b.sent.length, 0, "unattached window receives nothing");

  bridge.handleDetach(a.event);
  controller.emit(snapshot({ revision: 3, phase: "ready", generation: 1 }));
  assert.equal(a.sent.length, 2, "one remaining subscription still receives");

  bridge.handleDetach(a.event);
  controller.emit(snapshot({ revision: 4, phase: "stopped" }));
  assert.equal(a.sent.length, 2, "after the last detach nothing is pushed");
  bridge.stop();
});

test("broadcast re-checks the sender and drops navigated or destroyed subscriptions", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);
  bridge.handleAttach(target.event);

  target.contents.mainFrame.url = "https://untrusted.example/";
  controller.emit(snapshot({ revision: 2, phase: "starting" }));
  assert.equal(target.sent.length, 0, "navigated document receives no push");

  target.contents.mainFrame.url = appURL;
  controller.emit(snapshot({ revision: 3, phase: "starting" }));
  assert.equal(target.sent.length, 0, "pruned subscription is not revived by navigation back");

  bridge.handleAttach(target.event);
  target.contents.destroy();
  controller.emit(snapshot({ revision: 4, phase: "stopped" }));
  assert.equal(target.sent.length, 0, "destroyed document receives no push");
  bridge.stop();
});

test("navigation/destruction hooks release subscriptions without later pushes", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);

  bridge.handleAttach(target.event);
  bridge.detachWindow(target.contents);
  controller.emit(snapshot({ revision: 2, phase: "starting" }));
  assert.equal(target.sent.length, 0);

  bridge.handleAttach(target.event);
  target.contents.destroy();
  controller.emit(snapshot({ revision: 3, phase: "stopped" }));
  assert.equal(target.sent.length, 0);
  bridge.stop();
});

test("stop clears subscribers and unsubscribes from the controller", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);
  bridge.handleAttach(target.event);
  assert.equal(controller.listenerCount(), 1);

  bridge.stop();
  assert.equal(controller.listenerCount(), 0);
  controller.emit(snapshot({ revision: 2, phase: "starting" }));
  assert.equal(target.sent.length, 0);
});

test("registerRuntimeIpc exposes exactly the fixed channels and keeps send denial silent", () => {
  const authorization = new DesktopAuthorization();
  const trusted = makeSender(1);
  register(authorization, trusted);
  const untrusted = makeSender(2);
  const { controller, calls } = makeController();
  const bridge = new RuntimeBridge(controller, authorization);
  const handlers = new Map();
  const ipc = {
    handle(channel, listener) {
      handlers.set(channel, listener);
    },
    on(channel, listener) {
      handlers.set(channel, listener);
    },
  };
  registerRuntimeIpc(ipc, bridge);

  assert.deepEqual(
    [...handlers.keys()].sort(),
    [RUNTIME_ATTACH_CHANNEL, RUNTIME_DETACH_CHANNEL, RUNTIME_GET_CHANNEL, RUNTIME_RETRY_CHANNEL].sort(),
  );
  assert.deepEqual(handlers.get(RUNTIME_GET_CHANNEL)(untrusted.event), denied);
  assert.deepEqual(handlers.get(RUNTIME_RETRY_CHANNEL)(untrusted.event), denied);
  assert.doesNotThrow(() => handlers.get(RUNTIME_ATTACH_CHANNEL)(untrusted.event));
  assert.doesNotThrow(() => handlers.get(RUNTIME_DETACH_CHANNEL)(untrusted.event));
  controller.emit(snapshot({ revision: 2, phase: "starting" }));
  assert.equal(untrusted.sent.length, 0);
  assert.equal(trusted.sent.length, 0);
  assert.deepEqual(calls, { get: 0, retry: 0 });
  bridge.stop();
});

test("main creates the window and loads it before starting the single runtime owner", () => {
  const main = readFileSync(new URL("../dist/main.js", import.meta.url), "utf8");
  const windowIndex = main.indexOf("const win = createWindow();");
  const startIndex = main.indexOf("startup.run(() => controller.start())");
  assert.ok(windowIndex >= 0, "early window creation missing");
  assert.ok(startIndex >= 0, "async runtime start missing");
  assert.ok(windowIndex < startIndex, "window must be created/load-triggered before startup");
  assert.doesNotMatch(main, /new RuntimeSupervisor/, "main must not construct supervisors itself");
  assert.match(main, /registerRuntimeIpc\)\([^,]+, runtimeBridge\)/);
  assert.match(main, /new [\w.]+RuntimeBridge\(controller, authorization\)/);
  assert.match(main, /stopSupervisor: \(\) => \(runtime \? runtime\.stop\(\) : Promise\.resolve\(\)\)/);
  // 冒烟 no-core 不启动 Core，但必须是有限失败而非永久 preparing。
  assert.match(main, /markFailed\("startup_failed", false\)/);
  // ready 冒烟在窗口出现后等待启动收敛，再执行既有断言。
  assert.match(main, /await startupTask\.catch/);
});

test("preload exposes only the finite runtime methods and fixed channels", () => {
  const preload = readFileSync(new URL("../dist/preload.js", import.meta.url), "utf8");
  for (const channel of [
    RUNTIME_GET_CHANNEL,
    RUNTIME_RETRY_CHANNEL,
    RUNTIME_ATTACH_CHANNEL,
    RUNTIME_DETACH_CHANNEL,
    RUNTIME_CHANGED_CHANNEL,
  ]) {
    assert.ok(preload.includes(channel), `missing channel ${channel}`);
  }
  assert.match(preload, /getSnapshot:\s*\(\)\s*=>/);
  assert.match(preload, /retry:\s*\(\)\s*=>/);
  assert.match(preload, /onChanged:\s*\(/);
  // 订阅先登记 changed 监听，再发送 attach。
  const listenIndex = preload.indexOf('subscribe("yaomu:runtime:changed"');
  const attachIndex = preload.indexOf('ipcRenderer.send("yaomu:runtime:attach")');
  assert.ok(listenIndex >= 0 && attachIndex > listenIndex);
  // sandbox preload 不 require 本地模块，也不暴露连接细节。
  assert.doesNotMatch(preload, /require\("\.\//);
  assert.doesNotMatch(preload, /baseUrl|instanceToken|X-YaoMu-Token/);
});

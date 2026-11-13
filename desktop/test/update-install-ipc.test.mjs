import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  UPDATE_CANCEL_INSTALL_CHANNEL,
  UPDATE_GET_INSTALL_STATE_CHANNEL,
  UPDATE_INSTALL_STATE_ATTACH_CHANNEL,
  UPDATE_INSTALL_STATE_CHANNEL,
  UPDATE_INSTALL_STATE_DETACH_CHANNEL,
  UpdateInstallBridge,
  registerUpdateInstallIpc,
} = require("../dist/ipc/update-install.js");
const { DesktopAuthorization } = require("../dist/ipc/authorization.js");

const appURL = "file:///app/ui/dist/index.html";

function snapshot(overrides = {}) {
  return {
    schemaVersion: 1,
    revision: 1,
    phase: "idle",
    reason: null,
    version: null,
    canCancel: false,
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
  const calls = { get: 0, install: 0, cancel: 0 };
  return {
    controller: {
      getSnapshot() {
        calls.get += 1;
        return { ...current };
      },
      install() {
        calls.install += 1;
        return { ...current };
      },
      cancelInstall() {
        calls.cancel += 1;
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
  error: { code: "UPDATE_INSTALL_ACCESS_DENIED", message: "本地访问被拒绝" },
};

test("get/install/cancel authorize before any read or side effect", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller, calls } = makeController();
  const bridge = new UpdateInstallBridge(controller, authorization);

  const fake = makeSender(99);
  const sameIdForged = makeSender(target.contents.id);
  const childFrame = { sender: target.contents, senderFrame: { url: appURL } };
  for (const [event, label] of [
    [fake.event, "unregistered window"],
    [sameIdForged.event, "forged contents"],
    [childFrame, "child frame"],
  ]) {
    assert.deepEqual(bridge.handleGet(event), denied, `get: ${label}`);
    assert.deepEqual(bridge.handleInstall(event), denied, `install: ${label}`);
    assert.deepEqual(bridge.handleCancel(event), denied, `cancel: ${label}`);
    assert.throws(() => bridge.handleAttach(event), /LOCAL_ACCESS_DENIED/, `attach: ${label}`);
    assert.throws(() => bridge.handleDetach(event), /LOCAL_ACCESS_DENIED/, `detach: ${label}`);
  }

  target.contents.mainFrame.url = "https://untrusted.example/";
  assert.deepEqual(bridge.handleInstall(target.event), denied, "navigated install");
  target.contents.mainFrame.url = appURL;
  target.contents.destroy();
  assert.deepEqual(bridge.handleInstall(target.event), denied, "destroyed install");
  assert.deepEqual(calls, { get: 0, install: 0, cancel: 0 });
  bridge.stop();
});

test("trusted calls return the finite snapshot envelope", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const waiting = snapshot({
    revision: 4,
    phase: "waiting",
    reason: "active_work",
    version: "2.0.0",
    canCancel: true,
  });
  const { controller, calls } = makeController(waiting);
  const bridge = new UpdateInstallBridge(controller, authorization);

  assert.deepEqual(bridge.handleGet(target.event), { ok: true, data: waiting });
  assert.deepEqual(bridge.handleInstall(target.event), { ok: true, data: waiting });
  assert.deepEqual(bridge.handleCancel(target.event), { ok: true, data: waiting });
  assert.deepEqual(calls, { get: 1, install: 1, cancel: 1 });
  bridge.stop();
});

test("broadcast is reference-counted, schema-checked and prunes untrusted windows", () => {
  const authorization = new DesktopAuthorization();
  const a = makeSender(1);
  const b = makeSender(2);
  register(authorization, a);
  register(authorization, b);
  const { controller } = makeController();
  const bridge = new UpdateInstallBridge(controller, authorization);

  bridge.handleAttach(a.event);
  bridge.handleAttach(a.event);
  bridge.handleAttach(b.event);

  controller.emit(
    snapshot({ revision: 2, phase: "waiting", reason: "active_work", version: "1.0.0", canCancel: true }),
  );
  assert.equal(a.sent.length, 1, "one broadcast per window even with two preload subscriptions");
  assert.equal(b.sent.length, 1);
  assert.equal(a.sent[0].channel, UPDATE_INSTALL_STATE_CHANNEL);

  // 不符合 schema 的快照绝不推给 Renderer（额外字段/非法组合）。
  controller.emit({ ...snapshot({ revision: 3 }), extra: true });
  controller.emit(snapshot({ revision: 3, phase: "preparing", version: "1.0.0", canCancel: true }));
  assert.equal(a.sent.length, 1, "invalid snapshots are dropped");
  assert.equal(b.sent.length, 1);

  a.contents.mainFrame.url = "https://untrusted.example/";
  controller.emit(snapshot({ revision: 4, phase: "preparing", version: "1.0.0" }));
  assert.equal(a.sent.length, 1, "navigated window receives nothing and is pruned");
  assert.equal(b.sent.length, 2);

  bridge.handleDetach(b.event);
  controller.emit(snapshot({ revision: 5, phase: "blocked", reason: "runtime_changed", version: "1.0.0" }));
  assert.equal(b.sent.length, 2, "last detach stops pushes");
  bridge.stop();
});

test("navigation/destruction hooks and stop release subscriptions", () => {
  const authorization = new DesktopAuthorization();
  const target = makeSender();
  register(authorization, target);
  const { controller } = makeController();
  const bridge = new UpdateInstallBridge(controller, authorization);

  bridge.handleAttach(target.event);
  bridge.detachWindow(target.contents);
  controller.emit(snapshot({ revision: 2, phase: "preparing", version: "1.0.0" }));
  assert.equal(target.sent.length, 0);

  bridge.handleAttach(target.event);
  target.contents.destroy();
  controller.emit(snapshot({ revision: 3, phase: "preparing", version: "1.0.0" }));
  assert.equal(target.sent.length, 0);

  const other = makeSender(3);
  register(authorization, other);
  bridge.handleAttach(other.event);
  assert.equal(controller.listenerCount(), 1);
  bridge.stop();
  assert.equal(controller.listenerCount(), 0);
  controller.emit(snapshot({ revision: 4, phase: "preparing", version: "1.0.0" }));
  assert.equal(other.sent.length, 0);
});

test("registerUpdateInstallIpc exposes exactly the fixed channels and keeps send denial silent", () => {
  const authorization = new DesktopAuthorization();
  const trusted = makeSender(1);
  register(authorization, trusted);
  const untrusted = makeSender(2);
  const { controller, calls } = makeController();
  const bridge = new UpdateInstallBridge(controller, authorization);
  const handlers = new Map();
  const ipc = {
    handle(channel, listener) {
      handlers.set(channel, listener);
    },
    on(channel, listener) {
      handlers.set(channel, listener);
    },
  };
  registerUpdateInstallIpc(ipc, bridge);

  assert.deepEqual(
    [...handlers.keys()].sort(),
    [
      UPDATE_GET_INSTALL_STATE_CHANNEL,
      "update:install",
      UPDATE_CANCEL_INSTALL_CHANNEL,
      UPDATE_INSTALL_STATE_ATTACH_CHANNEL,
      UPDATE_INSTALL_STATE_DETACH_CHANNEL,
    ].sort(),
  );
  assert.deepEqual(handlers.get(UPDATE_GET_INSTALL_STATE_CHANNEL)(untrusted.event), denied);
  assert.deepEqual(handlers.get("update:install")(untrusted.event), denied);
  assert.deepEqual(handlers.get(UPDATE_CANCEL_INSTALL_CHANNEL)(untrusted.event), denied);
  assert.doesNotThrow(() => handlers.get(UPDATE_INSTALL_STATE_ATTACH_CHANNEL)(untrusted.event));
  assert.doesNotThrow(() => handlers.get(UPDATE_INSTALL_STATE_DETACH_CHANNEL)(untrusted.event));
  controller.emit(snapshot({ revision: 2, phase: "preparing", version: "1.0.0" }));
  assert.equal(untrusted.sent.length, 0);
  assert.equal(trusted.sent.length, 0, "unattached trusted window receives no push");
  assert.deepEqual(calls, { get: 0, install: 0, cancel: 0 });
  bridge.stop();
});

test("main disables implicit install and routes install through the guarded bridge", () => {
  const main = readFileSync(new URL("../dist/main.js", import.meta.url), "utf8");
  assert.match(main, /autoInstallOnAppQuit = false/);
  assert.doesNotMatch(main, /autoInstallOnAppQuit = true/);
  // update:install 不再在 handler 内直接调用安装器。
  assert.doesNotMatch(main, /ipcMain\.handle\("update:install"[\s\S]{0,200}quitAndInstall/);
  // 普通退出同步取消同意，且在停止 Core 之前调用。
  const quitIndex = main.indexOf('app.on("before-quit"');
  const cancelIndex = main.indexOf("updateInstall?.cancelConsent()", quitIndex);
  const stopIndex = main.indexOf("stopSupervisor:", quitIndex);
  assert.ok(quitIndex >= 0 && cancelIndex > quitIndex, "before-quit must cancel pending consent");
  assert.ok(stopIndex > cancelIndex, "consent is cancelled before the shutdown convergence");
  // 唯一的更新安装桥/守卫组装点。
  assert.match(main, /new update_install_1\.UpdateInstallBridge\(/);
  assert.match(main, /registerUpdateInstallIpc\)\(electron_1\.ipcMain, updateInstallBridge\)/);
  assert.match(main, /getAcceptedIdentity\(\)/);
  assert.match(main, /noteDownloadedVersion\(downloadedVersion\)/);
  // BaseUpdater 6.x 安装失败走 error 事件而非 throw：必须收敛为 install_failed。
  assert.match(main, /autoUpdater\.on\("error"[\s\S]{0,400}noteInstallerError/);
  // 不存在生产环境变量绕过守卫或注入 fake updater。
  assert.doesNotMatch(main, /YAOMU_[A-Z_]*UPDATE/);
  assert.doesNotMatch(main, /fakeUpdater|FakeUpdater/);
});

test("preload exposes finite install-state methods with listen-before-attach and idempotent unsubscribe", () => {
  const preload = readFileSync(new URL("../dist/preload.js", import.meta.url), "utf8");
  for (const channel of [
    UPDATE_GET_INSTALL_STATE_CHANNEL,
    "update:install",
    UPDATE_CANCEL_INSTALL_CHANNEL,
    UPDATE_INSTALL_STATE_ATTACH_CHANNEL,
    UPDATE_INSTALL_STATE_DETACH_CHANNEL,
    UPDATE_INSTALL_STATE_CHANNEL,
  ]) {
    assert.ok(preload.includes(channel), `missing channel ${channel}`);
  }
  assert.match(preload, /getInstallState:\s*\(\)\s*=>/);
  assert.match(preload, /cancelInstall:\s*\(\)\s*=>/);
  assert.match(preload, /onInstallState:\s*\(/);
  const listenIndex = preload.indexOf('subscribe("update:install-state"');
  const attachIndex = preload.indexOf('ipcRenderer.send("update:install-state:attach")');
  assert.ok(listenIndex >= 0 && attachIndex > listenIndex, "listener must be installed before attach");
  assert.match(preload, /if \(!active\)\s*return;/);
  // preload 不携带 Core 地址、令牌或身份字符串。
  assert.doesNotMatch(preload, /baseUrl|instanceToken|X-YaoMu-Token|workspaceId|instanceId|serverEpoch/);
});

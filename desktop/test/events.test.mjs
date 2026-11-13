import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import vm from "node:vm";

const require = createRequire(import.meta.url);
const { EventBroker, EventStream } = require("../dist/events.js");

// 通道名来自 contracts/desktop-events.md；测试直接使用契约字面量，避免把常量暴露成公共 API。
const CONNECTED_CHANNEL = "yaomu:api:events-connected";
const EVENT_CHANNEL = "yaomu:api:event";
const ERROR_CHANNEL = "yaomu:api:event-error";

const appURL = "file:///app/ui/dist/index.html";
const connection = { baseUrl: "http://127.0.0.1:19477", token: "test-instance-token" };

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
  return { contents, sent };
}

/** 可手动喂帧的 SSE 响应；body 保持打开直到 close()。 */
function sseResponse() {
  const encoder = new TextEncoder();
  let controller;
  const body = new ReadableStream({
    start(value) {
      controller = value;
    },
  });
  return {
    response: new Response(body, { status: 200 }),
    send(frame) {
      controller.enqueue(encoder.encode(frame));
    },
    close() {
      try {
        controller.close();
      } catch {
        // 已关闭/已取消时不重复关闭。
      }
    },
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

async function settle(ms = 40) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitFor(predicate, label, timeoutMs = 2_000) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`);
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

const countChannel = (sender, channel) =>
  sender.sent.filter((message) => message.channel === channel).length;

test("EventStream announces a successful connection and contains callback errors", async () => {
  const body = sseResponse();
  const events = [];
  const errors = [];
  let connected = 0;
  let fetches = 0;
  const stream = new EventStream({
    connection,
    fetchImpl: async () => {
      fetches += 1;
      return body.response;
    },
    onConnected: () => {
      connected += 1;
      throw new Error("renderer callback failure");
    },
    onEvent: (event) => events.push(event),
    onError: (error) => errors.push(error),
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  stream.start();
  await waitFor(() => connected === 1, "connection callback");
  body.send('data: {"seq":0,"type":"task.progress","task_id":"task-1"}\n\n');
  await waitFor(() => events.length === 1, "first event");
  await settle();
  assert.equal(connected, 1);
  assert.equal(fetches, 1, "callback errors must not trigger a new transport attempt");
  assert.deepEqual(errors, []);
  stream.stop();
  body.close();
});

test("EventStream does not announce a connection for a failed response", async () => {
  const errors = [];
  let connected = 0;
  const stream = new EventStream({
    connection,
    fetchImpl: async () => new Response("upstream down", { status: 503 }),
    onConnected: () => {
      connected += 1;
    },
    onEvent: () => {},
    onError: (error) => errors.push(error),
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  stream.start();
  await waitFor(() => errors.length >= 1, "retryable error report");
  assert.equal(errors[0].code, "CORE_NOT_READY");
  assert.equal(connected, 0);
  stream.stop();
});

test("a stopped stream never publishes a late connection, event or error", async () => {
  const pending = deferred();
  const seen = [];
  const stream = new EventStream({
    connection,
    fetchImpl: () => pending.promise,
    onConnected: () => seen.push("connected"),
    onEvent: () => seen.push("event"),
    onError: () => seen.push("error"),
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  stream.start();
  stream.stop();
  const body = sseResponse();
  body.send('data: {"seq":1,"type":"task.created"}\n\n');
  pending.resolve(body.response);
  await settle();
  assert.deepEqual(seen, []);
  body.close();
});

test("EventBroker sends the fixed connection notification to authorized subscribers only", async () => {
  const body = sseResponse();
  const trusted = makeSender(1);
  const untrusted = makeSender(2);
  const broker = new EventBroker({
    fetchImpl: async () => body.response,
    isTrustedSender: (sender) => sender === trusted.contents,
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  broker.setConnection(connection);
  broker.attach(trusted.contents);
  broker.attach(untrusted.contents);

  await waitFor(() => countChannel(trusted, CONNECTED_CHANNEL) === 1, "connection notification");
  const notification = trusted.sent.find((message) => message.channel === CONNECTED_CHANNEL);
  assert.deepEqual(notification.payload, { schemaVersion: 1 });
  assert.deepEqual(Object.keys(notification.payload), ["schemaVersion"]);
  await settle();
  assert.equal(untrusted.sent.length, 0, "unauthorized contents must not receive the signal");
  assert.equal(countChannel(trusted, CONNECTED_CHANNEL), 1);
  broker.stop();
  body.close();
});

test("automatic reconnect re-announces with no new business event and seq=0 keeps the cursor", async () => {
  const bodies = [];
  const urls = [];
  const sender = makeSender(1);
  const broker = new EventBroker({
    fetchImpl: async (url) => {
      urls.push(url);
      const body = sseResponse();
      bodies.push(body);
      return body.response;
    },
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  broker.setConnection(connection);
  broker.attach(sender.contents, 7);

  await waitFor(() => bodies.length === 1, "first connection");
  assert.match(urls[0], /after_seq=7/);
  await waitFor(() => countChannel(sender, CONNECTED_CHANNEL) === 1, "first announcement");
  bodies[0].send('data: {"seq":0,"type":"task.progress","task_id":"task-1"}\n\n');
  await waitFor(() => countChannel(sender, EVENT_CHANNEL) === 1, "progress event");
  bodies[0].close();

  await waitFor(() => bodies.length === 2, "automatic reconnect");
  assert.match(urls[1], /after_seq=7/, "seq=0 must not advance the durable cursor");
  await waitFor(() => countChannel(sender, CONNECTED_CHANNEL) === 2, "reconnect announcement");
  broker.stop();
  bodies[1].close();
});

test("seq>0 events advance the reconnect cursor", async () => {
  const bodies = [];
  const urls = [];
  const sender = makeSender(1);
  const broker = new EventBroker({
    fetchImpl: async (url) => {
      urls.push(url);
      const body = sseResponse();
      bodies.push(body);
      return body.response;
    },
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  broker.setConnection(connection);
  broker.attach(sender.contents);

  await waitFor(() => bodies.length === 1, "first connection");
  bodies[0].send('data: {"seq":4,"type":"task.state_changed","task_id":"task-1"}\n\n');
  await waitFor(() => countChannel(sender, EVENT_CHANNEL) === 1, "domain event");
  bodies[0].close();
  await waitFor(() => bodies.length === 2, "automatic reconnect");
  assert.match(urls[1], /after_seq=4/);
  broker.stop();
  bodies[1].close();
});

test("late responses from an obsolete stream cannot leak to a new subscriber", async () => {
  const pending = [];
  const sender = makeSender(1);
  const broker = new EventBroker({
    fetchImpl: () => {
      const request = deferred();
      pending.push(request);
      return request.promise;
    },
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  broker.setConnection(connection);
  broker.attach(sender.contents);
  await waitFor(() => pending.length === 1, "first fetch");
  broker.detach(sender.contents);
  broker.attach(sender.contents);
  await waitFor(() => pending.length === 2, "second fetch");

  // 旧流此时才返回可恢复错误：它的 onError/onConnected 都不能落到新订阅上。
  pending[0].resolve(new Response("expired", { status: 409 }));
  await settle();
  assert.equal(sender.sent.length, 0, "obsolete stream callbacks leaked to the new subscription");

  const body = sseResponse();
  pending[1].resolve(body.response);
  await waitFor(() => countChannel(sender, CONNECTED_CHANNEL) === 1, "new connection announcement");
  body.send('data: {"seq":1,"type":"task.created","task_id":"task-1"}\n\n');
  await waitFor(() => countChannel(sender, EVENT_CHANNEL) === 1, "new stream event");
  assert.equal(countChannel(sender, ERROR_CHANNEL), 0);
  broker.stop();
  body.close();
});

test("per-window subscription is reference counted and repeated detach is safe", async () => {
  let fetches = 0;
  const broker = new EventBroker({
    fetchImpl: async () => {
      fetches += 1;
      return sseResponse().response;
    },
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  broker.setConnection(connection);
  const sender = makeSender(1);
  broker.attach(sender.contents);
  broker.attach(sender.contents);
  await waitFor(() => fetches === 1, "first stream");
  broker.detach(sender.contents);
  broker.detach(sender.contents);
  broker.detach(sender.contents);
  await settle();
  assert.equal(fetches, 1, "redundant detaches must not create another stream");
  broker.stop();
});

test("preload subscribes to the connection signal locally before attach", () => {
  const preload = readFileSync(new URL("../dist/preload.js", import.meta.url), "utf8");
  assert.ok(preload.includes("yaomu:api:events-connected"));
  const listen = preload.indexOf('subscribe("yaomu:api:events-connected"');
  const attach = preload.indexOf('ipcRenderer.send("yaomu:events:attach"');
  assert.ok(listen >= 0, "connection listener missing");
  assert.ok(attach > listen, "connection listener must be registered before attach");
  assert.match(preload, /schemaVersion === 1/);
  assert.match(preload, /Object\.keys/);
  assert.doesNotMatch(preload, /require\("\.\//);
  assert.doesNotMatch(preload, /instanceToken|baseUrl/);
});

/** 在带假 electron 的隔离上下文中执行编译后的 preload，验证桥接行为。 */
function loadPreload() {
  const source = readFileSync(new URL("../dist/preload.js", import.meta.url), "utf8");
  const listeners = new Map();
  const operations = [];
  const sent = [];
  let exposed;
  const ipcRenderer = {
    on(channel, listener) {
      operations.push({ kind: "on", channel });
      const set = listeners.get(channel) ?? new Set();
      set.add(listener);
      listeners.set(channel, set);
    },
    removeListener(channel, listener) {
      operations.push({ kind: "off", channel });
      listeners.get(channel)?.delete(listener);
    },
    send(channel, payload) {
      operations.push({ kind: "send", channel });
      sent.push({ channel, payload });
    },
    invoke: async () => ({ ok: true, data: {} }),
    sendSync: () => "",
  };
  vm.runInNewContext(
    source,
    {
      require: (id) => {
        if (id !== "electron") throw new Error(`unexpected require: ${id}`);
        return {
          contextBridge: {
            exposeInMainWorld(name, value) {
              exposed = { name, value };
            },
          },
          ipcRenderer,
          webUtils: {},
        };
      },
      exports: {},
      module: { exports: {} },
      process: { platform: "darwin" },
      console,
      setTimeout,
      clearTimeout,
      queueMicrotask,
    },
    { filename: "preload.js" },
  );
  return { api: exposed.value.api, listeners, operations, sent };
}

test("preload only forwards the exact connection payload and unsubscribes everything once", () => {
  const { api, listeners, operations, sent } = loadPreload();
  let connected = 0;
  let events = 0;
  const unsubscribe = api.subscribeEvents(
    (event) => {
      events += event ? 1 : 0;
    },
    () => {},
    {
      afterSeq: 3,
      onConnected: () => {
        connected += 1;
        throw new Error("renderer callback failure");
      },
    },
  );

  assert.deepEqual(sent[0], { channel: "yaomu:events:attach", payload: 3 });
  const attachIndex = operations.findIndex(
    (operation) => operation.kind === "send" && operation.channel === "yaomu:events:attach",
  );
  assert.ok(attachIndex > 0, "attach must be sent");
  assert.ok(
    operations.slice(0, attachIndex).every((operation) => operation.kind === "on"),
    "all listeners must be registered before attach",
  );
  assert.deepEqual(operations.slice(0, attachIndex).map((operation) => operation.channel).sort(), [
    "yaomu:api:event",
    "yaomu:api:event-error",
    "yaomu:api:events-connected",
  ]);

  const deliver = (channel, payload) => {
    for (const listener of [...(listeners.get(channel) ?? [])]) listener({}, payload);
  };
  for (const invalid of [
    null,
    "{\"schemaVersion\":1}",
    { schemaVersion: 2 },
    { schemaVersion: 1, extra: true },
  ]) {
    deliver("yaomu:api:events-connected", invalid);
  }
  assert.equal(connected, 0, "invalid notifications must be ignored");
  deliver("yaomu:api:events-connected", { schemaVersion: 1 });
  assert.equal(connected, 1);
  assert.equal(sent.length, 1, "renderer callback errors must not reach the transport");

  deliver("yaomu:api:event", { seq: 0 });
  assert.equal(events, 1);

  unsubscribe();
  unsubscribe();
  assert.equal(sent.length, 2);
  assert.deepEqual(sent[1], { channel: "yaomu:events:detach", payload: undefined });
  for (const channel of [
    "yaomu:api:event",
    "yaomu:api:event-error",
    "yaomu:api:events-connected",
  ]) {
    assert.equal(listeners.get(channel).size, 0, `${channel} listener leaked`);
  }
});

// 服务端按游标分批补读时会交付部分事件后正常结束本次流；客户端必须按最后
// seq 续订且不报 CORE_NOT_READY。
test("a delivered batch that ends reconnects quietly from the last seq", async () => {
  const urls = [];
  const events = [];
  const errors = [];
  const stream = new EventStream({
    connection,
    fetchImpl: async (url) => {
      urls.push(String(url));
      const seq = Number(new URL(String(url)).searchParams.get("after_seq") ?? "0") + 1;
      const body = sseResponse();
      body.send(`data: {"seq":${seq},"type":"task.created"}\n\n`);
      body.close();
      return body.response;
    },
    onEvent: (event) => events.push(event),
    onError: (error) => errors.push(error),
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  stream.start();
  await waitFor(() => events.length >= 3, "batch continuation");
  stream.stop();
  assert.deepEqual(errors, [], "normal batch continuation must not report CORE_NOT_READY");
  assert.match(urls[1], /after_seq=1/);
  assert.match(urls[2], /after_seq=2/);
});

// 连接建立后立刻断流且一个有效帧都没有，仍必须报 CORE_NOT_READY 并进入退避。
test("a connection that ends before delivering a frame reports core unavailability", async () => {
  const errors = [];
  const stream = new EventStream({
    connection,
    fetchImpl: async () => {
      const body = sseResponse();
      body.close();
      return body.response;
    },
    onEvent: () => {},
    onError: (error) => errors.push(error),
    retryDelayMs: 5,
    maxRetryDelayMs: 5,
  });
  stream.start();
  await waitFor(() => errors.length >= 1, "zero-frame disconnect");
  stream.stop();
  assert.equal(errors[0].code, "CORE_NOT_READY");
});

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const { UpdateScheduler } = require("../dist/update-scheduler.js");

const HOUR = 60 * 60 * 1000;
const FOUR_HOURS = 4 * HOUR;

function flush() {
  return new Promise((resolve) => setImmediate(resolve));
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

function harness(overrides = {}) {
  const timers = new Map();
  let nextTimer = 0;
  const state = {
    enabled: true,
    checkBlocked: false,
    installActive: false,
    now: 1_000_000,
    checkCalls: [],
    errors: [],
    logs: [],
  };
  const options = {
    isEnabled: () => state.enabled,
    isCheckBlocked: () => state.checkBlocked,
    isInstallActive: () => state.installActive,
    check: (source) => {
      state.checkCalls.push(source);
      return overrides.check ? overrides.check(source) : Promise.resolve("ok");
    },
    onError: (source, error) => state.errors.push({ source, error }),
    log: (line) => state.logs.push(line),
    now: () => state.now,
    setInterval: (callback, delayMs) => {
      const handle = ++nextTimer;
      timers.set(handle, { callback, delayMs });
      return handle;
    },
    clearInterval: (handle) => timers.delete(handle),
    intervalMs: FOUR_HOURS,
    focusMinIntervalMs: HOUR,
    ...overrides.options,
  };
  const scheduler = new UpdateScheduler(options);
  return { scheduler, state, timers };
}

test("starts a four-hour interval only when enabled", async () => {
  const { scheduler, state, timers } = harness();
  scheduler.start();
  assert.equal(timers.size, 1);
  const [timer] = [...timers.values()];
  assert.equal(timer.delayMs, FOUR_HOURS);

  state.enabled = false;
  scheduler.start();
  assert.equal(timers.size, 0);
});

test("interval and focus checks are deduplicated while one check is in flight", async () => {
  const pending = deferred();
  const { scheduler, state, timers } = harness({ check: () => pending.promise });
  scheduler.start();
  const [timer] = [...timers.values()];
  assert.equal(timer.delayMs, FOUR_HOURS);

  timer.callback();
  await flush();
  assert.deepEqual(state.checkCalls, ["auto"]);

  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, ["auto"]);

  pending.resolve("ok");
  await pending.promise;
  await flush();
  assert.equal(state.checkCalls.length, 1);
});

test("focus checks are throttled to one per hour", async () => {
  const { scheduler, state } = harness();
  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, ["focus"]);

  state.now += HOUR - 1;
  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, ["focus"]);

  state.now += 1;
  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, ["focus", "focus"]);
});

test("disabled automatic checks skip interval and focus but never manual checks", async () => {
  const { scheduler, state, timers } = harness();
  state.enabled = false;
  scheduler.start();
  assert.equal(timers.size, 0);

  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, []);

  await scheduler.checkNow("manual");
  assert.deepEqual(state.checkCalls, ["manual"]);
});

test("install activity skips both automatic and manual checks", async () => {
  const { scheduler, state } = harness();
  state.installActive = true;
  await scheduler.checkNow("auto");
  await scheduler.checkNow("manual");
  assert.deepEqual(state.checkCalls, []);
});

test("stopping clears the interval and disabling stops future automatic checks", async () => {
  const { scheduler, timers } = harness();
  scheduler.start();
  assert.equal(timers.size, 1);
  scheduler.stop();
  assert.equal(timers.size, 0);

  scheduler.setEnabled(false);
  scheduler.onFocus();
  assert.equal(timers.size, 0);
});

test("errors keep their source; a manual request promotes an in-flight automatic check", async () => {
  const pending = deferred();
  const { scheduler, state } = harness({ check: () => pending.promise });
  const auto = scheduler.checkNow("auto");
  await flush();
  const manual = scheduler.checkNow("manual");
  assert.equal(auto, manual);

  pending.reject(new Error("offline"));
  await manual;

  assert.equal(state.errors.length, 1);
  assert.equal(state.errors[0].source, "manual");
  assert.match(state.errors[0].error.message, /offline/);
  assert.equal(state.checkCalls.length, 1);
});

test("an automatic check failure is reported as auto and does not keep the scheduler busy", async () => {
  const { scheduler, state } = harness({
    check: () => Promise.reject(new Error("feed unavailable")),
  });
  await scheduler.checkNow("auto");
  assert.deepEqual(state.errors.map((item) => item.source), ["auto"]);

  await scheduler.checkNow("manual");
  assert.equal(state.checkCalls.at(-1), "manual");
});

test("a synchronous check throw is converted into onError instead of an unhandled rejection", async () => {
  const { scheduler, state } = harness();
  const originalCheck = scheduler.options.check;
  scheduler.options.check = () => {
    throw new Error("boom");
  };
  await scheduler.checkNow("auto");
  assert.equal(state.errors.length, 1);
  assert.equal(state.errors[0].source, "auto");
  scheduler.options.check = originalCheck;
});

test("a transient block does not prevent the interval and later clears", async () => {
  const { scheduler, state, timers } = harness();
  scheduler.start();
  assert.equal(timers.size, 1);
  const [timer] = [...timers.values()];

  state.checkBlocked = true;
  timer.callback();
  await flush();
  scheduler.onFocus();
  await flush();
  assert.deepEqual(state.checkCalls, []);

  // manual 不被临时阻断；用户可以主动检查新版本。
  await scheduler.checkNow("manual");
  assert.deepEqual(state.checkCalls, ["manual"]);

  state.checkBlocked = false;
  timer.callback();
  await flush();
  assert.deepEqual(state.checkCalls, ["manual", "auto"]);
});

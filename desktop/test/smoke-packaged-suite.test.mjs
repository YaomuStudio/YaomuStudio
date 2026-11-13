import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import test from "node:test";
import {
  describeRunFacts,
  formatMissingResult,
  isScenarioPass,
  parseSmokeResult,
  runAppScenario,
  runScenarioSequence,
} from "../scripts/smoke-packaged-suite.mjs";

function createFakeChild() {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.pid = 7300;
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

function stopOnKill(child) {
  child.kill = (signal = "SIGTERM") => {
    child.kills.push(signal);
    queueMicrotask(() => child.finish(null, signal));
    return true;
  };
  return child;
}

function options(overrides = {}) {
  return {
    executable: "/fake/app",
    args: [],
    cwd: "/tmp",
    env: {},
    spawnImpl: () => createFakeChild(),
    timeoutMs: 500,
    cleanupGraceMs: 30,
    stdoutLimit: 4096,
    stderrLimit: 1024,
    platform: "linux",
    // 测试绝不向真实进程组发信号；fake child 的 kill 只更新自身状态。
    killTreeImpl: (child) => child.kill("SIGKILL"),
    ...overrides,
  };
}

const smokeLine =
  "SMOKE_RESULT " +
  JSON.stringify({ mode: "ready", ok: true, checks: [], consoleErrors: [] }) +
  "\n";

test("normal completion parses SMOKE_RESULT from a confirmed close", async () => {
  const child = createFakeChild();
  const run = await runAppScenario(
    options({
      spawnImpl: () => {
        queueMicrotask(() => {
          child.stdout.write(smokeLine);
          child.finish(0);
        });
        return child;
      },
    }),
  );
  assert.equal(run.closed, true);
  assert.equal(run.exitUnconfirmed, false);
  assert.equal(run.code, 0);
  assert.equal(parseSmokeResult(run.stdout)?.ok, true);
});

test("timeout without exit evidence keeps an explicit unconfirmed fact", async () => {
  const child = createFakeChild(); // kill 仅 ACK，不产生 exit/close
  const started = Date.now();
  const run = await runAppScenario(
    options({
      timeoutMs: 20,
      cleanupGraceMs: 20,
      spawnImpl: () => child,
    }),
  );
  assert.equal(run.timedOut, true);
  assert.equal(run.exitUnconfirmed, true);
  assert.equal(run.closed, false);
  assert.ok(Date.now() - started < 500, "bounded cleanup must not hang");
  assert.ok(child.kills.length >= 1);
});

test("a live child error is not exit evidence and cleanup is still tracked", async () => {
  const acked = createFakeChild();
  const unconfirmed = await runAppScenario(
    options({
      cleanupGraceMs: 20,
      spawnImpl: () => {
        queueMicrotask(() =>
          acked.emit("error", Object.assign(new Error("EPERM"), { code: "EPERM" })),
        );
        return acked;
      },
    }),
  );
  assert.equal(unconfirmed.liveError, true);
  assert.equal(unconfirmed.exitUnconfirmed, true);
  assert.equal(unconfirmed.closed, false);

  const stoppable = stopOnKill(createFakeChild());
  const confirmed = await runAppScenario(
    options({
      cleanupGraceMs: 100,
      spawnImpl: () => {
        queueMicrotask(() =>
          stoppable.emit("error", Object.assign(new Error("EPERM"), { code: "EPERM" })),
        );
        return stoppable;
      },
    }),
  );
  assert.equal(confirmed.liveError, true);
  assert.equal(confirmed.closed, true);
  assert.equal(confirmed.exitUnconfirmed, false);
});

test("stream overflow immediately starts owned cleanup and fails", async () => {
  const child = stopOnKill(createFakeChild());
  const started = Date.now();
  const run = await runAppScenario(
    options({
      timeoutMs: 5_000,
      cleanupGraceMs: 100,
      stdoutLimit: 32,
      spawnImpl: () => {
        queueMicrotask(() => child.stdout.write("x".repeat(200)));
        return child;
      },
    }),
  );
  assert.equal(run.outputOverflow, true);
  assert.equal(run.exitUnconfirmed, false);
  assert.equal(run.closed, true);
  assert.ok(child.kills.length >= 1);
  assert.ok(Date.now() - started < 1_000, "overflow must not wait for the scenario timeout");
});

test("sequence stops unless ready close is confirmed, and never before no-core twice", async () => {
  let noCoreCalls = 0;
  const stopped = await runScenarioSequence(
    async () => ({ exitUnconfirmed: true, closed: false }),
    async () => {
      noCoreCalls += 1;
      return { exitUnconfirmed: false, closed: true };
    },
  );
  assert.equal(noCoreCalls, 0);
  assert.equal(stopped.noCoreRun, null);
  assert.equal(stopped.stoppedForUnconfirmed, true);
  assert.equal(stopped.stopReason, "exit-unconfirmed");

  // exit 已到但 close/stdio 未收齐：仍停止整轮，不把它说成主 PID 未退出。
  const closeUnconfirmed = await runScenarioSequence(
    async () => ({ exitUnconfirmed: false, closed: false }),
    async () => {
      noCoreCalls += 1;
      return { exitUnconfirmed: false, closed: true };
    },
  );
  assert.equal(noCoreCalls, 0);
  assert.equal(closeUnconfirmed.noCoreRun, null);
  assert.equal(closeUnconfirmed.stopReason, "close-unconfirmed");

  const confirmed = await runScenarioSequence(
    async () => ({ exitUnconfirmed: false, closed: true }),
    async () => {
      noCoreCalls += 1;
      return { exitUnconfirmed: false, closed: true };
    },
  );
  assert.equal(noCoreCalls, 1);
  assert.equal(confirmed.stoppedForUnconfirmed, false);
  assert.equal(confirmed.stopReason, null);
});

test("missing SMOKE_RESULT report retains timeout/overflow/cleanup facts", () => {
  const line = formatMissingResult("ready", {
    code: null,
    signal: null,
    timedOut: true,
    outputOverflow: true,
    exitUnconfirmed: true,
  });
  assert.match(line, /^ready: no SMOKE_RESULT emitted/);
  assert.match(line, /timed-out=true/);
  assert.match(line, /output-overflow=true/);
  assert.match(line, /exit-unconfirmed=true/);
  assert.equal(describeRunFacts({ code: 3, signal: "SIGKILL" }).includes("exit=3"), true);
});

test("raw app output is never forwarded through the scenario log", async () => {
  const messages = [];
  const child = createFakeChild();
  await runAppScenario(
    options({
      log: (line) => messages.push(line),
      spawnImpl: () => {
        queueMicrotask(() => {
          child.stdout.write(`raw-marker ${smokeLine}`);
          child.stderr.write("raw-stderr-marker\n");
          child.finish(0);
        });
        return child;
      },
    }),
  );
  assert.equal(messages.some((line) => line.includes("raw-marker")), false);
  assert.equal(messages.some((line) => line.includes("raw-stderr-marker")), false);
});

test("scenario success requires confirmed close and no live error", () => {
  const confirmed = {
    code: 0,
    closed: true,
    liveError: false,
    timedOut: false,
    outputOverflow: false,
    exitUnconfirmed: false,
  };
  const okResult = { ok: true, consoleErrors: [] };
  assert.equal(isScenarioPass(okResult, confirmed), true);
  // 只凭 ok payload + code 0 不够：活错误或未确认 close 都不能 PASS。
  assert.equal(isScenarioPass(okResult, { ...confirmed, liveError: true }), false);
  assert.equal(isScenarioPass(okResult, { ...confirmed, closed: false }), false);
  assert.equal(isScenarioPass(okResult, { ...confirmed, code: 1 }), false);
  assert.equal(isScenarioPass(okResult, { ...confirmed, timedOut: true }), false);
  assert.equal(isScenarioPass(okResult, { ...confirmed, outputOverflow: true }), false);
  assert.equal(isScenarioPass(okResult, { ...confirmed, exitUnconfirmed: true }), false);
  assert.equal(isScenarioPass({ ok: true, consoleErrors: ["at least one"] }, confirmed), false);
  assert.equal(isScenarioPass(undefined, confirmed), false);
});

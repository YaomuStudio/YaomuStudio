import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  observeCoreRunnerEvidence,
  parseCoreRunnerEvidence,
  readCoreRunnerEvidence,
} = require("../dist/smoke-evidence.js");

function line(entry) {
  return JSON.stringify(entry);
}

function ready(overrides = {}) {
  return {
    ts: "2026-11-09T00:00:00.000Z",
    level: "info",
    component: "core",
    event: "core.ready",
    port: 51234,
    epoch: 7,
    ...overrides,
  };
}

function runnerReady(overrides = {}) {
  return {
    ts: "2026-11-09T00:00:01.000Z",
    level: "info",
    component: "core",
    event: "core.runner_ready",
    runner: "model",
    ...overrides,
  };
}

const query = { epoch: 7, port: 51234 };

test("model-side runner evidence requires ready before runner_ready for the same epoch/port", () => {
  const text = [line(ready()), line(runnerReady())].join("\n");
  assert.deepEqual(parseCoreRunnerEvidence(text, query), {
    ok: true,
    detail: "runner-model-ready",
  });
});

test("runner_ready before the matching core.ready is not accepted", () => {
  const text = [line(runnerReady()), line(ready())].join("\n");
  assert.deepEqual(parseCoreRunnerEvidence(text, query), {
    ok: false,
    detail: "runner-ready-before-ready",
  });
});

test("a later different startup resets earlier runner evidence", () => {
  const text = [
    line(ready()),
    line(runnerReady()),
    line(ready({ epoch: 8, port: 51235 })),
  ].join("\n");
  assert.deepEqual(parseCoreRunnerEvidence(text, query), {
    ok: false,
    detail: "missing-core-ready",
  });
});

test("wrong epoch or port is missing-core-ready", () => {
  const text = [line(ready({ epoch: 6 })), line(runnerReady())].join("\n");
  assert.equal(parseCoreRunnerEvidence(text, query).detail, "missing-core-ready");
  const wrongPort = [line(ready({ port: 51235 })), line(runnerReady())].join("\n");
  assert.equal(parseCoreRunnerEvidence(wrongPort, query).detail, "missing-core-ready");
});

test("non-model runner labels do not count as model-side readiness", () => {
  const text = [line(ready()), line(runnerReady({ runner: "host" }))].join("\n");
  assert.deepEqual(parseCoreRunnerEvidence(text, query), {
    ok: false,
    detail: "runner-not-model",
  });
});

test("malformed log lines are ignored rather than treated as evidence", () => {
  const text = ["not-json", "", line(ready()), "{broken", line(runnerReady())].join("\n");
  assert.equal(parseCoreRunnerEvidence(text, query).ok, true);
});

test("missing runner_ready stays runner-ready-missing", () => {
  const text = [line(ready()), line({ event: "core.runner_hello_failed", runner: "model" })].join(
    "\n",
  );
  assert.deepEqual(parseCoreRunnerEvidence(text, query), {
    ok: false,
    detail: "runner-ready-missing",
  });
});

test("readCoreRunnerEvidence reads the isolated current Core log", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-evidence-"));
  try {
    const logPath = path.join(root, "logs", "core.log");
    await mkdir(path.dirname(logPath), { recursive: true });
    await writeFile(logPath, `${[line(ready()), line(runnerReady())].join("\n")}\n`);
    const result = await readCoreRunnerEvidence({ logPath, epoch: 7, port: 51234 });
    assert.deepEqual(result, { ok: true, detail: "runner-model-ready" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("tail read keeps the newest evidence within the byte budget", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-evidence-tail-"));
  try {
    const logPath = path.join(root, "core.log");
    const noise = Array.from({ length: 200 }, (_, index) =>
      line({ event: "core.noise", index, padding: "x".repeat(120) }),
    );
    const text = [...noise, line(ready()), line(runnerReady())].join("\n") + "\n";
    await writeFile(logPath, text);
    const result = await readCoreRunnerEvidence({
      logPath,
      epoch: 7,
      port: 51234,
      maxBytes: 1024,
    });
    assert.deepEqual(result, { ok: true, detail: "runner-model-ready" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("missing or empty log is a fixed non-success", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-evidence-empty-"));
  try {
    const missing = await readCoreRunnerEvidence({
      logPath: path.join(root, "missing.log"),
      epoch: 7,
      port: 51234,
    });
    assert.deepEqual(missing, { ok: false, detail: "log-unavailable" });
    const emptyPath = path.join(root, "empty.log");
    await writeFile(emptyPath, "");
    assert.deepEqual(await readCoreRunnerEvidence({ logPath: emptyPath, epoch: 7, port: 51234 }), {
      ok: false,
      detail: "missing-core-ready",
    });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("pre-aborted signal rejects instead of returning evidence", async () => {
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    () =>
      readCoreRunnerEvidence({
        logPath: path.join(os.tmpdir(), "does-not-matter.log"),
        epoch: 1,
        port: 1,
        signal: controller.signal,
      }),
    (error) => error?.name === "AbortError",
  );
});

test("observeCoreRunnerEvidence polls the same startup until the model runner appears", async () => {
  const core = { epoch: 7, port: 51234, generation: 2 };
  let calls = 0;
  const result = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => core },
    {
      timeoutMs: 1_000,
      pollMs: 1,
      sleep: async () => undefined,
      readEvidence: async () => {
        calls += 1;
        return calls < 2
          ? { ok: false, detail: "runner-ready-missing" }
          : { ok: true, detail: "runner-model-ready" };
      },
    },
  );
  assert.deepEqual(result, { ok: true, detail: "runner-model-ready" });
  assert.equal(calls, 2);
});

test("observeCoreRunnerEvidence rejects a restart or lost current core", async () => {
  const core = { epoch: 7, port: 51234, generation: 2 };
  let polls = 0;
  const restarted = await observeCoreRunnerEvidence(
    {
      dataDir: "/tmp/yaomu-smoke",
      getCurrentCore: () => (polls++ === 0 ? core : { ...core, generation: 3 }),
    },
    {
      timeoutMs: 1_000,
      pollMs: 1,
      sleep: async () => undefined,
      readEvidence: async () => ({ ok: false, detail: "runner-ready-missing" }),
    },
  );
  assert.deepEqual(restarted, { ok: false, detail: "core-restarted" });

  const unavailable = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => null },
    { timeoutMs: 100, readEvidence: async () => ({ ok: true, detail: "runner-model-ready" }) },
  );
  assert.deepEqual(unavailable, { ok: false, detail: "current-core-unavailable" });
});

test("observeCoreRunnerEvidence rejects identity changes during an evidence read", async () => {
  let current = { epoch: 1, port: 12345, generation: 1 };
  const result = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => current },
    {
      timeoutMs: 100,
      readEvidence: async () => {
        current = { epoch: 2, port: 12346, generation: 2 };
        return { ok: true, detail: "runner-model-ready" };
      },
    },
  );
  assert.deepEqual(result, { ok: false, detail: "core-restarted" });
});

test("observeCoreRunnerEvidence budget covers a slow evidence read", async () => {
  const core = { epoch: 7, port: 51234, generation: 2 };
  const result = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => core },
    {
      timeoutMs: 20,
      readEvidence: () =>
        new Promise((resolve) =>
          setTimeout(() => resolve({ ok: true, detail: "runner-model-ready" }), 60),
        ),
    },
  );
  assert.equal(result.ok, false);
  assert.equal(result.detail, "evidence-timeout");
});

test("observeCoreRunnerEvidence is bounded and surfaces the last fixed reason", async () => {
  const core = { epoch: 7, port: 51234, generation: 2 };
  const timedOut = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => core },
    {
      timeoutMs: 0,
      readEvidence: async () => ({ ok: false, detail: "runner-ready-missing" }),
    },
  );
  assert.deepEqual(timedOut, { ok: false, detail: "evidence-timeout" });

  const unreadable = await observeCoreRunnerEvidence(
    { dataDir: "/tmp/yaomu-smoke", getCurrentCore: () => core },
    {
      timeoutMs: 30,
      readEvidence: async () => {
        throw new Error("read failed");
      },
    },
  );
  assert.equal(unreadable.ok, false);
  assert.ok(["log-unavailable", "evidence-timeout"].includes(unreadable.detail));
});

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const { createApiInvoker } = require("../dist/ipc/api.js");

const connection = () => ({ baseUrl: "http://127.0.0.1:1234", token: "test-instance" });

function timeoutError() {
  const error = new Error("The operation was aborted due to timeout");
  error.name = "TimeoutError";
  return error;
}

// 一次本机存储停顿会让提交请求超过旧的 30s 上限；用户看到的提示必须区分
// “Core 还在、只是超时”与“连不上 Core”，否则会去日志里找不存在的问题。
test("a timed-out submit reports a timeout instead of core unavailability", async () => {
  const invoke = createApiInvoker(connection, async () => {
    throw timeoutError();
  });
  const result = await invoke("createWorkflowRun", [
    { project_id: "proj-1", name: "p", template: "tool_voice_clone" },
  ]);
  assert.equal(result.ok, false);
  assert.equal(result.error.messageKey, "api.requestTimeout");
  assert.equal(result.error.retryable, true);
});

test("a transport failure still reports core unavailability", async () => {
  const invoke = createApiInvoker(connection, async () => {
    throw new TypeError("fetch failed");
  });
  const result = await invoke("createWorkflowRun", [{ project_id: "proj-1", name: "p" }]);
  assert.equal(result.ok, false);
  assert.equal(result.error.messageKey, "api.coreNotReady");
  assert.equal(result.error.retryable, true);
});

// 提交链路要能在一次本机存储停顿（数十秒）后拿到 Core 的真实结果，
// 其它调用保持原来的 30s 上限。
test("submit calls wait longer than the default call timeout", async () => {
  const recorded = [];
  const original = AbortSignal.timeout;
  AbortSignal.timeout = (ms) => {
    recorded.push(ms);
    return original.call(AbortSignal, ms);
  };
  try {
    const invoke = createApiInvoker(connection, async () => Response.json({ ok: true }));
    await invoke("getSystem", []);
    await invoke("preflight", [{ project_id: "proj-1" }]);
    await invoke("createWorkflowRun", [{ project_id: "proj-1", name: "p" }]);
  } finally {
    AbortSignal.timeout = original;
  }
  assert.deepEqual(recorded, [30_000, 120_000, 120_000]);
});

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(
  new URL("../scripts/smoke-packaged.mjs", import.meta.url),
);
const fakeSmokeResult = `console.log('SMOKE_RESULT ' + JSON.stringify({ mode: 'ready', ok: true, checks: [{ name: 'fake', ok: true }], consoleErrors: [] }));\n`;

function runScript(args) {
  return spawnSync(process.execPath, [scriptPath, ...args], {
    encoding: "utf8",
    timeout: 30_000,
  });
}

test("explicit --app without sibling resources is rejected before any PASS", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-smoke-cli-"));
  try {
    const fakeApp = path.join(root, "fake-app-for-rejection.cjs");
    await writeFile(fakeApp, fakeSmokeResult);
    const result = runScript(["--app", fakeApp]);
    assert.equal(result.status, 1);
    assert.match(`${result.stdout}\n${result.stderr}`, /未解析到包内 resources 目录/);
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /SMOKE PASS/);
    // 明确没有执行 fake app：它若被启动会产生 SMOKE_RESULT 输出。
    assert.doesNotMatch(result.stdout, /SMOKE_RESULT/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("explicit --app with a partial resources tree still fails, never SKIPs", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-smoke-cli-partial-"));
  try {
    let fakeApp;
    let resourcesDir;
    if (process.platform === "darwin") {
      fakeApp = path.join(root, "Fake.app", "Contents", "MacOS", "fake-app.cjs");
      resourcesDir = path.join(root, "Fake.app", "Contents", "Resources");
    } else {
      fakeApp = path.join(root, "fake-app.cjs");
      resourcesDir = path.join(root, "resources");
    }
    await mkdir(path.dirname(fakeApp), { recursive: true });
    await writeFile(fakeApp, fakeSmokeResult);
    // 只有空的 resources 目录：旧脚本会跳过资源/hello 并默认成功。
    await mkdir(resourcesDir, { recursive: true });
    const result = runScript(["--app", fakeApp]);
    assert.equal(result.status, 1);
    assert.match(`${result.stdout}\n${result.stderr}`, /包内缺少 Core 二进制/);
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /SMOKE PASS/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("--app without a path is a usage failure", () => {
  const result = runScript(["--app"]);
  assert.equal(result.status, 1);
  assert.match(`${result.stdout}\n${result.stderr}`, /--app 缺少可执行文件路径/);
});

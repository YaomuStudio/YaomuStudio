import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  ONBOARDING_SCHEMA_VERSION,
  readOnboardingFile,
  writeOnboardingCompleted,
} = require("../dist/onboarding.js");

async function tempDir(t, label) {
  const dir = await mkdtemp(path.join(os.tmpdir(), `yaomu-${label}-`));
  t.after(async () => {
    await rm(dir, { recursive: true, force: true });
  });
  return dir;
}

function onboardingFile(dir) {
  return path.join(dir, "onboarding.json");
}

async function tempFiles(dir) {
  return (await readdir(dir)).filter((name) => name.includes(".tmp"));
}

test("missing, corrupt or unknown files read as unset", async (t) => {
  const dir = await tempDir(t, "onboarding-unset");
  const file = onboardingFile(dir);

  assert.deepEqual(readOnboardingFile(file), { completed: null, status: "missing" });

  for (const [raw, status] of [
    ["not json", "corrupt"],
    ["{", "corrupt"],
    ["[]", "corrupt"],
    ["null", "corrupt"],
    ['"true"', "corrupt"],
    ['{"schema_version":1,"completed":"true"}', "corrupt"],
    ['{"schema_version":1}', "corrupt"],
    ['{"schema_version":2,"completed":true}', "unknown"],
    ['{"completed":true}', "unknown"],
  ]) {
    await writeFile(file, raw);
    assert.deepEqual(readOnboardingFile(file), { completed: null, status }, `payload: ${raw}`);
  }
});

test("completed must be an explicit boolean and round-trips", async (t) => {
  const dir = await tempDir(t, "onboarding-roundtrip");
  const file = onboardingFile(dir);
  assert.equal(ONBOARDING_SCHEMA_VERSION, 1);

  writeOnboardingCompleted(file, false);
  assert.deepEqual(readOnboardingFile(file), { completed: false, status: "ok" });

  writeOnboardingCompleted(file);
  assert.deepEqual(readOnboardingFile(file), { completed: true, status: "ok" });
});

test("writes atomically with the expected payload and no temp leftovers", async (t) => {
  const dir = await tempDir(t, "onboarding-atomic");
  const file = onboardingFile(dir);

  writeOnboardingCompleted(file, true);
  const payload = JSON.parse(await readFile(file, "utf8"));
  assert.deepEqual(payload, { schema_version: 1, completed: true });
  assert.deepEqual(await tempFiles(dir), []);

  const info = await stat(file);
  if (process.platform !== "win32") {
    assert.equal(info.mode & 0o777, 0o600);
  }
});

test("rejects non-boolean values without writing", async (t) => {
  const dir = await tempDir(t, "onboarding-invalid");
  const file = onboardingFile(dir);

  // 省略参数按“已完成”处理（仅 Main 内部使用）；显式非法值必须拒绝。
  for (const invalid of ["true", 1, null, {}]) {
    assert.throws(
      () => writeOnboardingCompleted(file, invalid),
      /ONBOARDING_COMPLETED_INVALID/,
      String(invalid),
    );
  }
  assert.deepEqual(readOnboardingFile(file), { completed: null, status: "missing" });
});

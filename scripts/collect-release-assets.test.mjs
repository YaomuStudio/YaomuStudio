import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, existsSync, readdirSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const script = new URL("./collect-release-assets.mjs", import.meta.url).pathname;

function run(args) {
  return spawnSync(process.execPath, [script, ...args], { encoding: "utf8" });
}

test("collects only expected Windows assets", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "yaomu-collect-"));
  const dist = path.join(root, "dist");
  const out = path.join(root, "out");
  mkdirSync(dist, { recursive: true });
  for (const name of [
    "YaoMuStudio-0.1.0-win-x64.exe",
    "YaoMuStudio-0.1.0-win-x64.exe.blockmap",
    "latest.yml",
    "builder-debug.yml",
  ]) {
    writeFileSync(path.join(dist, name), name);
  }
  try {
    const result = run(["--platform", "win", "--version", "0.1.0", "--dist", dist, "--out", out]);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(readdirSync(out).sort(), [
      "YaoMuStudio-0.1.0-win-x64.exe",
      "YaoMuStudio-0.1.0-win-x64.exe.blockmap",
      "latest.yml",
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("fails when a required asset is missing", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "yaomu-collect-missing-"));
  const dist = path.join(root, "dist");
  mkdirSync(dist, { recursive: true });
  writeFileSync(path.join(dist, "latest.yml"), "version: 0.1.0\n");
  try {
    const result = run(["--platform", "win", "--version", "0.1.0", "--dist", dist, "--out", path.join(root, "out")]);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /missing expected asset/);
    assert.equal(existsSync(path.join(root, "out", "latest.yml")), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const script = path.join(repoRoot, "scripts", "check-app-update-yml.mjs");
const builder = yaml.load(fs.readFileSync(path.join(repoRoot, "desktop", "electron-builder.yml"), "utf8"));
const publish = Array.isArray(builder.publish) ? builder.publish[0] : builder.publish;
const expectedUrl = publish?.url;

function runWith(contents) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "yaomu-app-update-"));
  try {
    fs.writeFileSync(path.join(dir, "app-update.yml"), contents);
    return spawnSync(process.execPath, [script, dir], { encoding: "utf8" });
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

test("accepts app-update.yml matching the generic builder feed", () => {
  assert.equal(typeof expectedUrl, "string");
  const result = runWith(`provider: generic\nurl: ${expectedUrl}\n`);
  assert.equal(result.status, 0, result.stderr);
});

test("rejects a different feed url", () => {
  const result = runWith("provider: generic\nurl: https://other.example.com/\n");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /url mismatch/);
});

test("rejects a github provider", () => {
  const result = runWith("provider: github\nowner: YaomuStudio\nrepo: YaomuStudio\n");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /generic update provider/);
});

import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";

const here = path.dirname(fileURLToPath(import.meta.url));
const configPath = path.join(here, "..", "electron-builder.yml");
const config = yaml.load(fs.readFileSync(configPath, "utf8"));

test("electron-builder publishes to the public YaomuStudio repository", () => {
  assert.equal(config.publish?.provider, "github");
  assert.equal(config.publish?.owner, "YaomuStudio");
  assert.equal(config.publish?.repo, "YaomuStudio");
  assert.equal(config.publish?.releaseType, "release");
});

test("publish block contains no embedded credentials", () => {
  const forbidden = /token|password|private.?key|secret|credential/i;
  for (const key of Object.keys(config.publish ?? {})) {
    assert.doesNotMatch(key, forbidden, `unexpected publish key: ${key}`);
  }
});

test("release asset names stay aligned across platforms", () => {
  assert.equal(config.win?.artifactName, "YaoMuStudio-${version}-win-${arch}.${ext}");
  assert.equal(config.mac?.artifactName, "YaoMuStudio-${version}-mac-${arch}.${ext}");
  assert.equal(config.linux?.artifactName, "YaoMuStudio-${version}-linux-${arch}.${ext}");
  assert.equal(config.linux?.executableName, "yaomustudio");
});

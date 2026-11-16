import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";
import {
  DOMESTIC_UPDATE_FEED_URL,
  selectUpdateFeeds,
} from "../dist/update-feed.js";

const here = path.dirname(fileURLToPath(import.meta.url));
const configPath = path.join(here, "..", "electron-builder.yml");
const config = yaml.load(fs.readFileSync(configPath, "utf8"));

test("electron-builder publishes to the domestic object-storage feed", () => {
  assert.equal(config.publish?.provider, "generic");
  assert.equal(config.publish?.url, DOMESTIC_UPDATE_FEED_URL);
});

test("Simplified Chinese locale prefers the domestic feed with GitHub fallback", () => {
  const { primary, fallback } = selectUpdateFeeds("zh-CN", "Asia/Shanghai");
  assert.equal(primary.id, "domestic");
  assert.equal(primary.provider, "generic");
  assert.equal(primary.url, DOMESTIC_UPDATE_FEED_URL);
  assert.equal(fallback.id, "international");
  assert.equal(fallback.provider, "github");
  assert.equal(fallback.owner, "YaomuStudio");
  assert.equal(fallback.repo, "YaomuStudio");
});

test("international locale prefers GitHub with domestic fallback", () => {
  const { primary, fallback } = selectUpdateFeeds("en-US", "America/New_York");
  assert.equal(primary.id, "international");
  assert.equal(primary.provider, "github");
  assert.equal(fallback.id, "domestic");
  assert.equal(fallback.provider, "generic");
});

test("China timezone selects the domestic feed even with an English locale", () => {
  const { primary } = selectUpdateFeeds("en-US", "Asia/Shanghai");
  assert.equal(primary.id, "domestic");
});

test("Traditional Chinese outside the mainland selects the international feed", () => {
  const { primary } = selectUpdateFeeds("zh-TW", "Asia/Taipei");
  assert.equal(primary.id, "international");
});

test("main wires locale feed selection and one-shot fallback", () => {
  const main = fs.readFileSync(new URL("../dist/main.js", import.meta.url), "utf8");
  assert.match(main, /selectUpdateFeeds/);
  assert.match(main, /checkForUpdatesWithFeedFallback/);
  assert.match(main, /updateFeedFallbackArmed/);
  assert.match(main, /setFeedURL/);
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
  // electron-builder resolves ${arch} to x86_64 on Linux; the release contract uses linux-x64.
  assert.equal(config.linux?.artifactName, "YaoMuStudio-${version}-linux-x64.${ext}");
  assert.equal(config.linux?.executableName, "yaomustudio");
});

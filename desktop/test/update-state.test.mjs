import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const { selectUpdateReplay } = require("../dist/update-state.js");

test("replays not-available when no version is known", () => {
  assert.deepEqual(selectUpdateReplay(null, null), {
    channel: "update:not-available",
    payload: {},
  });
});

test("replays a discovered version as available", () => {
  assert.deepEqual(selectUpdateReplay(null, "1.2.3"), {
    channel: "update:available",
    payload: { version: "1.2.3" },
  });
});

test("replays a downloaded version when it is the only state", () => {
  assert.deepEqual(selectUpdateReplay("1.2.3", null), {
    channel: "update:downloaded",
    payload: { version: "1.2.3" },
  });
});

test("same-version download is not downgraded to available", () => {
  assert.deepEqual(selectUpdateReplay("1.2.3", "1.2.3"), {
    channel: "update:downloaded",
    payload: { version: "1.2.3" },
  });
});

test("a newer available version wins over an older downloaded version", () => {
  assert.deepEqual(selectUpdateReplay("1.2.3", "1.3.0"), {
    channel: "update:available",
    payload: { version: "1.3.0" },
  });
});

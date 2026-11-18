import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { cleanYaomuWslRuntime } from "../dist/runtime-maintenance.js";

test("unregisters only the app-owned WSL distro and removes the app WSL directory", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "yaomu-runtime-"));
  const calls = [];
  await fs.mkdir(path.join(dir, "wsl", "distros", "yaomu"), { recursive: true });
  const result = await cleanYaomuWslRuntime({
    userDataDir: dir,
    exec: async (file, args) => {
      calls.push([file, ...args]);
      if (args[0] === "-l") return { stdout: "Ubuntu\nyaomu\n" };
      return { stdout: "" };
    },
  });
  assert.deepEqual(result, { removed: true });
  assert.deepEqual(calls[1], ["wsl.exe", "--unregister", "yaomu"]);
  await assert.rejects(fs.stat(path.join(dir, "wsl")));
});

test("does not unregister when the app distro is absent", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "yaomu-runtime-"));
  const calls = [];
  const result = await cleanYaomuWslRuntime({
    userDataDir: dir,
    exec: async (file, args) => {
      calls.push([file, ...args]);
      return { stdout: "Ubuntu\n" };
    },
  });
  assert.deepEqual(result, { removed: false, reason: "not_installed" });
  assert.equal(calls.length, 1);
});

test("refuses a distro override", async () => {
  await assert.rejects(
    cleanYaomuWslRuntime({ userDataDir: os.tmpdir(), distro: "Ubuntu" }),
    /non-app WSL distro/,
  );
});

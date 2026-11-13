import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const desktopRoot = path.resolve(import.meta.dirname, "..");

// wsl.js 在模块作用域导入 electron.app；单测用最小 stub 避免拉起 Electron。
const electronPath = require.resolve("electron");
require.cache[electronPath] = {
  id: electronPath,
  filename: electronPath,
  loaded: true,
  exports: {
    app: { isPackaged: false, getAppPath: () => desktopRoot },
  },
};

const { translationEnvForWsl, resolveWslRunnerEnv } = require("../dist/wsl.js");

test("bridges only whitelisted provider vars and merges WSLENV", () => {
  const env = translationEnvForWsl({
    GEMINI_MODEL_API_KEY: "secret-gemini",
    DEEPSEEK_API_KEY: "secret-deepseek",
    UNRELATED_SECRET: "must-not-pass",
    WSLENV: "EXISTING/u",
  });

  assert.equal(env.GEMINI_MODEL_API_KEY, "secret-gemini");
  assert.equal(env.DEEPSEEK_API_KEY, "secret-deepseek");
  assert.equal(env.UNRELATED_SECRET, undefined);
  assert.deepEqual(
    String(env.WSLENV).split(":").sort(),
    ["DEEPSEEK_API_KEY/u", "EXISTING/u", "GEMINI_MODEL_API_KEY/u"],
  );
});

test("returns no bridge when no provider variable is set", () => {
  assert.deepEqual(translationEnvForWsl({ UNRELATED: "x" }), {});
});

test(
  "injects mlstools path and provider env without leaking values into argv",
  { skip: process.platform !== "win32" },
  (t) => {
    const previous = {};
    const keys = [
      "GEMINI_MODEL_API_KEY",
      "YAOMU_WSL_DISTRO",
      "YAOMU_WSL_EXE",
      "YAOMU_WSL_RUNNER_COMMAND",
      "YAOMU_MOCK_MODEL_SLOTS",
      "WSLENV",
    ];
    for (const key of keys) previous[key] = process.env[key];
    t.after(() => {
      for (const key of keys) {
        if (previous[key] === undefined) delete process.env[key];
        else process.env[key] = previous[key];
      }
    });

    process.env.GEMINI_MODEL_API_KEY = "super-secret-provider-key";
    process.env.YAOMU_WSL_DISTRO = "yaomu-test";
    delete process.env.YAOMU_WSL_RUNNER_COMMAND;
    delete process.env.YAOMU_MOCK_MODEL_SLOTS;
    delete process.env.WSLENV;

    const env = resolveWslRunnerEnv({
      runtimeReady: true,
      tools: {
        flashcli: "/usr/local/bin/flashcli",
        mlstools: "/usr/local/bin/mlstools",
        ffmpeg: "/usr/bin/ffmpeg",
        ffprobe: "/usr/bin/ffprobe",
        python3: "/usr/bin/python3",
      },
    });

    assert.equal(env.YAOMU_WSL_DISTRO, "yaomu-test");
    assert.equal(env.GEMINI_MODEL_API_KEY, "super-secret-provider-key");
    assert.match(String(env.WSLENV), /GEMINI_MODEL_API_KEY\/u/);

    const argv = JSON.parse(String(env.YAOMU_WSL_RUNNER_ARGS));
    assert.ok(argv.includes("MLSTOOLS_BIN=/usr/local/bin/mlstools"));
    assert.ok(argv.includes("FLASHCLI_BIN=/usr/local/bin/flashcli"));
    assert.ok(!String(env.YAOMU_WSL_RUNNER_ARGS).includes("super-secret-provider-key"));
    assert.match(String(env.YAOMU_MOCK_MODEL_SLOTS), /(^|,)translate(,|$)/);
  },
);

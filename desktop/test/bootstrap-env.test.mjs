import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const { buildCoreSpawnEnv, startCore, terminateProcessTree } = require("../dist/bootstrap.js");

const dataDir = "/tmp/yaomu-core-data";
const port = 51234;
const token = "test-instance-token";
const fakeCore = fileURLToPath(new URL("./fixtures/fake-core.cjs", import.meta.url));

function build({ packaged, parentEnv, overrideEnv }) {
  return buildCoreSpawnEnv({
    parentEnv,
    overrideEnv,
    packaged,
    dataDir,
    port,
    token,
  });
}

test("packaged spawn env strips inherited and overridden mock slots", () => {
  const parentEnv = {
    PATH: "/usr/bin",
    YAOMU_MOCK_MODEL_SLOTS: "separation",
    YAOMU_WSL_RUNNER_COMMAND: "wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: "[]",
    FLASHCLI_CHECKPOINT: "/models/checkpoint",
  };
  const overrideEnv = {
    YAOMU_MOCK_MODEL_SLOTS: "separation,voice-clone",
    YAOMU_RELEASE_BASE_URLS: "https://flashhub-cdn.aodianyun.com/",
  };
  const parentSnapshot = { ...parentEnv };
  const overrideSnapshot = { ...overrideEnv };

  const env = build({ packaged: true, parentEnv, overrideEnv });

  assert.equal(env.YAOMU_MOCK_MODEL_SLOTS, undefined);
  // 只清理演示键，用户真实 WSL / FlashCLI / 发布配置必须保留。
  assert.equal(env.YAOMU_WSL_RUNNER_COMMAND, "wsl.exe");
  assert.equal(env.YAOMU_WSL_RUNNER_ARGS, "[]");
  assert.equal(env.FLASHCLI_CHECKPOINT, "/models/checkpoint");
  assert.equal(env.YAOMU_RELEASE_BASE_URLS, "https://flashhub-cdn.aodianyun.com/");
  assert.equal(env.PATH, "/usr/bin");
  // Bootstrap 私有键仍由本模块覆盖。
  assert.equal(env.YAOMU_DATA_DIR, dataDir);
  assert.equal(env.YAOMU_CORE_PORT, String(port));
  assert.equal(env.YAOMU_INSTANCE_TOKEN, token);
  // 不修改调用方对象，也不依赖删除后的全局 env。
  assert.deepEqual(parentEnv, parentSnapshot);
  assert.deepEqual(overrideEnv, overrideSnapshot);
});

test("development keeps an explicit mock slot selection and never invents one", () => {
  const explicit = build({
    packaged: false,
    parentEnv: { YAOMU_MOCK_MODEL_SLOTS: "separation" },
    overrideEnv: {},
  });
  assert.equal(explicit.YAOMU_MOCK_MODEL_SLOTS, "separation");

  const noDefault = build({ packaged: false, parentEnv: {}, overrideEnv: {} });
  assert.equal(noDefault.YAOMU_MOCK_MODEL_SLOTS, undefined);

  const overrideWins = build({
    packaged: false,
    parentEnv: { YAOMU_MOCK_MODEL_SLOTS: "separation" },
    overrideEnv: { YAOMU_MOCK_MODEL_SLOTS: "voice-clone" },
  });
  assert.equal(overrideWins.YAOMU_MOCK_MODEL_SLOTS, "voice-clone");
});

test("packaged spawn env strips mock slot keys regardless of case variants", () => {
  // Windows 环境变量名大小写不敏感，Node spawn 按字典序保留首个变体；
  // 只删精确键会让小写/混合大小写变体在 Windows 上继续生效。
  const env = build({
    packaged: true,
    parentEnv: { yaomu_mock_model_slots: "separation" },
    overrideEnv: { Yaomu_Mock_Model_Slots: "voice-clone" },
  });
  const variants = Object.keys(env).filter(
    (key) => key.toUpperCase() === "YAOMU_MOCK_MODEL_SLOTS",
  );
  assert.deepEqual(variants, []);
});

/**
 * 受控子进程集成：让 startCore 真正 spawn 假 Core，捕获它实际收到的环境。
 * 不只测试净化函数本身，避免“净化函数对、装配点没接上”的静默漂移。
 */
async function runFakeCore({ packaged, parentEnv, overrideEnv }) {
  const tmp = await mkdtemp(path.join(os.tmpdir(), "yaomu-core-spawn-"));
  const capture = path.join(tmp, "spawn-env.json");
  const coreDataDir = path.join(tmp, "core-data");
  const restore = [];
  for (const [key, value] of Object.entries(parentEnv ?? {})) {
    restore.push([key, process.env[key]]);
    process.env[key] = value;
  }
  let info;
  try {
    info = await startCore({
      dataDir: coreDataDir,
      packaged,
      token,
      launch: { command: process.execPath, args: [fakeCore], cwd: tmp },
      env: { YAOMU_TEST_CAPTURE_ENV: capture, ...overrideEnv },
      healthTimeoutMs: 15_000,
      healthIntervalMs: 50,
      log: () => undefined,
    });
    const parentMockAfter = process.env.YAOMU_MOCK_MODEL_SLOTS ?? null;
    const captured = JSON.parse(await readFile(capture, "utf8"));
    return { captured, parentMockAfter, expectedPort: String(info.port), coreDataDir };
  } finally {
    for (const [key, value] of restore) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    if (info) await terminateProcessTree(info.child, 2_000).catch(() => undefined);
    await rm(tmp, { recursive: true, force: true });
  }
}

test("packaged startCore strips mock slots from the environment the core process actually receives", async () => {
  const result = await runFakeCore({
    packaged: true,
    parentEnv: { YAOMU_MOCK_MODEL_SLOTS: "separation", YAOMU_TEST_PARENT_ONLY: "1" },
    overrideEnv: {
      YAOMU_MOCK_MODEL_SLOTS: "voice-clone",
      YAOMU_WSL_RUNNER_COMMAND: "wsl.exe",
      YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu"]',
    },
  });

  // 最终 spawn env：父环境与覆盖里的演示槽位都被清理。
  assert.equal(result.captured.mockSlots, null);
  // 父环境其余变量与真实 WSL 配置保留；父进程 env 未被修改。
  assert.equal(result.captured.parentOnly, "1");
  assert.equal(result.captured.wslCommand, "wsl.exe");
  assert.equal(result.captured.wslArgs, '["-d","yaomu"]');
  assert.equal(result.parentMockAfter, "separation");
  // Bootstrap 私有键确实进入子进程。
  assert.equal(result.captured.dataDir, result.coreDataDir);
  assert.equal(result.captured.port, result.expectedPort);
  assert.equal(result.captured.token, token);
});

test("development startCore passes an explicitly set mock slot through to the core process", async () => {
  const result = await runFakeCore({
    packaged: false,
    parentEnv: { YAOMU_MOCK_MODEL_SLOTS: "separation" },
    overrideEnv: {},
  });

  assert.equal(result.captured.mockSlots, "separation");
  assert.equal(result.parentMockAfter, "separation");
});

test("packaged startCore strips case variants from the environment the core process actually receives", async () => {
  const result = await runFakeCore({
    packaged: true,
    parentEnv: { yaomu_Mock_Model_Slots: "separation" },
    overrideEnv: { Yaomu_Mock_Model_Slots: "voice-clone" },
  });

  assert.deepEqual(result.captured.mockSlotKeys, []);
});

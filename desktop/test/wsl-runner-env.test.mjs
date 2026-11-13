import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test, { after, before } from "node:test";

const require = createRequire(import.meta.url);
const { resolveWslRunnerEnv, windowsPathToWslMount } = require("../dist/wsl.js");

const tools = {
  flashcli: "/usr/local/bin/flashcli",
  mlstools: "/usr/local/bin/mlstools",
  ffmpeg: "/usr/bin/ffmpeg",
  ffprobe: "/usr/bin/ffprobe",
  python3: "/usr/bin/python3",
};

const bundledRunner = {
  sourceDir: "C:\\Program Files\\曜幕\\resources\\core\\runner_src",
  runnerVersion: "0.1.0",
  manifestDigest: "a".repeat(64),
};

let repoRoot;
before(async () => {
  repoRoot = await mkdtemp(path.join(os.tmpdir(), "yaomu-wsl-env-"));
  await mkdir(path.join(repoRoot, "runner", "src", "yaomustudio_runner"), { recursive: true });
});
after(async () => {
  await rm(repoRoot, { recursive: true, force: true });
});

test("development WSL auto runner no longer injects implicit mock slots", () => {
  const env = {};
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env,
    tools,
  });

  assert.deepEqual(
    Object.keys(result).filter((key) => key.toUpperCase() === "YAOMU_MOCK_MODEL_SLOTS"),
    [],
  );
  assert.equal(result.YAOMU_MOCK_MODEL_SLOTS, undefined);
  assert.ok(result.YAOMU_WSL_RUNNER_COMMAND);
  const args = JSON.parse(result.YAOMU_WSL_RUNNER_ARGS);
  assert.ok(args.includes("FLASHCLI_BIN=/usr/local/bin/flashcli"));
  assert.ok(args.includes("YAOMU_CAPABILITY_EXECUTOR=flashcli"));
  assert.ok(!args.includes("YAOMU_MOCK_MODEL_SLOTS=separation,voice-clone"));
  assert.deepEqual(env, {});
});

test("explicit mock slots are preserved only when the developer sets them", () => {
  const env = { YAOMU_MOCK_MODEL_SLOTS: "separation" };
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env,
    tools,
  });

  assert.equal(result.YAOMU_MOCK_MODEL_SLOTS, "separation");
  assert.deepEqual(env, { YAOMU_MOCK_MODEL_SLOTS: "separation" });
});

test("explicit WSL runner commands are not rewritten or polluted with mock slots", () => {
  const env = {
    YAOMU_WSL_RUNNER_COMMAND: "C:\\Windows\\System32\\wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu","-e","python3","-m","yaomustudio_runner"]',
  };
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env,
    tools,
  });

  assert.equal(result.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(result.YAOMU_MOCK_MODEL_SLOTS, undefined);
  assert.deepEqual(env, {
    YAOMU_WSL_RUNNER_COMMAND: "C:\\Windows\\System32\\wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu","-e","python3","-m","yaomustudio_runner"]',
  });
});

test("packaged desktop never derives a development runner or mock slots", () => {
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    repoRoot,
    env: {},
    tools,
  });

  assert.equal(result.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(result.YAOMU_MOCK_MODEL_SLOTS, undefined);
  assert.equal(result.YAOMU_WSL_DISTRO, "yaomu");
});

test("missing tools stay unknown: resolveWslRunnerEnv never probes implicitly", () => {
  const env = {};
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env,
  });

  assert.ok(result.YAOMU_WSL_RUNNER_COMMAND);
  const args = JSON.parse(result.YAOMU_WSL_RUNNER_ARGS);
  assert.ok(args.some((arg) => arg.startsWith("PYTHONPATH=")));
  assert.ok(!args.some((arg) => arg.startsWith("FLASHCLI_BIN=")));
  assert.ok(!args.some((arg) => arg.startsWith("FFMPEG_BIN=")));
  assert.ok(!args.some((arg) => arg.startsWith("FFPROBE_BIN=")));
  assert.ok(!args.includes("YAOMU_CAPABILITY_EXECUTOR=flashcli"));
  assert.deepEqual(env, {});
});

test("packaged bundled source produces -B -P -s module argv with only the verified PYTHONPATH", () => {
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    repoRoot: "C:\\development-checkout",
    env: {},
    tools,
    bundledRunner,
  });

  assert.ok(result.YAOMU_WSL_RUNNER_COMMAND);
  const args = JSON.parse(result.YAOMU_WSL_RUNNER_ARGS);
  const pythonPaths = args.filter((arg) => arg.startsWith("PYTHONPATH="));
  assert.deepEqual(pythonPaths, [
    "PYTHONPATH=/mnt/c/Program Files/曜幕/resources/core/runner_src",
  ]);
  const moduleIndex = args.indexOf("-m");
  for (const flag of ["-B", "-P", "-s"]) {
    const flagIndex = args.indexOf(flag);
    assert.ok(flagIndex >= 0 && flagIndex < moduleIndex, flag);
  }
  assert.deepEqual(args.slice(-2), ["-m", "yaomustudio_runner"]);
  assert.ok(args.includes("FLASHCLI_BIN=/usr/local/bin/flashcli"));
  assert.ok(args.includes("FFMPEG_BIN=/usr/bin/ffmpeg"));
  assert.ok(!args.some((arg) => arg.includes("development-checkout")));
  assert.equal(result.YAOMU_MOCK_MODEL_SLOTS, undefined);
});

test("packaged launch without a verified bundled descriptor keeps the Runner unavailable", () => {
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    repoRoot: "C:\\development-checkout",
    env: {},
    tools,
    bundledRunner: null,
  });
  assert.equal(result.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(result.YAOMU_WSL_RUNNER_ARGS, undefined);
  assert.equal(result.YAOMU_WSL_DISTRO, "yaomu");
});

test("explicit external WSL override wins over bundled source and stays external", () => {
  const env = {
    YAOMU_WSL_RUNNER_COMMAND: "C:\\Windows\\System32\\wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu","-e","python3","-m","yaomustudio_runner"]',
  };
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env,
    tools,
    bundledRunner,
  });
  // 外部命令由 Core 直接读取宿主环境；投影不再生成第二条包内命令。
  assert.equal(result.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(result.YAOMU_WSL_RUNNER_ARGS, undefined);
  assert.equal(result.YAOMU_WSL_DISTRO, "yaomu");
  assert.deepEqual(env, {
    YAOMU_WSL_RUNNER_COMMAND: "C:\\Windows\\System32\\wsl.exe",
    YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu","-e","python3","-m","yaomustudio_runner"]',
  });
});

test("explicit override detection is case-insensitive and ignores blank values", () => {
  const lowerCase = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env: { yaomu_wsl_runner_command: "C:\\wsl.exe" },
    bundledRunner,
  });
  assert.equal(lowerCase.YAOMU_WSL_RUNNER_COMMAND, undefined);

  const blank = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env: { YAOMU_WSL_RUNNER_COMMAND: "   " },
    bundledRunner,
  });
  assert.ok(blank.YAOMU_WSL_RUNNER_COMMAND);
  const args = JSON.parse(blank.YAOMU_WSL_RUNNER_ARGS);
  assert.ok(args.includes("PYTHONPATH=/mnt/c/Program Files/曜幕/resources/core/runner_src"));
});

test("non-Windows projection stays empty even with a bundled descriptor", () => {
  const result = resolveWslRunnerEnv({
    platform: "darwin",
    packaged: true,
    env: {},
    bundledRunner,
  });
  assert.deepEqual(result, {});
});

test("windowsPathToWslMount preserves drive identity, spaces and non-ASCII", () => {
  assert.equal(
    windowsPathToWslMount("C:\\Program Files\\曜幕\\resources\\core\\runner_src"),
    "/mnt/c/Program Files/曜幕/resources/core/runner_src",
  );
  assert.equal(windowsPathToWslMount("D:/data/runner/src"), "/mnt/d/data/runner/src");
});

test("WSL media probe env injects the probed image ffprobe for Core", () => {
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env: {},
    tools,
  });

  assert.equal(result.YAOMU_WSL_FFPROBE_BIN, "/usr/bin/ffprobe");
  assert.equal(result.YAOMU_WSL_MLSTOOLS_BIN, "/usr/local/bin/mlstools");
  assert.equal(result.YAOMU_WSL_DISTRO, "yaomu");
  // 镜像内工具同时仍按既有约定传给 Runner 进程；Core 只读专用变量。
  const args = JSON.parse(result.YAOMU_WSL_RUNNER_ARGS);
  assert.ok(args.includes("FFPROBE_BIN=/usr/bin/ffprobe"));
  assert.ok(args.includes("MLSTOOLS_BIN=/usr/local/bin/mlstools"));
  // 路径只出现在既定投影里，不渗入 distro/exe 或诊断字段。
  const carrying = Object.keys(result)
    .filter((key) => String(result[key]).includes("/usr/bin/ffprobe"))
    .sort();
  assert.deepEqual(carrying, ["YAOMU_WSL_FFPROBE_BIN", "YAOMU_WSL_RUNNER_ARGS"]);
  const carryingMl = Object.keys(result)
    .filter((key) => String(result[key]).includes("/usr/local/bin/mlstools"))
    .sort();
  assert.deepEqual(carryingMl, ["YAOMU_WSL_MLSTOOLS_BIN", "YAOMU_WSL_RUNNER_ARGS"]);
});

test("media probe env survives packaged-missing-runner and external-runner early returns", () => {
  const packagedMissing = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env: {},
    tools,
    bundledRunner: null,
  });
  assert.equal(packagedMissing.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(packagedMissing.YAOMU_WSL_FFPROBE_BIN, "/usr/bin/ffprobe");

  const external = resolveWslRunnerEnv({
    platform: "win32",
    packaged: true,
    env: {
      YAOMU_WSL_RUNNER_COMMAND: "C:\\Windows\\System32\\wsl.exe",
      YAOMU_WSL_RUNNER_ARGS: '["-d","yaomu","-e","python3","-m","yaomustudio_runner"]',
    },
    tools,
    bundledRunner,
  });
  assert.equal(external.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(external.YAOMU_WSL_FFPROBE_BIN, "/usr/bin/ffprobe");
});

test("media probe env survives a development checkout without runner source", () => {
  const result = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot: path.join(repoRoot, "missing-checkout"),
    env: {},
    tools,
  });
  assert.equal(result.YAOMU_WSL_RUNNER_COMMAND, undefined);
  assert.equal(result.YAOMU_WSL_FFPROBE_BIN, "/usr/bin/ffprobe");
});

test("missing or unsafe ffprobe paths are never injected", () => {
  const unsafe = {
    missing: null,
    relative: "usr/bin/ffprobe",
    bare: "ffprobe",
    windows: "C:\\ffmpeg\\bin\\ffprobe.exe",
    whitespace: "/usr/bin/ff probe",
    newline: "/usr/bin/ffprobe\n--bad",
    control: "/usr/bin/ffprobe\u0000",
    overlong: "/" + "a".repeat(5_000),
  };
  for (const [name, ffprobe] of Object.entries(unsafe)) {
    const result = resolveWslRunnerEnv({
      platform: "win32",
      packaged: false,
      repoRoot,
      env: {},
      tools: { ...tools, ffprobe },
    });
    assert.equal(result.YAOMU_WSL_FFPROBE_BIN, undefined, name);
  }
});

test("unsafe distro names suppress media probe injection", () => {
  for (const distro of ["-yaomu", "bad distro", "bad\ndistro", "bad/distro", "中文", "a".repeat(65)]) {
    const result = resolveWslRunnerEnv({
      platform: "win32",
      packaged: false,
      repoRoot,
      env: { YAOMU_WSL_DISTRO: distro },
      tools,
    });
    assert.equal(result.YAOMU_WSL_FFPROBE_BIN, undefined, JSON.stringify(distro));
  }

  // 空白覆盖沿用默认发行版，仍可注入。
  const blank = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env: { YAOMU_WSL_DISTRO: "   " },
    tools,
  });
  assert.equal(blank.YAOMU_WSL_DISTRO, "yaomu");
  assert.equal(blank.YAOMU_WSL_FFPROBE_BIN, "/usr/bin/ffprobe");
});

test("YAOMU_WSL_EXE is projected only when the parent environment sets it", () => {
  const withExe = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env: { YAOMU_WSL_EXE: " C:\\Windows\\System32\\wsl.exe " },
    tools,
  });
  assert.equal(withExe.YAOMU_WSL_EXE, "C:\\Windows\\System32\\wsl.exe");

  const withoutExe = resolveWslRunnerEnv({
    platform: "win32",
    packaged: false,
    repoRoot,
    env: {},
    tools,
  });
  assert.equal(withoutExe.YAOMU_WSL_EXE, undefined);
});

test("non-Windows projection never carries the media probe env", () => {
  const result = resolveWslRunnerEnv({
    platform: "darwin",
    packaged: false,
    env: { YAOMU_WSL_EXE: "wsl.exe" },
    tools,
  });
  assert.deepEqual(result, {});
});

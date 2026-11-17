import assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const { resolveHostRuntimeEnv } = require("../dist/host-runtime.js");

async function makeCoreDir() {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-host-runtime-"));
  const coreDir = path.join(root, "core");
  await mkdir(path.join(coreDir, "python", "bin"), { recursive: true });
  for (const name of ["yaomu-runner", "ffmpeg", "ffprobe"]) {
    const file = path.join(coreDir, name);
    await writeFile(file, "#!/bin/sh\n");
    await chmod(file, 0o755);
  }
  const python = path.join(coreDir, "python", "bin", "python3");
  await writeFile(python, "#!/bin/sh\n");
  await chmod(python, 0o755);
  return { root, coreDir };
}

async function makeToolDir() {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-host-tools-"));
  for (const name of ["flashcli", "mlstools"]) {
    const file = path.join(root, name);
    await writeFile(file, "#!/bin/sh\n");
    await chmod(file, 0o755);
  }
  return root;
}

test("non-packaged app does not inject host runtime", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "darwin",
      packaged: false,
      coreDir,
      runnerValidated: true,
      env: {},
    });
    assert.deepEqual(result.env, {});
    assert.equal(result.reason, "not-packaged");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("packaged Windows injects bundled ffmpeg and ffprobe without a host runner", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    await writeFile(path.join(coreDir, "ffmpeg.exe"), "ffmpeg");
    await writeFile(path.join(coreDir, "ffprobe.exe"), "ffprobe");
    const result = resolveHostRuntimeEnv({
      platform: "win32",
      packaged: true,
      coreDir,
      runnerValidated: false,
      env: {},
    });
    assert.equal(result.reason, "windows-media");
    assert.equal(result.env.FFMPEG_BIN, path.join(coreDir, "ffmpeg.exe"));
    assert.equal(result.env.FFPROBE_BIN, path.join(coreDir, "ffprobe.exe"));
    assert.equal(result.env.YAOMU_RUNNER_BIN, undefined);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("packaged Windows without bundled ffmpeg does not invent a runtime", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "win32",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: {},
    });
    assert.deepEqual(result.env, {});
    assert.equal(result.reason, "ffmpeg-missing");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("complete POSIX resources inject runner, ffmpeg and ffprobe", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "darwin",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: {},
    });
    assert.equal(result.reason, "ok");
    assert.equal(result.env.YAOMU_RUNNER_BIN, path.join(coreDir, "yaomu-runner"));
    assert.equal(result.env.FFMPEG_BIN, path.join(coreDir, "ffmpeg"));
    assert.equal(result.env.FFPROBE_BIN, path.join(coreDir, "ffprobe"));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("invalid runner validation blocks all host injection", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "linux",
      packaged: true,
      coreDir,
      runnerValidated: false,
      env: {},
    });
    assert.deepEqual(result.env, {});
    assert.equal(result.reason, "runner-invalid");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Python symlink escaping the core directory is rejected", async () => {
  const { root, coreDir } = await makeCoreDir();
  const outside = path.join(root, "outside-python");
  try {
    await writeFile(outside, "#!/bin/sh\n");
    await chmod(outside, 0o755);
    const python = path.join(coreDir, "python", "bin", "python3");
    await rm(python);
    await symlink(outside, python);
    const result = resolveHostRuntimeEnv({
      platform: "darwin",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: {},
    });
    assert.deepEqual(result.env, {});
    assert.equal(result.reason, "python-missing");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("explicit process env values are not overwritten", async () => {
  const { root, coreDir } = await makeCoreDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "darwin",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: { FFMPEG_BIN: "/custom/ffmpeg" },
    });
    assert.equal(result.reason, "ok");
    assert.equal(result.env.FFMPEG_BIN, undefined);
    assert.equal(result.env.FFPROBE_BIN, path.join(coreDir, "ffprobe"));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Linux injects flashcli and mlstools found on PATH", async () => {
  const { root, coreDir } = await makeCoreDir();
  const toolDir = await makeToolDir();
  try {
    const result = resolveHostRuntimeEnv({
      platform: "linux",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: { PATH: toolDir },
    });
    assert.equal(result.reason, "ok");
    assert.equal(result.env.FLASHCLI_BIN, path.join(toolDir, "flashcli"));
    assert.equal(result.env.YAOMU_CAPABILITY_EXECUTOR, "flashcli");
    assert.equal(result.env.MLSTOOLS_BIN, path.join(toolDir, "mlstools"));
  } finally {
    await rm(root, { recursive: true, force: true });
    await rm(toolDir, { recursive: true, force: true });
  }
});

test("Linux explicit tools win over PATH, and macOS never probes FlashCLI", async () => {
  const { root, coreDir } = await makeCoreDir();
  const toolDir = await makeToolDir();
  try {
    const explicitFlash = path.join(toolDir, "flashcli");
    const linux = resolveHostRuntimeEnv({
      platform: "linux",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: { PATH: "", FLASHCLI_BIN: explicitFlash },
    });
    assert.equal(linux.reason, "ok");
    assert.equal(linux.env.FLASHCLI_BIN, undefined);
    assert.equal(linux.env.YAOMU_CAPABILITY_EXECUTOR, "flashcli");

    const mac = resolveHostRuntimeEnv({
      platform: "darwin",
      packaged: true,
      coreDir,
      runnerValidated: true,
      env: { PATH: toolDir },
    });
    assert.equal(mac.reason, "ok");
    assert.equal(mac.env.FLASHCLI_BIN, undefined);
    assert.equal(mac.env.MLSTOOLS_BIN, undefined);
  } finally {
    await rm(root, { recursive: true, force: true });
    await rm(toolDir, { recursive: true, force: true });
  }
});

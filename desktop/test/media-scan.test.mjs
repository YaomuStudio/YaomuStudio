import assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const { MAX_MEDIA_SCAN_FILES, scanMediaDirectory } = require("../dist/media-scan.js");

async function tempDir(t, label) {
  const dir = await mkdtemp(path.join(os.tmpdir(), `yaomu-${label}-`));
  t.after(async () => {
    await rm(dir, { recursive: true, force: true });
  });
  return dir;
}

function sorted(paths) {
  return [...paths].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

test("recursively scans nested folders and filters extensions case-insensitively", async (t) => {
  const root = await tempDir(t, "scan-nested");
  await mkdir(path.join(root, "sub", "deep"), { recursive: true });
  await mkdir(path.join(root, ".hidden-dir"), { recursive: true });
  await writeFile(path.join(root, "a.mp4"), "x");
  await writeFile(path.join(root, "b.MP3"), "x");
  await writeFile(path.join(root, "notes.txt"), "x");
  await writeFile(path.join(root, ".hidden.mp4"), "x");
  await writeFile(path.join(root, ".hidden-dir", "inside.mp4"), "x");
  await writeFile(path.join(root, "sub", "c.MKV"), "x");
  await writeFile(path.join(root, "sub", "deep", "e.Wav"), "x");

  const result = await scanMediaDirectory(root);

  const expected = sorted([
    path.join(root, "a.mp4"),
    path.join(root, "b.MP3"),
    path.join(root, "sub", "c.MKV"),
    path.join(root, "sub", "deep", "e.Wav"),
  ]);
  assert.equal(result.directory, path.resolve(root));
  assert.deepEqual(result.paths, expected);
  assert.equal(result.total, 4);
  assert.equal(result.truncated, false);
  assert.equal(result.errors, 0);
});

test("honors a custom extension list (dots and case accepted)", async (t) => {
  const root = await tempDir(t, "scan-extensions");
  await writeFile(path.join(root, "script.TXT"), "x");
  await writeFile(path.join(root, "clip.mp4"), "x");

  const result = await scanMediaDirectory(root, { extensions: [".TXT", "srt"] });

  assert.deepEqual(result.paths, [path.join(root, "script.TXT")]);
  assert.equal(result.total, 1);
});

test("skips symbolic links to files and directories", async (t) => {
  const root = await tempDir(t, "scan-symlink");
  const outside = await tempDir(t, "scan-symlink-target");
  const real = path.join(root, "real.mp4");
  await writeFile(real, "x");
  await writeFile(path.join(outside, "outside.mp4"), "x");

  try {
    await symlink(real, path.join(root, "link-file.mp4"));
    await symlink(outside, path.join(root, "link-dir"));
  } catch (error) {
    if (error && error.code === "EPERM") {
      t.skip("symlink creation is not permitted on this platform");
      return;
    }
    throw error;
  }

  const result = await scanMediaDirectory(root);
  assert.deepEqual(result.paths, [real]);
  assert.equal(result.total, 1);
  assert.equal(result.errors, 0);
});

test("sorts matches and truncates to the 128 file cap", async (t) => {
  const root = await tempDir(t, "scan-truncate");
  const names = Array.from({ length: 130 }, (_, index) =>
    `clip-${String(index).padStart(3, "0")}.mp4`,
  );
  for (const name of names) {
    await writeFile(path.join(root, name), "x");
  }

  const result = await scanMediaDirectory(root);
  const all = sorted(names.map((name) => path.join(root, name)));

  assert.equal(result.total, 130);
  assert.equal(result.paths.length, MAX_MEDIA_SCAN_FILES);
  assert.equal(result.truncated, true);
  assert.deepEqual(result.paths, all.slice(0, MAX_MEDIA_SCAN_FILES));

  // 显式传入更大的 limit 也不能超过上限，避免超出 Core 的导入授权上限。
  const capped = await scanMediaDirectory(root, { limit: 10_000 });
  assert.equal(capped.paths.length, MAX_MEDIA_SCAN_FILES);
});

test("returns an empty result for an empty directory", async (t) => {
  const root = await tempDir(t, "scan-empty");
  const result = await scanMediaDirectory(root);
  assert.deepEqual(result.paths, []);
  assert.equal(result.total, 0);
  assert.equal(result.truncated, false);
  assert.equal(result.errors, 0);
});

test("treats a missing root as a scan error instead of throwing", async (t) => {
  const root = await tempDir(t, "scan-missing");
  const missing = await scanMediaDirectory(path.join(root, "does-not-exist"));
  assert.deepEqual(missing.paths, []);
  assert.equal(missing.total, 0);
  assert.equal(missing.errors, 1);
});

test("counts unreadable subdirectories without aborting the scan", async (t) => {
  if (process.platform === "win32") {
    t.skip("permission bits are not a reliable unreadable-directory fixture on Windows");
    return;
  }
  if (typeof process.getuid === "function" && process.getuid() === 0) {
    t.skip("running as root: permission bits do not block reads");
    return;
  }

  const root = await tempDir(t, "scan-errors");
  await writeFile(path.join(root, "ok.mp4"), "x");
  const locked = path.join(root, "locked");
  await mkdir(locked);
  await writeFile(path.join(locked, "inside.mp4"), "x");

  await chmod(locked, 0o000);
  try {
    const result = await scanMediaDirectory(root);
    assert.deepEqual(result.paths, [path.join(root, "ok.mp4")]);
    assert.equal(result.total, 1);
    assert.equal(result.errors, 1);
    assert.equal(result.truncated, false);
  } finally {
    // 恢复权限，确保临时目录能在 after 钩子里被删除。
    await chmod(locked, 0o755);
  }
});

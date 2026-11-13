import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  DEFAULT_PREFERENCES,
  isCloseBehavior,
  readPreferences,
  readPreferencesFile,
  writePreferences,
} = require("../dist/preferences.js");

async function tempDir(t, label) {
  const dir = await mkdtemp(path.join(os.tmpdir(), `yaomu-${label}-`));
  t.after(async () => {
    await rm(dir, { recursive: true, force: true });
  });
  return dir;
}

function prefsFile(dir) {
  return path.join(dir, "desktop-preferences.json");
}

async function tempFiles(dir) {
  return (await readdir(dir)).filter((name) => name.includes(".tmp"));
}

test("defaults to quit with automatic update checks on when the file is missing", async (t) => {
  const dir = await tempDir(t, "prefs-missing");
  const missing = prefsFile(dir);

  assert.equal(readPreferences(missing).closeBehavior, "quit");
  assert.equal(readPreferences(missing).autoCheckUpdates, true);
  const result = readPreferencesFile(missing);
  assert.deepEqual(result.preferences, {
    schemaVersion: 1,
    closeBehavior: "quit",
    autoCheckUpdates: true,
    autoDownloadUpdates: false,
  });
  assert.equal(result.status, "missing");
  assert.equal(DEFAULT_PREFERENCES.closeBehavior, "quit");
  assert.equal(DEFAULT_PREFERENCES.autoCheckUpdates, true);
  assert.equal(DEFAULT_PREFERENCES.autoDownloadUpdates, false);
});

test("falls back to quit with auto checks on for corrupt JSON and non-object payloads", async (t) => {
  const dir = await tempDir(t, "prefs-corrupt");
  const file = prefsFile(dir);
  for (const raw of ["not json", "{", "[]", "null", '"quit"']) {
    await writeFile(file, raw);
    const result = readPreferencesFile(file);
    assert.equal(result.status, "corrupt", `payload: ${raw}`);
    assert.equal(result.preferences.closeBehavior, "quit");
    assert.equal(result.preferences.autoCheckUpdates, true);
  }
});

test("falls back to defaults for unknown schema or close behavior", async (t) => {
  const dir = await tempDir(t, "prefs-unknown");
  const file = prefsFile(dir);

  await writeFile(file, JSON.stringify({ schema_version: 2, close_behavior: "minimize" }));
  let result = readPreferencesFile(file);
  assert.equal(result.status, "unknown");
  assert.equal(result.preferences.closeBehavior, "quit");
  assert.equal(result.preferences.autoCheckUpdates, true);

  await writeFile(file, JSON.stringify({ schema_version: 1, close_behavior: "hide" }));
  result = readPreferencesFile(file);
  assert.equal(result.status, "unknown");
  assert.equal(result.preferences.closeBehavior, "quit");
  assert.equal(result.preferences.autoCheckUpdates, true);

  await writeFile(file, JSON.stringify({ schema_version: 1 }));
  result = readPreferencesFile(file);
  assert.equal(result.status, "unknown");
});

test("reads a valid minimize preference and ignores extra fields", async (t) => {
  const dir = await tempDir(t, "prefs-valid");
  const file = prefsFile(dir);
  await writeFile(
    file,
    JSON.stringify({
      schema_version: 1,
      close_behavior: "minimize",
      auto_check_updates: false,
      future: true,
    }),
  );

  const result = readPreferencesFile(file);
  assert.equal(result.status, "ok");
  assert.deepEqual(result.preferences, {
    schemaVersion: 1,
    closeBehavior: "minimize",
    autoCheckUpdates: false,
    autoDownloadUpdates: false,
  });
  assert.equal(readPreferences(file).closeBehavior, "minimize");
  assert.equal(readPreferences(file).autoCheckUpdates, false);
});

test("legacy preferences without auto_check_updates default the switch to on", async (t) => {
  const dir = await tempDir(t, "prefs-legacy");
  const file = prefsFile(dir);
  await writeFile(file, JSON.stringify({ schema_version: 1, close_behavior: "minimize" }));

  const result = readPreferencesFile(file);
  assert.equal(result.status, "ok");
  assert.deepEqual(result.preferences, {
    schemaVersion: 1,
    closeBehavior: "minimize",
    autoCheckUpdates: true,
    autoDownloadUpdates: false,
  });
});

test("non-boolean auto_check_updates falls back to on without invalidating other fields", async (t) => {
  const dir = await tempDir(t, "prefs-auto-invalid");
  const file = prefsFile(dir);
  for (const bad of ["false", 0, 1, null, [], {}]) {
    await writeFile(
      file,
      JSON.stringify({ schema_version: 1, close_behavior: "minimize", auto_check_updates: bad }),
    );
    const result = readPreferencesFile(file);
    assert.equal(result.status, "ok", `auto_check_updates: ${JSON.stringify(bad)}`);
    assert.equal(result.preferences.closeBehavior, "minimize");
    assert.equal(result.preferences.autoCheckUpdates, true);
    assert.equal(result.preferences.autoDownloadUpdates, false);
  }
});

test("legacy preferences without auto_download_updates keep automatic downloads off", async (t) => {
  const dir = await tempDir(t, "prefs-legacy-download");
  const file = prefsFile(dir);
  await writeFile(
    file,
    JSON.stringify({ schema_version: 1, close_behavior: "minimize", auto_check_updates: true }),
  );

  const result = readPreferencesFile(file);
  assert.equal(result.status, "ok");
  assert.deepEqual(result.preferences, {
    schemaVersion: 1,
    closeBehavior: "minimize",
    autoCheckUpdates: true,
    autoDownloadUpdates: false,
  });
});

test("non-boolean auto_download_updates falls back to off without invalidating other fields", async (t) => {
  const dir = await tempDir(t, "prefs-download-invalid");
  const file = prefsFile(dir);
  for (const bad of ["false", 0, 1, null, [], {}]) {
    await writeFile(
      file,
      JSON.stringify({
        schema_version: 1,
        close_behavior: "minimize",
        auto_check_updates: false,
        auto_download_updates: bad,
      }),
    );
    const result = readPreferencesFile(file);
    assert.equal(result.status, "ok", `auto_download_updates: ${JSON.stringify(bad)}`);
    assert.equal(result.preferences.closeBehavior, "minimize");
    assert.equal(result.preferences.autoCheckUpdates, false);
    assert.equal(result.preferences.autoDownloadUpdates, false);
  }
});

test("round-trips automatic downloads independently of the other switches", async (t) => {
  const dir = await tempDir(t, "prefs-download-roundtrip");
  const file = prefsFile(dir);

  for (const autoDownloadUpdates of [false, true]) {
    writePreferences(file, {
      schemaVersion: 1,
      closeBehavior: "minimize",
      autoCheckUpdates: false,
      autoDownloadUpdates,
    });
    assert.equal(readPreferences(file).autoDownloadUpdates, autoDownloadUpdates);
    assert.equal(readPreferences(file).autoCheckUpdates, false);
    assert.equal(readPreferences(file).closeBehavior, "minimize");
  }
});

test("writes atomically, creates parent directories, and round-trips both fields", async (t) => {
  const dir = await tempDir(t, "prefs-write");
  const file = path.join(dir, "nested", "deeper", "desktop-preferences.json");

  writePreferences(file, {
    schemaVersion: 1,
    closeBehavior: "minimize",
    autoCheckUpdates: false,
    autoDownloadUpdates: false,
  });
  assert.equal(readPreferences(file).closeBehavior, "minimize");
  assert.equal(readPreferences(file).autoCheckUpdates, false);

  const raw = await readFile(file, "utf8");
  assert.deepEqual(JSON.parse(raw), {
    schema_version: 1,
    close_behavior: "minimize",
    auto_check_updates: false,
    auto_download_updates: false,
  });
  assert.equal(raw.endsWith("\n"), true);

  // 覆盖写：仍走同一原子路径，最后保留新值。
  writePreferences(file, {
    schemaVersion: 1,
    closeBehavior: "quit",
    autoCheckUpdates: true,
    autoDownloadUpdates: false,
  });
  assert.equal(readPreferences(file).closeBehavior, "quit");
  assert.equal(readPreferences(file).autoCheckUpdates, true);
  assert.deepEqual(await tempFiles(path.dirname(file)), []);
});

test("round-trips automatic update checks off and on independently of close behavior", async (t) => {
  const dir = await tempDir(t, "prefs-auto-roundtrip");
  const file = prefsFile(dir);

  for (const autoCheckUpdates of [false, true]) {
    writePreferences(file, { schemaVersion: 1, closeBehavior: "quit", autoCheckUpdates, autoDownloadUpdates: false });
    assert.equal(readPreferences(file).autoCheckUpdates, autoCheckUpdates);
    assert.equal(readPreferences(file).closeBehavior, "quit");
  }
});

test("restricts the preference file to owner read/write on POSIX", async (t) => {
  if (process.platform === "win32") {
    t.skip("POSIX permission bits are not meaningful on Windows");
    return;
  }
  const dir = await tempDir(t, "prefs-mode");
  const file = prefsFile(dir);
  writePreferences(file, {
    schemaVersion: 1,
    closeBehavior: "minimize",
    autoCheckUpdates: false,
    autoDownloadUpdates: false,
  });
  const mode = (await stat(file)).mode & 0o777;
  assert.equal(mode, 0o600);
});

test("rejects invalid preferences without writing them", async (t) => {
  const dir = await tempDir(t, "prefs-invalid");
  const file = prefsFile(dir);

  assert.throws(
    () => writePreferences(file, { schemaVersion: 1, closeBehavior: "hide", autoCheckUpdates: true, autoDownloadUpdates: false }),
    /CLOSE_BEHAVIOR_INVALID/,
  );
  assert.throws(
    () => writePreferences(file, { schemaVersion: 2, closeBehavior: "quit", autoCheckUpdates: true, autoDownloadUpdates: false }),
    /SCHEMA_UNSUPPORTED/,
  );
  assert.throws(
    () => writePreferences(file, { schemaVersion: 1, closeBehavior: "quit", autoCheckUpdates: "yes", autoDownloadUpdates: false }),
    /AUTO_CHECK_UPDATES_INVALID/,
  );
  assert.throws(
    () => writePreferences(file, { schemaVersion: 1, closeBehavior: "quit", autoCheckUpdates: true, autoDownloadUpdates: "yes" }),
    /AUTO_DOWNLOAD_UPDATES_INVALID/,
  );
  assert.throws(() => writePreferences(file, null), /PREFERENCES_INVALID/);

  await assert.rejects(readFile(file), /ENOENT/);
  assert.deepEqual(await tempFiles(dir), []);
});

test("cleans up the temp file when the atomic rename fails", async (t) => {
  const dir = await tempDir(t, "prefs-rename-fail");
  // 目标本身是目录：rename(file, dir) 在 POSIX 上失败，临时文件必须被清理。
  const target = path.join(dir, "desktop-preferences.json");
  await mkdir(target);

  assert.throws(() =>
    writePreferences(target, { schemaVersion: 1, closeBehavior: "quit", autoCheckUpdates: true, autoDownloadUpdates: false }),
  );
  assert.deepEqual(await tempFiles(dir), []);
  assert.equal((await stat(target)).isDirectory(), true);
});

test("isCloseBehavior accepts only the two supported values", () => {
  assert.equal(isCloseBehavior("quit"), true);
  assert.equal(isCloseBehavior("minimize"), true);
  for (const bad of ["hide", "", null, undefined, 0, true, {}]) {
    assert.equal(isCloseBehavior(bad), false);
  }
});

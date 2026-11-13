import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const fixture = fileURLToPath(new URL("./fixtures/fake-updater.cjs", import.meta.url));

// 测试注入点自检：隐藏 Electron 驱动可用同一 require hook 得到 fake updater，
// 不触碰真实更新源、不执行安装、不退出应用。
test("fake updater require hook records installer arguments without installing", () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), "yaomu-fake-updater-"));
  const logFile = path.join(dir, "calls.jsonl");
  try {
    const script = `
      const { autoUpdater } = require("electron-updater");
      autoUpdater.on("update-downloaded", (info) => {
        autoUpdater.quitAndInstall(false, true);
      });
      autoUpdater.checkForUpdates().then(() => autoUpdater.downloadUpdate());
    `;
    execFileSync(process.execPath, ["--require", fixture, "-e", script], {
      env: { ...process.env, YAOMU_FAKE_UPDATER_LOG: logFile },
      timeout: 10_000,
    });
    const calls = readFileSync(logFile, "utf8")
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
    assert.deepEqual(
      calls.map((call) => call.event),
      ["checkForUpdates", "downloadUpdate", "quitAndInstall"],
    );
    const install = calls.find((call) => call.event === "quitAndInstall");
    assert.equal(install.silent, false);
    assert.equal(install.forceRunAfter, true);
    assert.equal(install.count, 1);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

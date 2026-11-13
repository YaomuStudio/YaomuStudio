/**
 * 测试专用 fake electron-updater 注入点（不参与生产构建）。
 *
 * 用途：真实隐藏 Electron 驱动需要证明“prepared + Core 已确认停止后才恰好一次
 * quitAndInstall”，但不得发起真实下载/安装。把本文件作为 require hook 加载：
 *
 *   NODE_OPTIONS="--require <repo>/desktop/test/fixtures/fake-updater.cjs" \
 *   YAOMU_FAKE_UPDATER_LOG=/tmp/fake-updater.jsonl electron .
 *
 * `require("electron-updater")` 会被解析到本模块，导出形状与真实包一致：
 * `{ autoUpdater }`。downloadUpdate 只发 update-downloaded（version=9.9.9），
 * quitAndInstall 只追加一行 JSON 记录，不会执行安装或退出应用。
 * 生产 main 不读取本文件，也没有对应环境变量开关。
 */
"use strict";

const fs = require("node:fs");
const Module = require("node:module");
const { EventEmitter } = require("node:events");

function record(event, data) {
  const target = process.env.YAOMU_FAKE_UPDATER_LOG;
  if (!target) return;
  try {
    fs.appendFileSync(target, `${JSON.stringify({ event, at: new Date().toISOString(), ...data })}\n`);
  } catch {
    // 测试驱动未提供可写日志时保持静默，不影响 fake 行为。
  }
}

class FakeAutoUpdater extends EventEmitter {
  constructor() {
    super();
    this.autoDownload = true;
    this.autoInstallOnAppQuit = true;
    this.currentVersion = { version: "0.0.0" };
    this.installCalls = 0;
  }

  checkForUpdates() {
    record("checkForUpdates");
    setImmediate(() => this.emit("update-available", { version: "9.9.9" }));
    return Promise.resolve({ updateInfo: { version: "9.9.9" } });
  }

  downloadUpdate() {
    record("downloadUpdate");
    setImmediate(() => this.emit("update-downloaded", { version: "9.9.9" }));
    return Promise.resolve([]);
  }

  quitAndInstall(silent, forceRunAfter) {
    this.installCalls += 1;
    record("quitAndInstall", { silent: silent === true, forceRunAfter: forceRunAfter === true, count: this.installCalls });
    // 不执行安装、不退出应用：驱动继续读取当日志并在退出后断言调用次数。
  }
}

const autoUpdater = new FakeAutoUpdater();

const originalResolveFilename = Module._resolveFilename;
Module._resolveFilename = function resolveFilename(request, ...rest) {
  if (request === "electron-updater") return __filename;
  return originalResolveFilename.call(this, request, ...rest);
};

module.exports = { autoUpdater };

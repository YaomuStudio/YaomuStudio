import { app, BrowserWindow, ipcMain, nativeTheme, protocol, session, shell } from "electron";
import { autoUpdater } from "electron-updater";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import {
  CoreBootstrapError,
  startCore,
  type CoreLaunchCommand,
} from "./bootstrap";
import { EventBroker, registerEventIpc } from "./events";
import { resolveHostRuntimeEnv } from "./host-runtime";
import { createApiInvoker, registerApiProxy } from "./ipc/api";
import { ASSET_SCHEME, createAssetStreamHandler } from "./ipc/asset-stream";
import { registerAssetSaveIpc } from "./ipc/assets";
import { registerFileIpc } from "./ipc/files";
import { registerNotificationIpc } from "./ipc/notify";
import { RuntimeBridge, registerRuntimeIpc } from "./ipc/runtime";
import { registerDesktopSettingsIpc } from "./ipc/settings";
import {
  UpdateInstallBridge,
  registerUpdateInstallIpc,
} from "./ipc/update-install";
import { DesktopAuthorization, isApplicationURL } from "./ipc/authorization";
import {
  DEFAULT_PREFERENCES,
  readPreferencesFile,
  writePreferences,
  type CloseBehavior,
} from "./preferences";
import { defaultDenyRendererPermissions } from "./permissions";
import {
  validateBundledRunnerResources,
  type BundledRunnerDescriptor,
} from "./runner-resources";
import { RuntimeController } from "./runtime";
import { StartupLifecycle, convergeShutdown } from "./startup-lifecycle";
import type { SmokeCoreContext } from "./smoke";
import { createTray, destroyTray } from "./tray";
import {
  UpdateInstallController,
  createUpdatePreparationClient,
} from "./update-install";
import { selectUpdateFeeds, type UpdateFeed } from "./update-feed";
import { UpdateScheduler, type UpdateCheckSource } from "./update-scheduler";
import { selectUpdateReplay } from "./update-state";
import {
  explicitWslRunnerCommand,
  prepareWslEnvironment,
  resolveWslRunnerEnv,
  translationEnvForWsl,
  type WslPreparation,
} from "./wsl";

// 播放协议必须在 app ready 前声明权限：standard/secure 让其表现同普通 URL，
// supportFetchAPI/corsEnabled 允许 Renderer 读取（含字幕正文预览），stream 让 <audio>/<video> 边下边播并支持 Range seek。
protocol.registerSchemesAsPrivileged([
  {
    scheme: ASSET_SCHEME,
    privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true, corsEnabled: true },
  },
]);

// 启动序列：注册受控 IPC → Bootstrap/Runtime Supervisor 拉起 Core → 加载 UI。
// Core 起不来时窗口仍然创建，Renderer 通过桥拿到 CORE_NOT_READY 的可读错误，
// 不会白屏（模型/核心故障时仍可查看日志与已有数据）。

// 冒烟模式（desktop/scripts/smoke.mjs）：隔离 profile、隐藏窗口、不弹可见界面。
// 生产启动不带 YAOMU_SMOKE，走完全相同的 Bootstrap/桥/安全默认值。
const smokeMode = process.env.YAOMU_SMOKE;
if (smokeMode) {
  const smokeUserData = process.env.YAOMU_SMOKE_USER_DATA;
  if (smokeUserData) app.setPath("userData", smokeUserData);
  app.disableHardwareAcceleration();
}

let mainWindow: BrowserWindow | null = null;
// 唯一 runtime controller 包装唯一 Supervisor；retry 复用同一拥有者。
let runtime: RuntimeController | null = null;
let runtimeBridge: RuntimeBridge | null = null;
// 更新安装守卫同样只有一个：显式同意、Core 排空证明与停止顺序都在 Main。
let updateInstall: UpdateInstallController | null = null;
let updateInstallBridge: UpdateInstallBridge | null = null;
// 本会话由 updater 确认已下载的版本；用于绑定/失效显式安装同意。
let downloadedVersion: string | null = null;
let quitting = false;
// Core 停止并清理完成前，重复 app.quit() 不得绕过退出等待。
let shutdownComplete = false;
// 关闭行为以主进程为唯一权威：Renderer 的 localStorage 只用于设置页展示。
let closeBehavior: CloseBehavior = DEFAULT_PREFERENCES.closeBehavior;
// 自动检查更新同样以主进程为权威；手动“检查更新”不受该开关限制。
let autoCheckUpdates: boolean = DEFAULT_PREFERENCES.autoCheckUpdates;
// 自动下载默认关闭；安装始终需要用户确认。
let autoDownloadUpdates: boolean = DEFAULT_PREFERENCES.autoDownloadUpdates;
// 检查调度器与下载状态只在 setupAutoUpdater 后存在（仅打包态）。
let updateScheduler: UpdateScheduler | null = null;
let updateAvailableVersion: string | null = null;
let updateDownloadInFlight = false;
let updateDownloadSource: "auto" | "manual" | null = null;
let updateFeeds: ReturnType<typeof selectUpdateFeeds> | null = null;
let activeUpdateFeed: UpdateFeed | null = null;
let updateFeedFallbackUsed = false;
let updateFeedFallbackArmed = false;
// 手动检查失败时保留错误状态，不用状态重放把它覆盖成 idle。
let manualCheckFailed = false;
// 初始化闸门：WSL/工具探测与 Core 启动前的准备只运行一次；退出时先取消再等待收敛。
const startup = new StartupLifecycle();
const authorization = new DesktopAuthorization();

// 本次准备的 Runner 来源事实（Main-only，不进入 Renderer 或快照）。
// bundledRunnerSource 只在异步校验通过后设置；外部显式配置不计为包内来源。
let bundledRunnerSource: Pick<BundledRunnerDescriptor, "runnerVersion" | "manifestDigest"> | null =
  null;
let hasExternalWslRunner = false;

const events = new EventBroker({
  log: (line) => log(line),
  isTrustedSender: (sender) => authorization.isTrustedContents(sender),
});

function log(line: string): void {
  // 不输出实例令牌、完整环境变量或媒体/字幕正文。
  console.log(`[yaomu] ${line}`);
}

function describe(error: unknown): string {
  if (error instanceof CoreBootstrapError) {
    return error.hint ? `${error.message}（${error.hint}）` : error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

/** 桌面偏好文件位于应用 userData 下；冒烟模式使用隔离的 YAOMU_SMOKE_USER_DATA。 */
function preferencesPath(): string {
  return path.join(app.getPath("userData"), "desktop-preferences.json");
}

/** Core 数据目录；冒烟打包脚本会显式指向隔离临时目录。 */
function coreDataDir(): string {
  return process.env.YAOMU_DATA_DIR ?? path.join(app.getPath("userData"), "core-data");
}

/** 与 electron-builder.yml `productName` / 打包 Info.plist 对齐。 */
const PRODUCT_NAME = "YaomuStudio";
/** 与 electron-builder.yml `appId` 对齐（Windows 任务栏分组）。 */
const APP_USER_MODEL_ID = "com.aodianyun.yaomustudio";
/**
 * 开发态改名前的 userData 路径。`app.setName` 会在首次 getPath 前改写默认
 * userData；模型包与数据库仍必须留在原目录，避免升级后“模型消失”。
 */
const DEV_USER_DATA = app.isPackaged ? "" : app.getPath("userData");

/**
 * 开发态（`electron .`）品牌图标路径。打包态由 electron-builder 写入 .app/.exe，
 * 不在此覆盖。Dock/`BrowserWindow` 运行时加载用 PNG/ICO；`.icns` 仅供打包写入 bundle。
 */
function brandIconPath(): string | null {
  if (app.isPackaged) return null;
  // macOS 的 app.dock.setIcon 对仓库内 .icns 可能 Failed to load；PNG 可靠。
  const file = process.platform === "win32" ? "icon.ico" : "icon.png";
  const candidate = path.join(__dirname, "..", "resources", file);
  return fs.existsSync(candidate) ? candidate : null;
}

/**
 * 未打包时在 ready 前对齐开发壳的展示身份：名称与 Win AppUserModelId。
 * 改名前后必须把 userData 固定回原路径，否则现有模型/数据库会被切到新目录。
 */
function applyDevBrandName(): void {
  if (app.isPackaged) return;
  app.setName(PRODUCT_NAME);
  if (DEV_USER_DATA) {
    app.setPath("userData", DEV_USER_DATA);
  }
  if (process.platform === "win32") {
    app.setAppUserModelId(APP_USER_MODEL_ID);
  }
}

/** 未打包时把 Dock 换成品牌图；打包态沿用 bundle 图标。 */
function applyDevBrandIcon(): void {
  if (app.isPackaged) return;
  const iconPath = brandIconPath();
  if (!iconPath || process.platform !== "darwin" || !app.dock) return;
  try {
    app.dock.setIcon(iconPath);
  } catch (error) {
    log(`开发态 Dock 图标未应用：${describe(error)}`);
  }
}

// 尽早改名，减少 Dock/菜单短暂显示 “Electron”。userData 在改名后立即固定回原路径。
if (!app.isPackaged) {
  applyDevBrandName();
}

/** 启动时读取桌面偏好；缺失/损坏/未知值回退默认值，不阻断启动。 */
function loadDesktopPreferences(): void {
  const result = readPreferencesFile(preferencesPath());
  closeBehavior = result.preferences.closeBehavior;
  autoCheckUpdates = result.preferences.autoCheckUpdates;
  autoDownloadUpdates = result.preferences.autoDownloadUpdates;
  if (result.status === "ok") {
    log(
      `桌面偏好：closeBehavior=${closeBehavior}, autoCheckUpdates=${autoCheckUpdates}, autoDownloadUpdates=${autoDownloadUpdates}`,
    );
    return;
  }
  log(
    `桌面偏好不可用（${result.status}），回退 closeBehavior=${DEFAULT_PREFERENCES.closeBehavior}, autoCheckUpdates=${DEFAULT_PREFERENCES.autoCheckUpdates}, autoDownloadUpdates=${DEFAULT_PREFERENCES.autoDownloadUpdates}`,
  );
}

/** 恢复并聚焦主窗口；窗口已销毁时按需重建（托盘仅存在于非冒烟模式）。 */
function showMainWindow(): void {
  if (!mainWindow || mainWindow.isDestroyed()) {
    if (!smokeMode) createWindow();
    return;
  }
  if (mainWindow.isMinimized()) mainWindow.restore();
  mainWindow.show();
  mainWindow.focus();
}

/**
 * 让托盘跟随关闭行为：minimize 时按需创建，其余情况销毁。
 * 托盘创建失败时本次运行回退 quit 语义，避免窗口隐藏后无法恢复。
 */
function syncTray(): void {
  if (smokeMode) return;
  if (closeBehavior === "minimize") {
    const created = createTray({
      onShow: showMainWindow,
      onQuit: () => app.quit(),
      locale: app.getLocale(),
      log: (line) => log(line),
    });
    if (!created) {
      closeBehavior = DEFAULT_PREFERENCES.closeBehavior;
      destroyTray();
    }
    return;
  }
  destroyTray();
}

/** 更新关闭行为；`persist` 时原子写入偏好文件（冒烟模式不写）。 */
function applyCloseBehavior(next: CloseBehavior, persist: boolean): void {
  closeBehavior = next;
  // 先落盘再同步托盘，但写失败也继续同步：否则切到 minimize 时可能隐藏窗口却没有托盘。
  let persistError: unknown;
  if (persist && !smokeMode) {
    try {
      writePreferences(preferencesPath(), {
        schemaVersion: 1,
        closeBehavior: next,
        autoCheckUpdates,
        autoDownloadUpdates,
      });
    } catch (error) {
      log(`桌面偏好保存失败：${describe(error)}`);
      persistError = error;
    }
  }
  syncTray();
  if (persistError) throw new Error("桌面偏好保存失败");
}

/** 更新“自动检查更新”设置；仅 false→true 且打包态触发一次检查，初始化同值同步不重复。 */
function applyAutoCheckUpdates(next: boolean, persist: boolean): void {
  const previous = autoCheckUpdates;
  autoCheckUpdates = next;
  let persistError: unknown;
  if (persist && !smokeMode) {
    try {
      writePreferences(preferencesPath(), {
        schemaVersion: 1,
        closeBehavior,
        autoCheckUpdates: next,
        autoDownloadUpdates,
      });
    } catch (error) {
      log(`桌面偏好保存失败：${describe(error)}`);
      persistError = error;
    }
  }
  if (previous === false && next === true && app.isPackaged) {
    updateScheduler?.setEnabled(true);
    void updateScheduler?.checkNow("auto");
  }
  if (previous === true && next === false) {
    updateScheduler?.setEnabled(false);
  }
  if (persistError) throw new Error("桌面偏好保存失败");
}

/** 更新“自动下载更新”设置；开启后若有已发现但未下载的版本，立即开始下载。 */
function applyAutoDownloadUpdates(next: boolean, persist: boolean): void {
  const previous = autoDownloadUpdates;
  autoDownloadUpdates = next;
  autoUpdater.autoDownload = next;
  let persistError: unknown;
  if (persist && !smokeMode) {
    try {
      writePreferences(preferencesPath(), {
        schemaVersion: 1,
        closeBehavior,
        autoCheckUpdates,
        autoDownloadUpdates: next,
      });
    } catch (error) {
      log(`桌面偏好保存失败：${describe(error)}`);
      persistError = error;
    }
  }
  if (
    previous === false &&
    next === true &&
    app.isPackaged &&
    updateAvailableVersion !== null &&
    downloadedVersion === null &&
    !updateDownloadInFlight
  ) {
    void startUpdateDownload("auto");
  }
  if (persistError) throw new Error("桌面偏好保存失败");
}

/** Core 可执行文件解析顺序：YAOMU_CORE_BIN → 打包资源 → 开发态预构建/`go run`。 */
function resolveCoreLaunch(): CoreLaunchCommand {
  const explicit = process.env.YAOMU_CORE_BIN;
  if (explicit) return { command: explicit, args: [] };

  const executable = process.platform === "win32" ? "yaomu-core.exe" : "yaomu-core";
  if (app.isPackaged) {
    return { command: path.join(process.resourcesPath, "core", executable), args: [] };
  }

  const repoRoot = path.join(app.getAppPath(), "..");
  const prebuilt = path.join(repoRoot, "app-server", "bin", executable);
  if (fs.existsSync(prebuilt)) return { command: prebuilt, args: [] };

  const serverDir = path.join(repoRoot, "app-server");
  if (fs.existsSync(path.join(serverDir, "go.mod"))) {
    return { command: "go", args: ["run", "./cmd/server"], cwd: serverDir };
  }

  throw new CoreBootstrapError(
    "CORE_NOT_READY",
    "找不到 Core 可执行文件",
    "开发态可设置 YAOMU_CORE_BIN；或在 app-server/ 下构建 Core。",
  );
}

/**
 * 模型发布分发源前缀。Core 内嵌产品 interim 目录时会自带前缀；此处仅在未显式设置时
 * 注入 FlashHub CDN，避免空前缀导致下载全部拒绝。目录本身仍由 Core 内嵌/覆盖加载。
 */
function resolveReleaseEnv(): NodeJS.ProcessEnv {
  if (process.env.YAOMU_RELEASE_BASE_URLS) return {};
  return { YAOMU_RELEASE_BASE_URLS: "https://flashhub-cdn.aodianyun.com/" };
}

function applyCoreConnection(): void {
  events.setConnection(runtime?.getConnection() ?? null);
}

/**
 * 构造唯一 runtime controller：一个 Supervisor 覆盖准备、启动、握手与重试。
 * launcher 顺序为 prepare（WSL/工具探测）→ startCore → 认证 system 握手；
 * 只有握手确认的实例会通过 getConnection 暴露给业务代理与事件桥。
 */
function createRuntimeController(dataDir: string): RuntimeController {
  let launch: CoreLaunchCommand | null = null;
  let runtimeEnv: NodeJS.ProcessEnv = {};

  return new RuntimeController({
    prepare: async (signal) => {
      launch = resolveCoreLaunch();
      // 方案 2：先启动 yaomu 镜像（失败不阻断 APP_READY），由镜像提供 ffmpeg/flashcli；
      // 客户端不再探测宿主 FFmpeg，也不再注入宿主 Runner。
      // 探测全部异步且有界；取消/无法确认清理时不得继续后续探测或启动 Core。
      const wslPreparation: WslPreparation = await prepareWslEnvironment({
        signal,
        log: (line) => log(line),
      });
      // 打包态默认 Runner 只来自应用自有资源：先异步校验，再交给纯 argv 投影。
      // 资源损坏时保持 Runner 不可用，但 Core 仍可启动供编辑/诊断（architecture §3）。
      const coreDir = path.join(process.resourcesPath, "core");
      hasExternalWslRunner = explicitWslRunnerCommand(process.env) !== null;
      bundledRunnerSource = null;
      let bundledRunner: BundledRunnerDescriptor | undefined;
      let bundledRunnerValidated = false;
      const shouldValidateBundledRunner =
        app.isPackaged && (process.platform !== "win32" || !hasExternalWslRunner);
      if (shouldValidateBundledRunner) {
        const validated = await validateBundledRunnerResources({
          coreDir,
          signal,
        });
        if (validated.ok) {
          bundledRunner = validated.descriptor;
          bundledRunnerValidated = true;
          bundledRunnerSource = {
            runnerVersion: validated.descriptor.runnerVersion,
            manifestDigest: validated.descriptor.manifestDigest,
          };
          log(
            `[bootstrap] bundled runner resources verified: version=${validated.descriptor.runnerVersion} ` +
              `manifest=${validated.descriptor.manifestDigest.slice(0, 12)}`,
          );
        } else {
          // 固定安全诊断：只记有限 reason，不输出文件名/路径等动态 detail。
          log(
            `[bootstrap] bundled runner resources unavailable (${validated.reason}) — ` +
              "请修复或重新安装应用后重启；编辑与诊断仍可用",
          );
        }
      }
      const hostRuntime = resolveHostRuntimeEnv({
        platform: process.platform,
        packaged: app.isPackaged,
        coreDir,
        runnerValidated: bundledRunnerValidated,
        env: process.env,
      });
      if (hostRuntime.reason === "ok") {
        log("[bootstrap] host runner configured (bundled source)");
        if (hostRuntime.env.FLASHCLI_BIN) {
          log("[bootstrap] host FLASHCLI_BIN injected (separate/voice_clone → flashcli)");
        }
        if (hostRuntime.env.MLSTOOLS_BIN) {
          log("[bootstrap] host MLSTOOLS_BIN injected (06 translate → mlstools)");
        }
      } else if (process.platform !== "win32") {
        log(`[bootstrap] bundled host runtime not injected (${hostRuntime.reason})`);
      }
      runtimeEnv = {
        ...resolveReleaseEnv(),
        ...hostRuntime.env,
        ...resolveWslRunnerEnv({
          tools: wslPreparation.tools,
          runtimeReady: wslPreparation.runtimeReady,
          bundledRunner,
        }),
      };
      if (runtimeEnv.YAOMU_WSL_RUNNER_COMMAND) {
        // 只记录来源类别与能力执行器选择；不输出完整 argv/env（可能含本机路径与配置）。
        log(
          hasExternalWslRunner
            ? "[bootstrap] wsl runner configured (external override; bundled source not verified)"
            : "[bootstrap] wsl runner configured (bundled source)",
        );
        const argsRaw = runtimeEnv.YAOMU_WSL_RUNNER_ARGS ?? "";
        if (argsRaw.includes("FLASHCLI_BIN=")) {
          log("[bootstrap] wsl FLASHCLI_BIN injected (separate/voice_clone → flashcli)");
        } else {
          // 不再隐式注入 YAOMU_MOCK_MODEL_SLOTS；开发态需要时由开发者显式设置。
          log("[bootstrap] wsl FLASHCLI_BIN missing — model slots rely on explicit environment");
        }
        if (argsRaw.includes("MLSTOOLS_BIN=")) {
          log("[bootstrap] wsl MLSTOOLS_BIN injected (06 translate → mlstools)");
        } else {
          log(
            "[bootstrap] wsl MLSTOOLS_BIN missing — 06 translate will fail until mlstools is available",
          );
        }
      }
      const bridgedTranslationCount = Object.keys(translationEnvForWsl(process.env)).filter(
        (name) => name !== "WSLENV",
      ).length;
      if (bridgedTranslationCount > 0) {
        log(`[bootstrap] translation provider env bridged (${bridgedTranslationCount} vars)`);
      }
      if (runtimeEnv.YAOMU_RELEASE_BASE_URLS) {
        log(`[bootstrap] release prefixes: ${runtimeEnv.YAOMU_RELEASE_BASE_URLS}`);
      }
    },
    startCore: (signal) => {
      if (!launch) {
        throw new CoreBootstrapError("CORE_NOT_READY", "Core 启动参数缺失");
      }
      return startCore({
        dataDir,
        launch,
        env: runtimeEnv,
        // 可信打包标记由 main 注入；bootstrap 在最终 spawn env 副本中清理演示键。
        packaged: app.isPackaged,
        // stop 取消进行中的启动；Bootstrap 确认自有 child 退出后才 reject。
        signal,
        log: (line) => log(line),
      });
    },
    maxRestarts: 1,
    log: (line) => log(line),
    onConnectionChange: () => applyCoreConnection(),
  });
}

function registerIpc(controller: RuntimeController, installController: UpdateInstallController): void {
  const getConnection = () => controller.getConnection();
  const invoke = createApiInvoker(getConnection);
  registerApiProxy(invoke, authorization);
  registerAssetSaveIpc(getConnection, authorization);
  // 播放只走主进程自定义协议：handler 用实例令牌取 Core 内容，Renderer 拿不到令牌/路径。
  protocol.handle(ASSET_SCHEME, createAssetStreamHandler(getConnection));
  registerFileIpc(authorization);
  registerNotificationIpc(authorization);
  registerEventIpc(events, authorization);
  registerDesktopSettingsIpc(ipcMain, authorization, {
    getAutoCheckUpdates: () => autoCheckUpdates,
    getAutoDownloadUpdates: () => autoDownloadUpdates,
    applyCloseBehavior: (value) => applyCloseBehavior(value, true),
    applyAutoCheckUpdates: (value) => applyAutoCheckUpdates(value, true),
    applyAutoDownloadUpdates: (value) => applyAutoDownloadUpdates(value, true),
    applyUiScale: (contents, value) => contents.setZoomFactor(value),
  });
  // 宿主初始化桥：只暴露有限快照/订阅/重试；业务事件仍走既有 EventBroker。
  runtimeBridge = new RuntimeBridge(controller, authorization);
  registerRuntimeIpc(ipcMain, runtimeBridge);
  // 更新安装状态桥：独立有限快照/显式同意/等待取消；update:install 只在授权后进入守卫。
  updateInstallBridge = new UpdateInstallBridge(installController, authorization);
  registerUpdateInstallIpc(ipcMain, updateInstallBridge);
}

/**
 * 构造唯一更新安装守卫。Core 访问来自已认证的 RuntimeController（Main-only）；
 * prepared 后由同一拥有者停止 Core；安装器只在停止确认后调用一次。
 */
function createUpdateInstallController(controller: RuntimeController): UpdateInstallController {
  return new UpdateInstallController({
    getRuntimeAccess: () => {
      const identity = controller.getAcceptedIdentity();
      const connection = controller.getConnection();
      if (!identity || !connection) return null;
      return { identity, connection };
    },
    prepare: createUpdatePreparationClient(),
    stopRuntime: () => controller.stop(),
    getDownloadedVersion: () => downloadedVersion,
    installer: {
      quitAndInstall: () => autoUpdater.quitAndInstall(false, true),
    },
    log: (line) => log(line),
  });
}

/** Windows WCO 控件配色：与渲染层 --color-surface / --color-text 对齐，高度同 --topbar-height。 */
function titleBarOverlayOptions(): { color: string; symbolColor: string; height: number } {
  const dark = nativeTheme.shouldUseDarkColors;
  return {
    color: dark ? "#212121" : "#ffffff",
    symbolColor: dark ? "#d4d4d4" : "#3f3f3f",
    height: 52,
  };
}

/**
 * Linux 自绘窗口控件的后端（TopBar 经 yaomu.window 调用）。
 * mac/win 用系统按钮，不走这些通道；仍注册以便 preload 类型统一。
 */
function registerWindowControlsIpc(): void {
  ipcMain.handle("window:minimize", (event) => {
    authorization.requireSender(event);
    mainWindow?.minimize();
  });
  ipcMain.handle("window:maximize-toggle", (event) => {
    authorization.requireSender(event);
    if (!mainWindow) return false;
    if (mainWindow.isMaximized()) {
      mainWindow.unmaximize();
      return false;
    }
    mainWindow.maximize();
    return true;
  });
  ipcMain.handle("window:close", (event) => {
    authorization.requireSender(event);
    mainWindow?.close();
  });
  ipcMain.handle("window:is-maximized", (event) => {
    authorization.requireSender(event);
    return mainWindow?.isMaximized() ?? false;
  });
}

function createWindow(): BrowserWindow {
  const devUrl = process.env.VITE_DEV_SERVER_URL;
  // 打包后 ui/dist 被 electron-builder 映射进 asar 的 ui/dist；
  // 开发态（未打包）回落到仓库根 ui/dist。
  const rendererFile = app.isPackaged
    ? path.join(__dirname, "../ui/dist/index.html")
    : path.join(__dirname, "../../ui/dist/index.html");
  const allowedBase = devUrl ?? pathToFileURL(rendererFile).href;

  const iconPath = brandIconPath();
  const win = new BrowserWindow({
    width: 1280,
    height: 800,
    title: PRODUCT_NAME,
    // 冒烟模式绝不显示窗口；正常启动保持默认可见。
    show: !smokeMode,
    // 开发态补齐 Win/Linux 任务栏与窗口图标；macOS Dock 由 applyDevBrandIcon 处理。
    ...(iconPath ? { icon: iconPath } : {}),
    /*
     * 自绘标题栏：mac 隐藏原生标题栏（红黄绿按钮浮在标题栏左侧留白区）；
     * Windows 用 Window Controls Overlay（系统绘制最小化/最大化/关闭）；
     * Linux 无 WCO，去框后由渲染层 TopBar 自绘与 WCO 同视觉的控件
     * （yaomu.window IPC）。渲染层拖拽区在 ui/src/styles/widgets.css 的 is-frameless 段。
     */
    ...(process.platform === "darwin" ? { titleBarStyle: "hidden" as const } : {}),
    ...(process.platform === "win32"
      ? { titleBarStyle: "hidden" as const, titleBarOverlay: titleBarOverlayOptions() }
      : {}),
    ...(process.platform === "linux" ? { frame: false } : {}),
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  if (process.platform === "win32") {
    // WCO 按钮颜色需与顶栏背景一致；系统深浅色切换时同步重绘。
    const syncOverlay = (): void => {
      if (!win.isDestroyed()) win.setTitleBarOverlay(titleBarOverlayOptions());
    };
    nativeTheme.on("updated", syncOverlay);
    win.once("closed", () => nativeTheme.off("updated", syncOverlay));
  }
  const contents = win.webContents;
  authorization.registerWindow(contents, allowedBase);
  contents.on("did-start-navigation", (details) => {
    if (!details.isMainFrame || details.isSameDocument) return;
    authorization.resetWindow(contents);
    events.detachWindow(contents);
    runtimeBridge?.detachWindow(contents);
    updateInstallBridge?.detachWindow(contents);
  });
  contents.once("destroyed", () => {
    authorization.removeWindow(contents);
    events.detachWindow(contents);
    runtimeBridge?.detachWindow(contents);
    updateInstallBridge?.detachWindow(contents);
  });
  if (smokeMode) {
    // 隐藏窗口默认会被节流，避免冒烟截图拿到未绘制的帧。
    win.webContents.setBackgroundThrottling(false);
  }

  // 外链只交系统浏览器（限 http/https），不新开 Electron 窗口，也不在应用窗口内导航。
  const openExternal = (url: string): void => {
    if (/^https?:\/\//i.test(url)) void shell.openExternal(url);
  };
  win.webContents.setWindowOpenHandler(({ url }) => {
    openExternal(url);
    return { action: "deny" };
  });
  win.webContents.on("will-navigate", (event, url) => {
    if (isApplicationURL(url, allowedBase)) return;
    event.preventDefault();
    openExternal(url);
  });

  mainWindow = win;
  // 窗口重新获得焦点时补查一次更新；节流与是否启用由调度器决定。
  win.on("focus", () => updateScheduler?.onFocus());
  // 首次加载/刷新完成后补发当前更新状态，避免错过启动检查事件。
  win.webContents.on("did-finish-load", () => sendCurrentUpdateState());
  win.on("closed", () => {
    if (mainWindow === win) mainWindow = null;
  });

  // 关闭行为：minimize 时只隐藏窗口并保留托盘；真正退出由 before-quit 放行。
  win.on("close", (event) => {
    if (quitting || smokeMode) return;
    if (closeBehavior !== "minimize") return;
    event.preventDefault();
    win.hide();
    syncTray();
  });

  if (devUrl) {
    void win.loadURL(devUrl);
  } else {
    void win.loadFile(rendererFile);
  }
  return win;
}

function sendUpdate(channel: string, payload?: unknown): void {
  mainWindow?.webContents.send(channel, payload);
}

/**
 * 向当前 Renderer 重放有限更新状态。
 * 首次加载/刷新时补发一次，避免启动检查早于 Renderer 订阅而丢事件；
 * 手动检查完成后也调用，避免加入已在飞行的检查时停在 checking。
 */
function sendCurrentUpdateState(): void {
  const event = selectUpdateReplay(downloadedVersion, updateAvailableVersion);
  sendUpdate(event.channel, event.payload);
}

/** 按区域选择首选更新通道；构建产物默认指向对象存储，运行时可按平台覆盖。 */
function applyUpdateFeed(feed: UpdateFeed): void {
  activeUpdateFeed = feed;
  if (feed.provider === "generic") {
    autoUpdater.setFeedURL({ provider: "generic", url: feed.url });
  } else {
    autoUpdater.setFeedURL({ provider: "github", owner: feed.owner, repo: feed.repo });
  }
}

function initializeUpdateFeed(): void {
  if (updateFeeds) return;
  updateFeeds = selectUpdateFeeds(
    app.getLocale(),
    Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
  applyUpdateFeed(updateFeeds.primary);
  log(`更新通道：${updateFeeds.primary.id}（备用 ${updateFeeds.fallback.id}）`);
}

/**
 * 首选通道检查失败时切换到备用通道一次；同一会话内成功切换后保持备用源。
 * `updateFeedFallbackArmed` 让 error 事件先不向 Renderer 暴露，避免首源失败瞬间闪错。
 */
async function checkForUpdatesWithFeedFallback(): Promise<
  Awaited<ReturnType<typeof autoUpdater.checkForUpdates>>
> {
  if (!updateFeeds) initializeUpdateFeed();
  const feeds = updateFeeds;
  if (!feeds) throw new Error("update feeds unavailable");
  updateFeedFallbackArmed = !updateFeedFallbackUsed;
  try {
    const result = await autoUpdater.checkForUpdates();
    updateFeedFallbackArmed = false;
    return result;
  } catch (error) {
    updateFeedFallbackArmed = false;
    if (updateFeedFallbackUsed) throw error;
    updateFeedFallbackUsed = true;
    log(`更新通道切换：${activeUpdateFeed?.id ?? "unknown"} -> ${feeds.fallback.id}`);
    applyUpdateFeed(feeds.fallback);
    return autoUpdater.checkForUpdates();
  }
}

/**
 * 自动检查更新调度：仅在打包态创建；开关关闭时不发起任何检查。
 * 手动 `update:check` 由白名单 IPC 独立触发，不受该开关限制。
 */
function startUpdateScheduler(): void {
  if (smokeMode || !app.isPackaged) return;
  updateScheduler = new UpdateScheduler({
    isEnabled: () =>
      !smokeMode && app.isPackaged && autoCheckUpdates && !quitting,
    // 已有待安装版本或正在下载时只阻断自动/聚焦检查；manual 仍可检查新版本。
    isCheckBlocked: () => downloadedVersion !== null || updateDownloadInFlight,
    isInstallActive: () => {
      const phase = updateInstall?.getSnapshot().phase;
      return (
        phase === "waiting" ||
        phase === "preparing" ||
        phase === "stopping" ||
        phase === "installing"
      );
    },
    check: async () => {
      const result = await checkForUpdatesWithFeedFallback();
      // 自动下载时 checkForUpdates 返回的 downloadPromise 也必须挂 catch，
      // 否则下载失败会成为未处理拒绝；UI 侧仍由 error 事件收敛。
      void result?.downloadPromise?.catch(() => undefined);
    },
    onError: (source, error) => handleUpdateCheckError(source, error),
    log: (line) => log(line),
  });
  if (!autoCheckUpdates) {
    log("自动检查更新已关闭");
    return;
  }
  updateScheduler.setEnabled(true);
  void updateScheduler.checkNow("auto");
}

/** 后台检查失败静默；只有用户主动检查才向 Renderer 暴露有限错误。 */
function handleUpdateCheckError(source: UpdateCheckSource, error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  log(`检查更新失败：${message}`);
  if (source === "manual") {
    manualCheckFailed = true;
    // Renderer 只收到固定信号；细节留在本机日志，不把原始异常文本带进页面。
    sendUpdate("update:error", "update_failed");
  } else if (updateAvailableVersion !== null) {
    // 已有可用版本时，后台失败不得清掉顶栏入口。
    sendUpdate("update:status", "available");
  } else {
    // 让 About 页从 checking 回到 idle，不产生错误 toast。
    sendUpdate("update:status", "idle");
  }
}

/** 下载更新；manual 失败显示有限错误，auto 失败只记录，保留“可更新”入口供重试。 */
function startUpdateDownload(source: "auto" | "manual"): Promise<void> {
  if (updateDownloadInFlight) return Promise.resolve();
  updateDownloadInFlight = true;
  updateDownloadSource = source;
  return Promise.resolve()
    .then(() => autoUpdater.downloadUpdate())
    .then(() => undefined)
    .catch((error: unknown) => {
      const message = error instanceof Error ? error.message : String(error);
      log(`下载更新失败（${source}）：${message}`);
      if (source === "manual") {
        sendUpdate("update:error", "update_failed");
      } else if (updateAvailableVersion !== null) {
        // 自动下载失败保留顶栏“可更新”入口供重试，不重复弹更新通知。
        sendUpdate("update:status", "available");
      }
    })
    .finally(() => {
      updateDownloadInFlight = false;
      updateDownloadSource = null;
    });
}

/** 开发态没有打包更新源：提供有限空桥，避免 Renderer 调用更新 IPC 时报未注册。 */
function registerDevUpdateIpc(): void {
  ipcMain.handle("update:ready", (event) => {
    authorization.requireSender(event);
  });
  ipcMain.handle("update:check", (event) => {
    authorization.requireSender(event);
    sendUpdate("update:not-available", { version: app.getVersion() });
  });
  ipcMain.handle("update:download", (event) => {
    authorization.requireSender(event);
  });
}

function setupAutoUpdater(): void {
  initializeUpdateFeed();
  // 自动下载跟随用户设置；安装始终需要显式确认（FR-MODEL-04）。
  autoUpdater.autoDownload = autoDownloadUpdates;
  // 普通退出绝不隐式安装：安装只能经排空守卫显式调用 quitAndInstall。
  // electron-updater 6.x 会先 install 再 app.quit，before-quit 兜底不足以阻止。
  autoUpdater.autoInstallOnAppQuit = false;

  autoUpdater.on("checking-for-update", () => sendUpdate("update:status", "checking"));
  autoUpdater.on("update-available", (info) => {
    updateAvailableVersion = typeof info.version === "string" ? info.version : null;
    if (autoDownloadUpdates) {
      updateDownloadInFlight = true;
      updateDownloadSource = "auto";
    }
    sendUpdate("update:available", info);
  });
  autoUpdater.on("update-not-available", (info) => {
    updateAvailableVersion = null;
    sendUpdate("update:not-available", info);
  });
  autoUpdater.on("download-progress", (p) => {
    updateDownloadInFlight = true;
    sendUpdate("update:progress", p);
  });
  autoUpdater.on("update-downloaded", (info) => {
    updateDownloadInFlight = false;
    updateDownloadSource = null;
    updateAvailableVersion = null;
    // 先更新 Main 的待装版本，再让守卫失效不匹配的旧同意。
    downloadedVersion = typeof info.version === "string" ? info.version : null;
    updateInstall?.noteDownloadedVersion(downloadedVersion);
    sendUpdate("update:downloaded", info);
  });
  autoUpdater.on("error", (err) => {
    if (updateFeedFallbackArmed) {
      updateFeedFallbackArmed = false;
      log(`更新源检查失败，准备切换：${err.message}`);
      return;
    }
    log(`更新器错误：${err.message}`);
    // 6.x BaseUpdater 安装失败走 dispatchError 而非 throw；守卫把 installing 收敛为
    // 有限 install_failed（Core 已停止，绝不自动重装）。
    const wasInstalling = updateInstall?.getSnapshot().phase === "installing";
    const downloadSource = updateDownloadSource;
    updateInstall?.noteInstallerError();
    if (wasInstalling) {
      sendUpdate("update:error", "update_failed");
    } else if (downloadSource === "manual") {
      sendUpdate("update:error", "update_failed");
    } else if (downloadSource === "auto" && updateAvailableVersion !== null) {
      // 自动下载失败保留“可更新”入口供重试，不重复弹通知。
      sendUpdate("update:status", "available");
    }
    updateDownloadInFlight = false;
    updateDownloadSource = null;
  });

  // 白名单 IPC：检查 / 下载逐能力暴露（最小权限）；安装走 update-install 守卫桥。
  // 手动检查始终可用，不读取“自动检查更新”开关。
  ipcMain.handle("update:check", async (event) => {
    authorization.requireSender(event);
    manualCheckFailed = false;
    await updateScheduler?.checkNow("manual");
    // 若加入了已在飞行的检查，终态事件可能已发出；补发一次当前状态。
    // 手动检查失败时保留 error，不用重放覆盖。
    if (!manualCheckFailed) sendCurrentUpdateState();
  });
  // Renderer 首个更新订阅者挂载后请求当前状态；解决 AppShell 晚于启动检查挂载的窗口。
  ipcMain.handle("update:ready", (event) => {
    authorization.requireSender(event);
    sendCurrentUpdateState();
  });
  ipcMain.handle("update:download", (event) => {
    authorization.requireSender(event);
    return startUpdateDownload("manual");
  });

  startUpdateScheduler();
}

void app.whenReady().then(async () => {
  // ready 前已进入退出：不再注册初始化、探测或创建窗口。
  if (quitting || startup.cancelled) return;
  loadDesktopPreferences();
  // 名称/任务栏身份已在启动早期对齐；Dock 图标需在 ready 后应用。
  applyDevBrandIcon();
  // Renderer Web 权限默认拒绝；仅受信顶层 fullscreen 例外，窗口创建/加载前安装到实际 session。
  defaultDenyRendererPermissions(authorization, session.defaultSession);
  // 唯一 runtime owner 先构造（不 spawn）；IPC 注册后立即创建/加载窗口，
  // 再异步执行准备与 Core 启动，首窗不等待 WSL/工具探测。
  const dataDir = coreDataDir();
  const controller = createRuntimeController(dataDir);
  runtime = controller;
  // 更新安装守卫与 runtime 同生：只读取已认证 Core 身份，停止时复用同一拥有者。
  const installController = createUpdateInstallController(controller);
  updateInstall = installController;
  registerIpc(controller, installController);
  // 窗口控件 IPC 必须在创建/加载窗口前注册：smoke 模式会提前进入断言分支，
  // 正常模式 renderer 也可能在 createWindow() 后立即调用 window:is-maximized。
  registerWindowControlsIpc();
  // 冒烟 no-core 刻意不拉起 Core：给出有限失败快照，而不是永久 preparing。
  if (smokeMode === "no-core") {
    controller.markFailed("startup_failed", false);
    log("冒烟 no-core 模式：未启动 Core，runtime 标记为有限失败");
  }
  const win = createWindow();
  // 冒烟模式在 syncTray 内直接返回，绝不创建托盘。
  syncTray();

  // 初始化期间已退出（或初始化被取消）：不再启动 Core。
  if (quitting || startup.cancelled) return;

  let startupTask: Promise<void> = Promise.resolve();
  if (smokeMode !== "no-core") {
    // 窗口 load 已触发后再异步 startup；初始化闸门保证只跑一次，
    // 退出时 before-quit 取消并等待其有界收敛。
    startupTask = startup.run(() => controller.start());
    if (!smokeMode) {
      void startupTask.catch((error: unknown) => log(`初始化失败：${describe(error)}`));
    }
  }

  if (smokeMode) {
    // ready 冒烟等启动完成后再执行既有断言；窗口可以先出现。
    await startupTask.catch((error: unknown) => log(`初始化失败：${describe(error)}`));
    let code = 1;
    try {
      const { runSmoke } = await import("./smoke");
      // Main-only 冒烟上下文：当前已认证 Core 的 epoch/port、包内 Runner 描述与隔离数据目录。
      // Windows 打包态要求同次 Core 的模型侧 Runner 证据；其他平台明确 not-run。
      const smokeContext: SmokeCoreContext = {
        requireRunnerModelReady:
          app.isPackaged && process.platform === "win32" && smokeMode === "ready",
        dataDir,
        getCurrentCore: () => {
          const identity = controller.getAcceptedIdentity();
          const connection = controller.getConnection();
          if (!identity || !connection) return null;
          const port = Number(new URL(connection.baseUrl).port);
          if (!Number.isSafeInteger(port) || port <= 0) return null;
          return {
            epoch: identity.serverEpoch,
            port,
            generation: identity.generation,
          };
        },
        getBundledRunner: () => bundledRunnerSource,
        hasExternalWslRunner: () => hasExternalWslRunner,
      };
      code = await runSmoke(win, smokeMode, authorization, smokeContext);
    } catch (error) {
      log(`冒烟失败：${describe(error)}`);
    }
    // 先停止本次拉起的 Core 进程树，再以冒烟结果退出。
    try {
      await controller.stop();
    } catch (error) {
      log(`停止 Core 失败：${describe(error)}`);
      // 未确认停止不能沿冒烟 code=0 退出，即使断言本身已通过。
      code = 1;
    }
    app.exit(code);
    return;
  }

  if (app.isPackaged) {
    setupAutoUpdater();
  } else {
    registerDevUpdateIpc();
  }
  app.on("activate", () => {
    if (quitting || startup.cancelled) return;
    if (!mainWindow || mainWindow.isDestroyed()) createWindow();
    else showMainWindow();
  });
});

// 应用退出：先取消初始化并停 Core，确认进程树退出后再销毁托盘并真正退出。
// 停止期间的重复 quit 被阻止，避免第二次 before-quit 绕过等待。
app.on("before-quit", (event) => {
  if (shutdownComplete) return;
  event.preventDefault();
  if (quitting) return;
  quitting = true;
  // 普通退出取消待装同意：此后迟到的 prepared/停止回包不得调用安装器。
  // autoInstallOnAppQuit 已关闭，安装库不会在退出钩子里隐式安装。
  updateInstall?.cancelConsent();
  // 退出期间不再发起新的周期/聚焦检查。
  updateScheduler?.stop();
  // 先取消初始化：晚到的探测结果不得继续探测、拉起 Core 或创建窗口。
  startup.cancel();
  void (async () => {
    events.stop();
    runtimeBridge?.stop();
    updateInstallBridge?.stop();
    // 两路并行等待：Supervisor.stop 立刻挂接拒绝处理；初始化等待自身有界。
    // 即使初始化未收敛，也报告未确认事实并走既有退出兜底，不无限等待。
    const convergence = await convergeShutdown({
      startup,
      stopSupervisor: () => (runtime ? runtime.stop() : Promise.resolve()),
    });
    if (convergence.startup.kind === "timeout") {
      log(
        `初始化未在期限内收敛，按未确认退出处理并继续退出：${describe(convergence.startup.error)}`,
      );
    }
    if (convergence.supervisor.kind === "failed") {
      log(`停止 Core 失败：${describe(convergence.supervisor.error)}`);
    }
    destroyTray();
    shutdownComplete = true;
    app.quit();
  })();
});

app.on("window-all-closed", () => {
  // 最小化到托盘时即使没有可见窗口也保持运行；冒烟模式自行以 app.exit 收尾。
  if (smokeMode || quitting) return;
  if (closeBehavior === "minimize") return;
  // quit 行为在三个平台一致：关闭最后一个窗口即走 before-quit 停 Core 后退出。
  app.quit();
});

/**
 * 无窗口冒烟（仅在 YAOMU_SMOKE 设置时由 main 调用，生产启动不会执行）。
 *
 * - ready 模式：Core 已由 Bootstrap 拉起，断言 preload 桥可用、Core 就绪、
 *   真实项目经桥创建后出现在 UI（项目页与工作台），并截图到临时目录。
 * - no-core 模式：不启动 Core，断言桥返回可读的 CORE_NOT_READY，UI 显示错误而非白屏。
 *
 * 结果以单行 `SMOKE_RESULT {json}` 输出到 stdout，由 desktop/scripts/smoke.mjs 汇总；
 * 不使用可见窗口（BrowserWindow 以 show:false 创建，截图取 webContents 帧）。
 */
import { app, type BrowserWindow } from "electron";
import { mkdir, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { DesktopAuthorization } from "./ipc/authorization";
import { observeCoreRunnerEvidence } from "./smoke-evidence";

interface CheckResult {
  name: string;
  ok: boolean;
  detail?: string;
}

/**
 * Main-only 冒烟上下文：只传递当前已认证 Core 的 epoch/port、包内 Runner 描述
 * 与隔离数据目录。不是 Renderer IPC，也不改变生产启动路径。
 */
export interface SmokeCoreContext {
  requireRunnerModelReady: boolean;
  dataDir: string;
  evidenceTimeoutMs?: number;
  getCurrentCore: () => { epoch: number; port: number; generation: number } | null;
  getBundledRunner: () => { runnerVersion: string; manifestDigest: string } | null;
  hasExternalWslRunner: () => boolean;
}

/** 本次冒烟实际观察到的 Core 模型侧执行器证据；非 Windows 明确 not-run。 */
export interface SmokeCoreRunnerEvidence {
  required: boolean;
  ok: boolean;
  detail: string;
  source?: "bundled" | "external" | "unavailable";
  runnerVersion?: string;
  manifestDigest?: string;
}

export interface BridgeEnvelope<T> {
  ok: boolean;
  data?: T;
  error?: { code?: string; message?: string };
}

interface SmokeResult {
  mode: string;
  ok: boolean;
  checks: CheckResult[];
  consoleErrors: string[];
  screenshot?: string;
  pipelineScreenshot?: string;
  pdf?: string;
  captureError?: string;
  /** 高权限 IPC 来源探针只在开发态运行；打包态如实记录 not-run，不参与 ok。 */
  securityProbes?: { ipcSources: "run" | "not-run" };
  /** Windows 严格冒烟要求同次 Core 的模型侧 Runner 就绪证据。 */
  coreRunnerEvidence?: SmokeCoreRunnerEvidence;
}

const SMOKE_PROJECT_NAME = "冒烟测试项目";
const SMOKE_PLAYBACK_PROJECT_NAME = "冒烟播发项目";

/** 生成 16-bit 单声道 PCM WAV（静音）作为真实可解码媒体，而不是纯文本假字节。 */
function wavFixture(durationMs: number): Buffer {
  const sampleRate = 8000;
  const samples = Math.max(1, Math.floor((sampleRate * durationMs) / 1000));
  const dataSize = samples * 2;
  const buffer = Buffer.alloc(44 + dataSize);
  buffer.write("RIFF", 0);
  buffer.writeUInt32LE(36 + dataSize, 4);
  buffer.write("WAVE", 8);
  buffer.write("fmt ", 12);
  buffer.writeUInt32LE(16, 16);
  buffer.writeUInt16LE(1, 20); // PCM
  buffer.writeUInt16LE(1, 22); // mono
  buffer.writeUInt32LE(sampleRate, 24);
  buffer.writeUInt32LE(sampleRate * 2, 28);
  buffer.writeUInt16LE(2, 32);
  buffer.writeUInt16LE(16, 34);
  buffer.write("data", 36);
  buffer.writeUInt32LE(dataSize, 40);
  // 数据区保持静音；只验证容器/协议/seek，不依赖音频输出设备。
  return buffer;
}

const delay = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

async function evaluate<T>(win: BrowserWindow, script: string): Promise<T | undefined> {
  try {
    return (await win.webContents.executeJavaScript(script, true)) as T;
  } catch {
    return undefined;
  }
}

async function waitFor(win: BrowserWindow, predicate: string, timeoutMs = 20_000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await evaluate<boolean>(win, `Boolean(${predicate})`);
    if (ok === true) return true;
    await delay(200);
  }
  return false;
}

/**
 * 有界等待本次已认证 Core 的模型侧 Runner 证据。
 * 只接受同一个 epoch/port/generation；期间重启或连接丢失按失败处理。
 */
async function observeRunnerEvidence(
  context: SmokeCoreContext,
  timeoutMs: number,
): Promise<{ ok: boolean; detail: string }> {
  return observeCoreRunnerEvidence(
    { dataDir: context.dataDir, getCurrentCore: context.getCurrentCore },
    { timeoutMs },
  );
}

interface ShellState {
  ready: boolean;
  onboardingEnvironment?: boolean;
  onboardingDeviceName?: string;
  onboardingDeviceVisible?: boolean;
}

/**
 * 首次启动会进 onboarding：ready 模式先实际验证环境步骤，再回到工作台；
 * no-core 模式直接写入已引导标记，避免依赖不可用的 Core。
 */
async function ensureShell(win: BrowserWindow, mode: string): Promise<ShellState> {
  const ready = await waitFor(
    win,
    "document.readyState === 'complete' && !!document.querySelector('#app') && document.querySelector('#app').children.length > 0",
  );
  if (!ready) return { ready: false };

  let onboardingEnvironment: boolean | undefined;
  let onboardingDeviceName: string | undefined;
  let onboardingDeviceVisible: boolean | undefined;

  if (mode === "ready") {
    await evaluate(
      win,
      `(() => {
        localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: false }));
        localStorage.setItem("yaomu.onboarding.step", "environment");
        location.hash = "#/onboarding";
        location.reload();
        return true;
      })()`,
    );
    onboardingEnvironment = await waitFor(
      win,
      `!!document.querySelector('[data-testid="onboarding-environment-checks"]')`,
      25_000,
    );
    onboardingDeviceName = await evaluate<string | undefined>(
      win,
      `(async () => {
        const result = await window.yaomu.api.getDevices();
        if (!result?.ok || !Array.isArray(result.data)) return undefined;
        const device = result.data.find((item) => item.recommended) ?? result.data[0];
        return device?.name;
      })()`,
    );
    if (onboardingDeviceName) {
      onboardingDeviceVisible = await waitFor(
        win,
        `document.body.innerText.includes(${JSON.stringify(onboardingDeviceName)})`,
      );
    }
  }

  await evaluate(
    win,
    `(() => {
      localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: true }));
      localStorage.removeItem("yaomu.onboarding.step");
      location.hash = "#/";
      location.reload();
      return true;
    })()`,
  );
  const shellReady = await waitFor(
    win,
    mode === "no-core"
      ? // 首次 ready 前业务页不挂载；no-core 以初始化失败面板作为有限状态。
        "document.readyState === 'complete' && (!!document.querySelector('.app-shell') || !!document.querySelector('[data-testid=\"runtime-startup-panel\"]'))"
      : "document.readyState === 'complete' && !!document.querySelector('.app-shell')",
    25_000,
  );
  return {
    ready: shellReady,
    onboardingEnvironment,
    onboardingDeviceName,
    onboardingDeviceVisible,
  };
}

async function capture(
  win: BrowserWindow,
  mode: string,
): Promise<{ screenshot?: string; pdf?: string; error?: string }> {
  const dir = path.join(os.tmpdir(), "yaomu-smoke");
  const screenshotPath = path.join(dir, `yaomu-smoke-${mode}.png`);
  const pdfPath = path.join(dir, `yaomu-smoke-${mode}.pdf`);
  try {
    await mkdir(dir, { recursive: true });
    // 给渲染器一个稳定帧再截图；隐藏窗口依赖离屏渲染。
    await delay(800);
    const image = await win.webContents.capturePage();
    if (!image.isEmpty()) {
      await writeFile(screenshotPath, image.toPNG());
      return { screenshot: screenshotPath };
    }
    // capturePage 在个别平台对隐藏窗口返回空图：用 PDF 作为可核对的替代证据。
    const pdf = await win.webContents.printToPDF({ printBackground: true });
    await writeFile(pdfPath, pdf);
    return { pdf: pdfPath, error: "capturePage returned an empty image" };
  } catch (error) {
    return { error: error instanceof Error ? error.message : String(error) };
  }
}

/** Chromium 为磁盘文件生成真实 File，再走生产 preload；合成 File 不能替代此验证。 */
async function authorizeNativeFile(win: BrowserWindow, fixture: string): Promise<string> {
  const debuggerApi = win.webContents.debugger;
  let attached = false;
  try {
    await evaluate(win, `(() => {
      const input = document.createElement("input");
      input.type = "file";
      input.id = "smoke-native-file";
      input.hidden = true;
      document.body.appendChild(input);
    })()`);
    debuggerApi.attach("1.3");
    attached = true;
    const document = await debuggerApi.sendCommand("DOM.getDocument") as { root: { nodeId: number } };
    const input = await debuggerApi.sendCommand("DOM.querySelector", {
      nodeId: document.root.nodeId,
      selector: "#smoke-native-file",
    }) as { nodeId: number };
    await debuggerApi.sendCommand("DOM.setFileInputFiles", { nodeId: input.nodeId, files: [fixture] });
    return await evaluate<string>(win, `window.yaomu.api.getPathForFile(
      document.querySelector("#smoke-native-file").files[0]
    )`) ?? "";
  } finally {
    if (attached) debuggerApi.detach();
    await evaluate(win, `document.querySelector("#smoke-native-file")?.remove()`);
  }
}

export async function runSmoke(
  win: BrowserWindow,
  mode: string,
  authorization: DesktopAuthorization,
  coreContext?: SmokeCoreContext,
): Promise<number> {
  const checks: CheckResult[] = [];
  const consoleErrors: string[] = [];
  const check = (name: string, ok: boolean, detail?: string): void => {
    checks.push({ name, ok, detail });
  };
  // 仅 Main 为本次 smoke 自建 fixture 登记；不在生产 Renderer 暴露字符串授权入口。
  const grantFixture = (fixture: string): void => {
    authorization.requireWindow(win.webContents).paths.grantInputFiles([fixture]);
  };

  win.webContents.on("console-message", (...args: unknown[]) => {
    const details = args[1];
    if (details && typeof details === "object" && "level" in details) {
      const record = details as { level?: string; message?: string };
      if (record.level === "error") {
        consoleErrors.push(String(record.message ?? "").slice(0, 500));
      }
      return;
    }
    // 兼容旧签名 (event, level, message)；level 3 是 error。
    if ((args[1] === 3 || args[1] === "error") && typeof args[2] === "string") {
      consoleErrors.push(args[2].slice(0, 500));
    }
  });

  const shell = await ensureShell(win, mode);
  check("ui.shell", shell.ready, shell.ready ? "app shell rendered" : "UI did not render");
  if (mode === "ready") {
    check(
      "ui.onboardingEnvironment",
      shell.onboardingEnvironment === true,
      shell.onboardingEnvironment ? "environment step rendered" : "environment step missing",
    );
    check(
      "ui.onboardingEnvironmentDevice",
      shell.onboardingDeviceVisible === true,
      shell.onboardingDeviceName,
    );
  }
  check(
    "ui.noBlankPage",
    (await evaluate<boolean>(win, "document.body.innerText.trim().length > 20")) === true,
  );

  const bridgeReady = await evaluate<boolean>(
    win,
    "typeof window.yaomu?.api?.getSystem === 'function' && typeof window.yaomu?.api?.subscribeEvents === 'function'",
  );
  check("bridge.available", bridgeReady === true);
  // 宿主初始化桥必须是有限、可订阅的只读接口，不暴露连接细节。
  const runtimeBridge = await evaluate<boolean>(
    win,
    "typeof window.yaomu?.runtime?.getSnapshot === 'function' && " +
      "typeof window.yaomu?.runtime?.onChanged === 'function' && " +
      "typeof window.yaomu?.runtime?.retry === 'function'",
  );
  check("bridge.runtime", runtimeBridge === true);
  const runtimeSnapshot = runtimeBridge
    ? await evaluate<
        BridgeEnvelope<{ schemaVersion?: number; generation?: number; phase?: string; reason?: string | null; canRetry?: boolean }>
      >(win, "window.yaomu.runtime.getSnapshot()")
    : undefined;
  if (mode === "no-core") {
    check(
      "runtime.finiteFailure",
      runtimeSnapshot?.ok === true &&
        runtimeSnapshot?.data?.phase === "failed" &&
        runtimeSnapshot?.data?.reason === "startup_failed" &&
        runtimeSnapshot?.data?.canRetry === false,
      runtimeSnapshot?.ok === true ? `${runtimeSnapshot.data?.phase}/${runtimeSnapshot.data?.reason}` : runtimeSnapshot?.error?.code,
    );
  } else {
    check(
      "runtime.ready",
      runtimeSnapshot?.ok === true &&
        runtimeSnapshot?.data?.schemaVersion === 1 &&
        runtimeSnapshot?.data?.phase === "ready" &&
        (runtimeSnapshot?.data?.generation ?? 0) >= 1,
      runtimeSnapshot?.ok === true ? `${runtimeSnapshot.data?.phase}/generation=${runtimeSnapshot.data?.generation}` : runtimeSnapshot?.error?.code,
    );
  }
  // 高权限 IPC 来源探针只存在于开发态模块；发行包不携带，打包态如实标 not-run。
  let ipcSourceProbe: "run" | "not-run" = "not-run";
  if (!app.isPackaged) {
    const { probeIpcSources } = await import("./smoke-probe");
    await probeIpcSources(authorization, check);
    ipcSourceProbe = "run";
  }

  const syntheticFile = await evaluate<string>(win, `window.yaomu.api.getPathForFile(
    new File(["synthetic"], "arbitrary-source.mp4")
  )`);
  check("security.syntheticFileDenied", syntheticFile === "");
  const arbitraryPaths = await evaluate<{ importDenied: boolean; exportDenied: boolean; openDenied: boolean }>(win,
    `(async () => {
      const api = window.yaomu.api;
      const imported = await api.importAssets({ project_id: "probe", file_grants: [${JSON.stringify(os.tmpdir())}], copy_mode: false });
      const exported = await api.export({ project_id: "probe", output_dir_grant: ${JSON.stringify(os.tmpdir())} });
      let openDenied = false;
      try { await api.openPath(${JSON.stringify(os.tmpdir())}); }
      catch (error) { openDenied = String(error).includes("LOCAL_ACCESS_DENIED"); }
      return { importDenied: imported?.error?.code === "LOCAL_ACCESS_DENIED",
        exportDenied: exported?.error?.code === "LOCAL_ACCESS_DENIED", openDenied };
    })()`);
  check("security.ungrantedImportDenied", arbitraryPaths?.importDenied === true);
  check("security.ungrantedExportDenied", arbitraryPaths?.exportDenied === true);
  check("security.ungrantedOpenDenied", arbitraryPaths?.openDenied === true);
  const nativeFixture = path.join(os.tmpdir(), `yaomu-native-file-${process.pid}.wav`);
  await writeFile(nativeFixture, wavFixture(100));
  const nativeGrant = await authorizeNativeFile(win, nativeFixture);
  check("security.nativeFileGrant", nativeGrant.length > 0);

  const assetSaveReady = await evaluate<boolean>(
    win,
    "typeof window.yaomu?.api?.saveAsset === 'function'",
  );
  check("bridge.assetSave", assetSaveReady === true);

  // 任务产出默认目录：主进程创建 <程序目录>/outputs 并只对该窗口授权导出。
  const defaultOutput = (await evaluate<string>(
    win,
    `(async () => {
      try { return await window.yaomu.api.getDefaultOutputDir(); }
      catch (error) { return ""; }
    })()`,
  )) ?? "";
  let defaultOutputReady = false;
  try {
    defaultOutputReady = path.basename(defaultOutput) === "outputs" &&
      (await stat(defaultOutput)).isDirectory();
  } catch {
    // 目录未创建或路径不可用，由下面的检查统一报告。
  }
  check("bridge.defaultOutputDir", defaultOutputReady, defaultOutput);
  let defaultOutputGranted = false;
  try {
    authorization.requireWindow(win.webContents).paths.assertApiPaths("export", [
      { output_dir_grant: defaultOutput },
    ]);
    defaultOutputGranted = defaultOutputReady;
  } catch {
    // 未授权时保持失败，由检查统一报告。
  }
  check("security.defaultOutputGranted", defaultOutputGranted);

  const invalidAssetSave = await evaluate<string>(
    win,
    `(async () => {
      try {
        await window.yaomu.api.saveAsset({ assetId: "a/b" });
        return "resolved";
      } catch (error) {
        return String(error);
      }
    })()`,
  );
  check(
    "bridge.assetSaveRejectsInvalid",
    typeof invalidAssetSave === "string" && invalidAssetSave.includes("ASSET_SAVE_INPUT_INVALID"),
    invalidAssetSave,
  );

  const system = bridgeReady
    ? await evaluate<BridgeEnvelope<{ core_ready?: boolean }>>(win, "window.yaomu.api.getSystem()")
    : undefined;

  let screenshot: string | undefined;
  let pipelineScreenshot: string | undefined;
  let pdf: string | undefined;
  let captureError: string | undefined;

  if (mode === "no-core") {
    check(
      "bridge.coreNotReady",
      system?.ok === false && system?.error?.code === "CORE_NOT_READY",
      system?.error?.code,
    );
    // 可读错误：工作台/状态栏或初始化失败面板显示错误文案，而不是白屏。
    const readable = await waitFor(
      win,
      "!!document.querySelector('.notice--error') || !!document.querySelector('.statusbar')?.innerText?.trim() || " +
        "!!document.querySelector('[data-testid=\"runtime-startup-panel\"]')",
      10_000,
    );
    check("ui.errorReadable", readable);
    ({ screenshot, pdf, error: captureError } = await capture(win, mode));
  } else {
    check(
      "bridge.coreReady",
      system?.ok === true && system?.data?.core_ready === true,
      system?.ok === true ? undefined : system?.error?.code,
    );

    const created = await evaluate<BridgeEnvelope<{ id?: string; name?: string }>>(
      win,
      `window.yaomu.api.createProject({ name: ${JSON.stringify(SMOKE_PROJECT_NAME)} })`,
    );
    const createdId = created?.data?.id ?? "";
    check(
      "core.projectCreated",
      created?.ok === true && typeof created?.data?.id === "string",
      created?.error?.code,
    );

    // 补面端点逐一验证：快照（含事件高水位）、审核列表、槽位配置读写。
    if (createdId) {
      const snapshot = await evaluate<BridgeEnvelope<{ event_seq?: number; project_id?: string }>>(
        win,
        `window.yaomu.api.getSnapshot(${JSON.stringify(createdId)})`,
      );
      check(
        "bridge.snapshot",
        snapshot?.ok === true &&
          typeof snapshot?.data?.event_seq === "number" &&
          snapshot?.data?.project_id === createdId,
        snapshot?.error?.code,
      );

      // 真实可解码 WAV + 独立项目：验证 yaomu-asset 协议、Range seek 与无令牌 URL。
      // 不复用上方纯文本假字节导入，避免把 fake bytes 当作播放证据。
      const playbackProject = await evaluate<BridgeEnvelope<{ id?: string }>>(
        win,
        `window.yaomu.api.createProject({ name: ${JSON.stringify(SMOKE_PLAYBACK_PROJECT_NAME)} })`,
      );
      const playbackProjectId = playbackProject?.data?.id ?? "";
      const wavPath = path.join(os.tmpdir(), `yaomu-smoke-smoke-playback-${process.pid}.wav`);
      await writeFile(wavPath, wavFixture(600));
      grantFixture(wavPath);
      const wavImport = playbackProjectId
        ? await evaluate<BridgeEnvelope<{ asset_ids?: string[]; assetIds?: string[] }>>(
            win,
            `window.yaomu.api.importAssets({
              project_id: ${JSON.stringify(playbackProjectId)},
              file_grants: [${JSON.stringify(wavPath)}],
              copy_mode: false,
            })`,
          )
        : undefined;
      const wavAssetId =
        wavImport?.data?.asset_ids?.[0] ?? wavImport?.data?.assetIds?.[0] ?? "";
      const playbackUrl = wavAssetId
        ? await evaluate<string>(
            win,
            `window.yaomu.api.assetUrl(${JSON.stringify(wavAssetId)})`,
          )
        : "";
      check(
        "media.assetUrl",
        typeof playbackUrl === "string" &&
          playbackUrl.startsWith(`yaomu-asset://asset/`) &&
          !playbackUrl.includes("http"),
        playbackUrl || "empty",
      );

      // Chromium 实际加载元数据并 seek：覆盖协议注册、Range/206 与容器解码链路。
      const playback = wavAssetId
        ? await evaluate<{
            loaded: boolean;
            duration: number;
            seeked: boolean;
            error: string;
          }>(
            win,
            `(async () => {
              const url = window.yaomu.api.assetUrl(${JSON.stringify(wavAssetId)});
              const audio = document.createElement("audio");
              audio.preload = "metadata";
              document.body.appendChild(audio);
              const result = { loaded: false, duration: 0, seeked: false, error: "" };
              try {
                await new Promise((resolve, reject) => {
                  const timer = setTimeout(() => reject(new Error("METADATA_TIMEOUT")), 8000);
                  audio.addEventListener("loadedmetadata", () => {
                    clearTimeout(timer);
                    resolve(null);
                  }, { once: true });
                  audio.addEventListener("error", () => {
                    clearTimeout(timer);
                    reject(new Error("MEDIA_ERROR"));
                  }, { once: true });
                  audio.src = url;
                });
                result.loaded = true;
                result.duration = audio.duration;
                audio.currentTime = 0.2;
                await new Promise((resolve) => {
                  const timer = setTimeout(resolve, 3000);
                  audio.addEventListener("seeked", () => {
                    clearTimeout(timer);
                    resolve(null);
                  }, { once: true });
                });
                result.seeked = Math.abs(audio.currentTime - 0.2) < 0.05;
              } catch (error) {
                result.error = String(error);
              }
              audio.removeAttribute("src");
              audio.load();
              audio.remove();
              return result;
            })()`,
          )
        : undefined;
      check(
        "media.assetProtocolSeek",
        playback?.loaded === true && (playback?.duration ?? 0) > 0.5 && playback?.seeked === true,
        playback?.error || `duration=${playback?.duration ?? "missing"}`,
      );

      const reviews = await evaluate<BridgeEnvelope<unknown[]>>(
        win,
        `window.yaomu.api.listReviews(${JSON.stringify(createdId)})`,
      );
      check("bridge.reviews", reviews?.ok === true && Array.isArray(reviews?.data));

      const environment = await evaluate<
        BridgeEnvelope<{ platform?: string; devices?: unknown[]; checks?: unknown[] }>
      >(win, `window.yaomu.api.getEnvironment()`);
      check(
        "bridge.environment",
        environment?.ok === true &&
          typeof environment?.data?.platform === "string" &&
          Array.isArray(environment?.data?.devices) &&
          Array.isArray(environment?.data?.checks),
        environment?.error?.code,
      );

      const listedDevices = await evaluate<
        BridgeEnvelope<Array<{ name?: string; recommended?: boolean; driver_version?: string }>>
      >(win, `window.yaomu.api.getDevices()`);
      const recommendedDevice = Array.isArray(listedDevices?.data)
        ? listedDevices.data.find((device) => device.recommended) ?? listedDevices.data[0]
        : undefined;
      if (recommendedDevice?.name) {
        await evaluate(win, `location.hash = "#/settings/devices"`);
        check(
          "ui.devices",
          await waitFor(
            win,
            `document.body.innerText.includes(${JSON.stringify(recommendedDevice.name)})`,
          ),
          recommendedDevice.name,
        );
        if (recommendedDevice.driver_version) {
          check(
            "ui.devicesDriver",
            await waitFor(
              win,
              `document.body.innerText.includes(${JSON.stringify(recommendedDevice.driver_version)})`,
            ),
            recommendedDevice.driver_version,
          );
        }
      } else {
        check("ui.devices", false, "no recommended device");
      }

      const engineRead = await evaluate<BridgeEnvelope<{ slot?: string; device_id?: string }>>(
        win,
        `window.yaomu.api.getEngineConfig("asr")`,
      );
      check(
        "bridge.engineConfigRead",
        engineRead?.ok === true && engineRead?.data?.slot === "asr",
        engineRead?.error?.code,
      );

      const engineWrite = await evaluate<BridgeEnvelope<{ device_id?: string }>>(
        win,
        `window.yaomu.api.setEngineConfig("asr", { device_id: "auto" })`,
      );
      check(
        "bridge.engineConfigWrite",
        engineWrite?.ok === true && engineWrite?.data?.device_id === "auto",
        engineWrite?.error?.code,
      );
    }

    // 项目列表页与工作台都必须渲染 Core 返回的真实项目。
    await evaluate(win, `location.hash = "#/projects"`);
    const listed = await waitFor(
      win,
      `document.body.innerText.includes(${JSON.stringify(SMOKE_PROJECT_NAME)})`,
    );
    check("ui.projectsListed", listed);

    await evaluate(win, `location.hash = "#/"`);
    const workbench = await waitFor(
      win,
      `document.body.innerText.includes(${JSON.stringify(SMOKE_PROJECT_NAME)})`,
    );
    check("ui.workbenchListed", workbench);
    const taskCenterEntry = await evaluate<boolean>(
      win,
      `document.body.innerText.includes("任务中心") || document.body.innerText.includes("Task center")`,
    );
    check("ui.noTaskCenterEntry", taskCenterEntry === false);
    const taskProgressBars = await evaluate<number>(
      win,
      `document.querySelectorAll(".ym-progress").length`,
    );
    check("ui.noTaskProgress", taskProgressBars === 0);

    // 译制页必须绑定查询参数指定的项目（不再硬编码演示项目）。
    if (createdId) {
      const hash = `#/pipeline?project=${encodeURIComponent(createdId)}`;
      await evaluate(win, `location.hash = ${JSON.stringify(hash)}`);
      const bound = await waitFor(
        win,
        `document.querySelector('.project-select .ym-select__value')?.textContent?.trim() === ${JSON.stringify(SMOKE_PROJECT_NAME)}`,
      );
      const noLoadError = await evaluate<boolean>(
        win,
        "!document.querySelector('.state-box--error') && !!document.querySelector('.wb-page')",
      );
      check("ui.pipelineProjectBound", bound && noLoadError === true, createdId);
      const pipelineCapture = await capture(win, "pipeline");
      pipelineScreenshot = pipelineCapture.screenshot;

      // 新路由：无项目的 /pipeline 进入新建页；项目流水线仍由 /projects/:id/pipeline 承载。
      await evaluate(win, `location.hash = "#/pipeline"`);
      const newPage = await waitFor(
        win,
        `location.hash.includes("/pipeline/new") && (document.body.innerText.includes("新建全流程译制") || document.body.innerText.includes("New full pipeline"))`,
      );
      check("ui.pipelineNewPage", newPage === true);
      const newPageCapture = await capture(win, "pipeline-new");
      check(
        "smoke.pipelineNewScreenshot",
        newPageCapture.screenshot !== undefined || newPageCapture.pdf !== undefined,
        newPageCapture.screenshot ?? newPageCapture.pdf,
      );
    } else {
      check("ui.pipelineProjectBound", false, "project id missing");
    }

    // 工具页同样必须在真实 Core 下渲染工作台骨架，不能白屏或进入错误态。
    await evaluate(win, `location.hash = "#/tools/extract"`);
    const extractBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("字幕提取") || document.body.innerText.includes("Subtitle extraction"))`,
    );
    const extractNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.extractPage", extractBound && extractNoError === true);

    // 真实 Core 预检：导入临时源文件并以 tool_extract 模板调用 preflight，
    // 验证 UI 桥 → Core 模板/目标语言放宽/输入校验链路；不依赖模型完成。
    if (createdId) {
      const sourcePath = path.join(os.tmpdir(), `yaomu-smoke-source-${process.pid}.mp4`);
      await writeFile(sourcePath, Buffer.from("yaomu-smoke-source"));
      grantFixture(sourcePath);
      const preflight = await evaluate<{
        importOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(sourcePath)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { importOk: false, preflightOk: false, issues: -1, error: imported?.error?.code };
          const assetId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          if (!assetId) return { importOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke extract",
            template: "tool_extract",
            source_language: "zh",
            target_languages: [],
            input_revision_ids: [assetId],
            extract: { mode: "asr" },
          });
          return {
            importOk: true,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check(
        "core.toolExtractPreflight",
        preflight?.importOk === true && preflight?.preflightOk === true && (preflight?.issues ?? -1) >= 0,
        preflight?.error ?? `issues=${preflight?.issues ?? "missing"}`,
      );
    }

    // 分离工具页在真实 Core 下渲染；再用真实 Core 预检验证模板/输入/und 链路。
    await evaluate(win, `location.hash = "#/tools/separate"`);
    const separateBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("音频分离") || document.body.innerText.includes("Audio separation"))`,
    );
    const separateNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.separatePage", separateBound && separateNoError === true);

    if (createdId) {
      const sourcePath = path.join(os.tmpdir(), `yaomu-smoke-separate-${process.pid}.mp4`);
      await writeFile(sourcePath, Buffer.from("yaomu-smoke-separate"));
      grantFixture(sourcePath);
      const preflight = await evaluate<{
        importOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(sourcePath)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { importOk: false, preflightOk: false, issues: -1, error: imported?.error?.code };
          const assetId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          if (!assetId) return { importOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke separate",
            template: "tool_separate",
            source_language: "und",
            target_languages: [],
            input_revision_ids: [assetId],
          });
          return {
            importOk: true,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check(
        "core.toolSeparatePreflight",
        preflight?.importOk === true && preflight?.preflightOk === true && (preflight?.issues ?? -1) >= 0,
        preflight?.error ?? `issues=${preflight?.issues ?? "missing"}`,
      );
    }

    // 擦除工具页在真实 Core 下渲染；再用真实 Core 预检验证 OCR/擦除范围/模板链路。
    await evaluate(win, `location.hash = "#/tools/erase"`);
    const eraseBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("字幕擦除") || document.body.innerText.includes("Subtitle erase"))`,
    );
    const eraseNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.erasePage", eraseBound && eraseNoError === true);

    if (createdId) {
      const sourcePath = path.join(os.tmpdir(), `yaomu-smoke-erase-${process.pid}.mp4`);
      await writeFile(sourcePath, Buffer.from("yaomu-smoke-erase"));
      grantFixture(sourcePath);
      const preflight = await evaluate<{
        importOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(sourcePath)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { importOk: false, preflightOk: false, issues: -1, error: imported?.error?.code };
          const assetId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          if (!assetId) return { importOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke erase",
            template: "tool_erase",
            source_language: "chinese",
            target_languages: [],
            input_revision_ids: [assetId],
            extract: { mode: "ocr", ocr_language: "chinese" },
            erase_mode: "dialog_only",
          });
          return {
            importOk: true,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check(
        "core.toolErasePreflight",
        preflight?.importOk === true && preflight?.preflightOk === true && (preflight?.issues ?? -1) >= 0,
        preflight?.error ?? `issues=${preflight?.issues ?? "missing"}`,
      );
    }

    // 翻译工具页 + 真实 Core 的 SRT 导入轨/翻译预检。
    await evaluate(win, `location.hash = "#/tools/translate"`);
    const translateBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("字幕翻译") || document.body.innerText.includes("Subtitle translation"))`,
    );
    const translateNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.translatePage", translateBound && translateNoError === true);

    if (createdId) {
      const sourcePath = path.join(os.tmpdir(), `yaomu-smoke-translate-${process.pid}.srt`);
      await writeFile(sourcePath, Buffer.from(
        "1\n00:00:00,000 --> 00:00:01,000\nHello\n\n2\n00:00:01,200 --> 00:00:02,000\nWorld\n",
      ));
      grantFixture(sourcePath);
      const translate = await evaluate<{
        importOk: boolean;
        trackOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(sourcePath)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { importOk: false, trackOk: false, preflightOk: false, issues: -1, error: imported?.error?.code };
          const assetId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          if (!assetId) return { importOk: false, trackOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const track = await window.yaomu.api.importSubtitleTrack({
            project_id: ${JSON.stringify(createdId)},
            asset_id: assetId,
            language: "en",
          });
          if (!track?.ok) return { importOk: true, trackOk: false, preflightOk: false, issues: -1, error: track?.error?.code };
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke translate",
            template: "tool_translate",
            source_language: "en",
            target_languages: ["zh"],
            input_revision_ids: [assetId],
          });
          return {
            importOk: true,
            trackOk: true,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check(
        "core.subtitleTrackImport",
        translate?.importOk === true && translate?.trackOk === true,
        translate?.error,
      );
      check(
        "core.toolTranslatePreflight",
        translate?.preflightOk === true && (translate?.issues ?? -1) >= 0,
        translate?.error ?? `issues=${translate?.issues ?? "missing"}`,
      );
    }

    // 人工校正真实链路：翻译轨导入 → 保存样式/擦除块 → 默认 outputs 字幕导出。
    if (createdId) {
      const correctionSrt = path.join(os.tmpdir(), `yaomu-smoke-correction-${process.pid}.srt`);
      await writeFile(correctionSrt, Buffer.from(
        "1\n00:00:00,000 --> 00:00:01,000\n你好\n\n2\n00:00:01,200 --> 00:00:02,000\n世界\n",
      ));
      grantFixture(correctionSrt);
      const correction = await evaluate<{
        saveOk: boolean;
        inputOk: boolean;
        styleOk: boolean;
        eraseOk: boolean;
        exportOk: boolean;
        outputDir: string;
        outputPath: string;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(correctionSrt)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { saveOk: false, inputOk: false, styleOk: false, eraseOk: false, exportOk: false, outputDir: "", outputPath: "", error: "IMPORT:" + JSON.stringify(imported?.error ?? {}) };
          const assetId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          const trackResponse = await window.yaomu.api.importSubtitleTrack({
            project_id: ${JSON.stringify(createdId)},
            asset_id: assetId,
            language: "zh",
            type: "translation",
          });
          if (!trackResponse?.ok) return { saveOk: false, inputOk: false, styleOk: false, eraseOk: false, exportOk: false, outputDir: "", outputPath: "", error: "TRACK:" + JSON.stringify(trackResponse?.error ?? {}) };
          // 复用 tool_translate 已导入的 transcription 轨作为唯一源轨；
          // 再导入一条会造成多源歧义，Core 按契约返回空（未知）。
          const projectBefore = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
          const sourceTrack = (projectBefore?.data?.tracks ?? []).find(
            (item) => item.type === "transcription" && item.current_revision_id,
          );
          if (!sourceTrack) return { saveOk: false, inputOk: false, styleOk: false, eraseOk: false, exportOk: false, outputDir: "", outputPath: "", error: "SOURCE_TRACK_MISSING" };
          const sourceRevisionId = sourceTrack.current_revision_id ?? "";
          const track = trackResponse.data;
          const revision = (track.revisions ?? []).find((item) => item.id === track.current_revision_id) ?? (track.revisions ?? [])[0];
          const segments = (revision?.segments ?? []).map((segment) => ({
            id: segment.id,
            start_ms: segment.start_ms,
            end_ms: segment.end_ms,
            text: segment.text,
          }));
          const saved = await window.yaomu.api.saveSubtitle(track.id, {
            base_revision_id: track.current_revision_id,
            segments,
            style: { x: 0.5, y: 0.85, font_size: 52, align: "center" },
            erase_blocks: [{ start_ms: 0, end_ms: 900, bbox: [10, 20, 110, 120], source: "clean" }],
          });
          if (!saved?.ok) return { saveOk: false, inputOk: false, styleOk: false, eraseOk: false, exportOk: false, outputDir: "", outputPath: "", error: "SAVE:" + JSON.stringify(saved?.error ?? {}) };
          const project = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
          const storedTrack = (project?.data?.tracks ?? []).find((item) => item.id === track.id);
          const stored = (storedTrack?.revisions ?? []).find((item) => item.id === saved.data.id);
          const outputDir = await window.yaomu.api.getDefaultOutputDir();
          const exported = await window.yaomu.api.export({
            project_id: ${JSON.stringify(createdId)},
            kind: "subtitle",
            format: "srt",
            input_revision_id: saved.data.id,
            output_dir_grant: outputDir,
            naming_template: "{project}_{language}_{timestamp}",
          });
          if (!exported?.ok) return { saveOk: true, inputOk: false, styleOk: false, eraseOk: false, exportOk: false, outputDir, outputPath: "", error: exported?.error?.code };
          let record = exported.data;
          for (let i = 0; i < 50 && record.status !== "SUCCEEDED" && record.status !== "FAILED"; i += 1) {
            await new Promise((resolve) => setTimeout(resolve, 100));
            const list = await window.yaomu.api.listExports(${JSON.stringify(createdId)});
            record = (list?.data ?? []).find((item) => item.id === record.id) ?? record;
          }
          return {
            saveOk: true,
            inputOk: Boolean(sourceRevisionId) && saved.data.input_revision_id === sourceRevisionId,
            styleOk: stored?.style?.font_size === 52 && stored?.style?.align === "center",
            eraseOk: stored?.erase_blocks?.length === 1 && stored?.erase_blocks?.[0]?.bbox?.[0] === 10,
            exportOk: record.status === "SUCCEEDED" && typeof record.output_path === "string" && record.output_path.startsWith(outputDir),
            outputDir,
            outputPath: record.output_path ?? "",
            error: record.status === "FAILED" ? "EXPORT_FAILED" : undefined,
          };
        })()`,
      );
      check("core.correctionSaveStyleErase",
        correction?.saveOk === true && correction?.styleOk === true && correction?.eraseOk === true,
        correction?.error ?? `save=${correction?.saveOk} style=${correction?.styleOk} erase=${correction?.eraseOk}`);
      check("core.translationInputRevision",
        correction?.inputOk === true,
        correction?.error ?? `input=${correction?.inputOk}`);
      let correctionFile = false;
      try {
        correctionFile = correction?.outputPath
          ? (await stat(correction.outputPath)).isFile()
          : false;
      } catch {
        // 导出路径不存在时由下方检查统一报告。
      }
      check("core.correctionExportDefaultDir",
        correction?.exportOk === true && correctionFile,
        correction?.error ?? correction?.outputPath);
    }

    // 人工校正 UI：真实 Core 下验证 1.5s 防抖自动保存与离开前保存。
    if (createdId) {
      await evaluate(win, `location.hash = "#/projects/${createdId}/correction"`);
      const correctionUiReady = await waitFor(
        win,
        `!!document.querySelector('[data-testid="segment-translation"]')`,
      );
      if (correctionUiReady) {
        const sourceAutosave = await evaluate<{ ok: boolean; error?: string }>(
          win,
          `(async () => {
            const before = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const beforeIds = Object.fromEntries(
              (before?.data?.tracks ?? []).map((track) => [track.id, track.current_revision_id]),
            );
            const textarea = document.querySelector('[data-testid="segment-source"]');
            if (!textarea) return { ok: false, error: "NO_SOURCE_TEXTAREA" };
            const setter = Object.getOwnPropertyDescriptor(
              window.HTMLTextAreaElement.prototype,
              "value",
            ).set;
            setter.call(textarea, "Smoke source autosave");
            textarea.dispatchEvent(new Event("input", { bubbles: true }));
            await new Promise((resolve) => setTimeout(resolve, 2200));
            const after = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const changed = (after?.data?.tracks ?? []).find((track) => {
              if (track.type !== "transcription") return false;
              if (beforeIds[track.id] === track.current_revision_id) return false;
              const revision = (track.revisions ?? []).find(
                (item) => item.id === track.current_revision_id,
              );
              return revision?.segments?.[0]?.text === "Smoke source autosave";
            });
            return { ok: Boolean(changed), error: changed ? undefined : "SOURCE_AUTOSAVE_NOT_APPLIED" };
          })()`,
        );
        check("core.correctionSourceAutosave", sourceAutosave?.ok === true, sourceAutosave?.error);

        const autosave = await evaluate<{ ok: boolean; error?: string }>(
          win,
          `(async () => {
            const before = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const beforeIds = Object.fromEntries(
              (before?.data?.tracks ?? []).map((track) => [track.id, track.current_revision_id]),
            );
            const textarea = document.querySelector('[data-testid="segment-translation"]');
            if (!textarea) return { ok: false, error: "NO_TEXTAREA" };
            const setter = Object.getOwnPropertyDescriptor(
              window.HTMLTextAreaElement.prototype,
              "value",
            ).set;
            setter.call(textarea, "Smoke autosave");
            textarea.dispatchEvent(new Event("input", { bubbles: true }));
            await new Promise((resolve) => setTimeout(resolve, 2200));
            const after = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const changed = (after?.data?.tracks ?? []).find((track) => {
              if (beforeIds[track.id] === track.current_revision_id) return false;
              const revision = (track.revisions ?? []).find(
                (item) => item.id === track.current_revision_id,
              );
              return revision?.segments?.[0]?.text === "Smoke autosave";
            });
            return { ok: Boolean(changed), error: changed ? undefined : "AUTOSAVE_NOT_APPLIED" };
          })()`,
        );
        check("core.correctionAutosave", autosave?.ok === true, autosave?.error);

        const leaveSave = await evaluate<{ ok: boolean; error?: string }>(
          win,
          `(async () => {
            const before = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const beforeIds = Object.fromEntries(
              (before?.data?.tracks ?? []).map((track) => [track.id, track.current_revision_id]),
            );
            const textarea = document.querySelector('[data-testid="segment-translation"]');
            const back = document.querySelector('.ym-back-button');
            if (!textarea || !back) return { ok: false, error: "NO_UI" };
            const setter = Object.getOwnPropertyDescriptor(
              window.HTMLTextAreaElement.prototype,
              "value",
            ).set;
            setter.call(textarea, "Smoke leave save");
            textarea.dispatchEvent(new Event("input", { bubbles: true }));
            back.click();
            await new Promise((resolve) => setTimeout(resolve, 1800));
            const after = await window.yaomu.api.getProject(${JSON.stringify(createdId)});
            const changed = (after?.data?.tracks ?? []).find((track) => {
              if (beforeIds[track.id] === track.current_revision_id) return false;
              const revision = (track.revisions ?? []).find(
                (item) => item.id === track.current_revision_id,
              );
              return revision?.segments?.[0]?.text === "Smoke leave save";
            });
            return { ok: Boolean(changed), error: changed ? undefined : "LEAVE_SAVE_NOT_APPLIED" };
          })()`,
        );
        check("core.correctionLeaveSave", leaveSave?.ok === true, leaveSave?.error);
      } else {
        check("core.correctionSourceAutosave", false, "correction UI not ready");
        check("core.correctionAutosave", false, "correction UI not ready");
        check("core.correctionLeaveSave", false, "correction UI not ready");
      }
    }

    // 真实 Core 批量导出：公开 API 构造 translate-only Run（复用源字幕轨），
    // 保存译文产生真实 delivery 门槛并放行，再由人工校正导出面板逐项提交。
    // 视频/音频没有当前 revision 的成功 08，Core 应以 REVIEW_REQUIRED 阻断。
    {
      const batchSrt = path.join(os.tmpdir(), `yaomu-smoke-batch-${process.pid}.srt`);
      const batchWav = path.join(os.tmpdir(), `yaomu-smoke-batch-${process.pid}.wav`);
      await writeFile(batchSrt, Buffer.from(
        "1\n00:00:00,000 --> 00:00:00,300\nBatch source\n\n2\n00:00:00,350 --> 00:00:00,600\nSecond line\n",
      ));
      await writeFile(batchWav, wavFixture(600));
      grantFixture(batchSrt);
      grantFixture(batchWav);
      const batchSetup = await evaluate<{
        ok: boolean;
        projectId: string;
        runId: string;
        revisionId: string;
        inputOk: boolean;
        error?: string;
      }>(
        win,
        `(async () => {
          const fail = (error) => ({ ok: false, projectId: "", runId: "", revisionId: "", inputOk: false, error });
          const project = await window.yaomu.api.createProject({ name: "冒烟批量导出" });
          if (!project?.ok) return fail("PROJECT:" + JSON.stringify(project?.error ?? {}));
          const projectId = project.data.id;
          const srtAssetResp = await window.yaomu.api.importAssets({
            project_id: projectId,
            file_grants: [${JSON.stringify(batchSrt)}],
            copy_mode: false,
          });
          const srtAsset = srtAssetResp?.data?.asset_ids?.[0];
          if (!srtAsset) return fail("SRT_ASSET:" + JSON.stringify(srtAssetResp?.error ?? {}));
          const wavAssetResp = await window.yaomu.api.importAssets({
            project_id: projectId,
            file_grants: [${JSON.stringify(batchWav)}],
            copy_mode: false,
          });
          const wavAsset = wavAssetResp?.data?.asset_ids?.[0];
          if (!wavAsset) return fail("WAV_ASSET:" + JSON.stringify(wavAssetResp?.error ?? {}));
          const sourceTrack = await window.yaomu.api.importSubtitleTrack({
            project_id: projectId,
            asset_id: srtAsset,
            language: "en",
            type: "transcription",
          });
          if (!sourceTrack?.ok) return fail("SOURCE_TRACK:" + JSON.stringify(sourceTrack?.error ?? {}));
          const run = await window.yaomu.api.createWorkflowRun({
            project_id: projectId,
            name: "冒烟批量导出",
            template: "custom_pipeline",
            features: ["translate"],
            source_language: "en",
            target_languages: ["zh"],
            input_revision_ids: [wavAsset],
            // 复用已导入源字幕轨：无需 ASR/分离/克隆模型即可创建带
            // 06 审核节点的真实 Run，从而产生真实交付门槛。
            reuse_subtitle_track_id: sourceTrack.data.id,
          });
          if (!run?.ok) return fail("RUN:" + JSON.stringify(run?.error ?? {}));
          // 必须在 Run 创建后导入译文轨：创建时已有译文会被冻结为导入译稿，
          // 06 节点直接完成、Run 立即终态，保存就不会产生交付门槛。
          const translationTrack = await window.yaomu.api.importSubtitleTrack({
            project_id: projectId,
            asset_id: srtAsset,
            language: "zh",
            type: "translation",
          });
          if (!translationTrack?.ok) return fail("TRANSLATION_TRACK:" + JSON.stringify(translationTrack?.error ?? {}));
          const track = translationTrack.data;
          const current = (track.revisions ?? []).find(
            (item) => item.id === track.current_revision_id,
          ) ?? (track.revisions ?? [])[0];
          const segments = (current?.segments ?? []).map((segment) => ({
            id: segment.id,
            start_ms: segment.start_ms,
            end_ms: segment.end_ms,
            text: segment.text,
          }));
          if (segments[0]) segments[0].text = "批量导出译文";
          const saved = await window.yaomu.api.saveSubtitle(track.id, {
            base_revision_id: track.current_revision_id,
            segments,
          });
          if (!saved?.ok) return fail("SAVE:" + JSON.stringify(saved?.error ?? {}));
          const reviews = await window.yaomu.api.listReviews(projectId, run.data.id);
          if (!reviews?.ok) return fail("REVIEWS:" + JSON.stringify(reviews?.error ?? {}));
          const gate = (reviews.data ?? []).find((item) =>
            item.type === "delivery" &&
            (item.current_bindings ?? []).some(
              (binding) =>
                binding.source_kind === "delivery_revision" &&
                binding.source_id === saved.data.id,
            ),
          );
          if (!gate) {
            return fail("DELIVERY_GATE:" + JSON.stringify({
              runStatus: run.data.status,
              saved: saved.data.id,
              gates: (reviews.data ?? []).map((item) => ({
                type: item.type,
                status: item.status,
                bindings: item.current_bindings,
              })),
            }));
          }
          const approved = await window.yaomu.api.approveReview(gate.id, {
            revision_digest: gate.revision_digest,
            type: "delivery",
          });
          return {
            ok: approved?.ok === true,
            projectId,
            runId: run.data.id,
            revisionId: saved.data.id,
            inputOk: saved.data.input_revision_id === sourceTrack.data.current_revision_id,
            error: approved?.ok
              ? undefined
              : "APPROVE:" + JSON.stringify(approved?.error ?? {}),
          };
        })()`,
      );
      const batchSkippedForModel =
        batchSetup?.ok !== true &&
        typeof batchSetup?.error === "string" &&
        batchSetup.error.includes("PREFLIGHT_STALE") &&
        !coreContext?.requireRunnerModelReady;
      if (batchSkippedForModel) {
        console.log(`[smoke] batch export: SKIP (model not ready) ${batchSetup.error}`);
      } else {
      check(
        "core.batchExportSetup",
        batchSetup?.ok === true && batchSetup?.inputOk === true,
        batchSetup?.error ?? `input=${batchSetup?.inputOk}`,
      );
      if (batchSetup?.ok) {
        await evaluate(win, `location.hash = "#/projects/${batchSetup.projectId}/correction"`);
        const pageReady = await waitFor(
          win,
          `!!document.querySelector('[data-testid="segment-translation"]')`,
        );
        if (pageReady) {
          const openReady = await waitFor(
            win,
            `(() => {
              const button = document.querySelector('[data-testid="mcs-export-open"]');
              return !!button && button.disabled !== true;
            })()`,
          );
          const opened = openReady && await evaluate<boolean>(
            win,
            `(() => {
              const button = document.querySelector('[data-testid="mcs-export-open"]');
              if (!button) return false;
              button.click();
              return true;
            })()`,
          );
          const panelReady = opened === true && await waitFor(
            win,
            `!!document.querySelector('[data-testid="mcs-export-panel"]') &&
             !document.querySelector('[data-testid="mcs-export-blocked"]') &&
             document.querySelector('[data-testid="mcs-export-kind-subtitle"] input')?.checked === true`,
          );
          const kinds = await evaluate<{
            panel: boolean;
            blockedText: string;
            subtitleChecked: boolean;
            videoDisabled: boolean;
            audioDisabled: boolean;
            startDisabled: boolean;
          }>(
            win,
            `(() => {
              const input = (kind) =>
                document.querySelector('[data-testid="mcs-export-kind-' + kind + '"] input');
              const start = document.querySelector('[data-testid="mcs-export-start"]');
              const blocked = document.querySelector('[data-testid="mcs-export-blocked"]');
              return {
                panel: !!document.querySelector('[data-testid="mcs-export-panel"]'),
                blockedText: blocked?.textContent?.trim() ?? "",
                subtitleChecked: input("subtitle")?.checked === true,
                videoDisabled: input("video")?.disabled === true,
                audioDisabled: input("audio")?.disabled === true,
                startDisabled: !start || start.disabled === true,
              };
            })()`,
          );
          check(
            "core.batchExportPanel",
            panelReady === true &&
              kinds?.panel === true &&
              kinds?.subtitleChecked === true &&
              kinds?.videoDisabled === true &&
              kinds?.audioDisabled === true &&
              kinds?.startDisabled === false,
            JSON.stringify(kinds ?? {}),
          );

          if (kinds && !kinds.startDisabled) {
            await evaluate(
              win,
              `document.querySelector('[data-testid="mcs-export-start"]').click()`,
            );
            const exported = await evaluate<{
              status: string;
              outputPath: string;
              outputDir: string;
              error?: string;
            }>(
              win,
              `(async () => {
                let record = null;
                for (let i = 0; i < 60; i += 1) {
                  const list = await window.yaomu.api.listExports(${JSON.stringify(batchSetup.projectId)});
                  record = (list?.data ?? []).find((item) => item.kind === "subtitle") ?? null;
                  if (record && ["SUCCEEDED", "FAILED", "CANCELED", "INTERRUPTED"].includes(record.status)) break;
                  await new Promise((resolve) => setTimeout(resolve, 250));
                }
                return {
                  status: record?.status ?? "MISSING",
                  outputPath: record?.output_path ?? "",
                  outputDir: record?.output_dir_grant ?? "",
                  error: record ? undefined : "EXPORT_NOT_FOUND",
                };
              })()`,
            );
            let batchFile = false;
            try {
              batchFile = exported?.outputPath
                ? (await stat(exported.outputPath)).isFile()
                : false;
            } catch {
              // 文件缺失由下方检查统一报告。
            }
            check(
              "core.batchExportSubtitle",
              exported?.status === "SUCCEEDED" && batchFile,
              exported?.error ?? `${exported?.status} ${exported?.outputPath}`,
            );
            if (exported?.outputPath) {
              const perItem = await waitFor(
                win,
                `(() => {
                  const record = document.querySelector('[data-testid="mcs-export-record-subtitle"]');
                  return !!record &&
                    record.textContent.includes(${JSON.stringify(exported.outputPath)}) &&
                    !record.querySelector('.export-panel__record-error');
                })()`,
              );
              check("core.batchExportPerItemStatus", perItem === true);
            } else {
              check("core.batchExportPerItemStatus", false, "no output path");
            }
          } else {
            check("core.batchExportSubtitle", false, "export panel not startable");
            check("core.batchExportPerItemStatus", false, "export panel not startable");
          }
        } else {
          check("core.batchExportPanel", false, "correction UI not ready");
          check("core.batchExportSubtitle", false, "correction UI not ready");
          check("core.batchExportPerItemStatus", false, "correction UI not ready");
        }

        // 真实 Core 门禁：没有当前 revision 的成功 08，视频/音频必须拒绝。
        const blocked = await evaluate<{ video: string; audio: string }>(
          win,
          `(async () => {
            const outputDir = await window.yaomu.api.getDefaultOutputDir();
            const call = async (kind, format) => {
              const result = await window.yaomu.api.export({
                project_id: ${JSON.stringify(batchSetup.projectId)},
                run_id: ${JSON.stringify(batchSetup.runId)},
                language: "zh",
                kind,
                format,
                output_dir_grant: outputDir,
                naming_template: "{project}_{language}_{timestamp}",
              });
              return result?.ok ? "OK" : (result?.error?.code ?? "UNKNOWN");
            };
            return { video: await call("video", "mp4_h264"), audio: await call("audio", "wav") };
          })()`,
        );
        check(
          "core.batchExportBlockedKinds",
          blocked?.video === "REVIEW_REQUIRED" && blocked?.audio === "REVIEW_REQUIRED",
          JSON.stringify(blocked ?? {}),
        );
      } else {
        check("core.batchExportPanel", false, "batch setup failed");
        check("core.batchExportSubtitle", false, "batch setup failed");
        check("core.batchExportPerItemStatus", false, "batch setup failed");
        check("core.batchExportBlockedKinds", false, "batch setup failed");
      }
      }
    }

    // 克隆工具页 + 真实 Core 的声源/字幕轨/克隆预检。
    await evaluate(win, `location.hash = "#/voice-clone"`);
    const cloneBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("高情感克隆配音") || document.body.innerText.includes("High-emotion clone dubbing") || document.body.innerText.includes("声音克隆") || document.body.innerText.includes("Voice clone"))`,
    );
    const cloneNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.voiceClonePage", cloneBound && cloneNoError === true);
    const cloneAuthorizationStep = await evaluate<boolean>(
      win,
      `document.body.innerText.includes("4 · 授权确认") || document.body.innerText.includes("4 · Authorization")`,
    );
    check("ui.voiceCloneNoAuthorizationStep", cloneAuthorizationStep === false);
    const cloneCapture = await capture(win, "voice-clone");
    check(
      "smoke.voiceCloneScreenshot",
      cloneCapture.screenshot !== undefined || cloneCapture.pdf !== undefined,
      cloneCapture.screenshot ?? cloneCapture.pdf,
    );

    if (createdId) {
      const audioPath = path.join(os.tmpdir(), `yaomu-smoke-clone-${process.pid}.wav`);
      const srtPath = path.join(os.tmpdir(), `yaomu-smoke-clone-${process.pid}.srt`);
      await writeFile(audioPath, Buffer.from("RIFF0000WAVEfmt "));
      await writeFile(srtPath, Buffer.from(
        "1\n00:00:00,000 --> 00:00:01,000\nHello\n\n2\n00:00:01,200 --> 00:00:02,000\nWorld\n",
      ));
      grantFixture(audioPath);
      grantFixture(srtPath);
      const clone = await evaluate<{
        importOk: boolean;
        tracksOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const audioImport = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(audioPath)}],
            copy_mode: false,
          });
          if (!audioImport?.ok) return { importOk: false, tracksOk: false, preflightOk: false, issues: -1, error: audioImport?.error?.code };
          const audioId = audioImport.data?.asset_ids?.[0] ?? audioImport.data?.assetIds?.[0];
          if (!audioId) return { importOk: false, tracksOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const srtImport = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(srtPath)}],
            copy_mode: false,
          });
          if (!srtImport?.ok) return { importOk: true, tracksOk: false, preflightOk: false, issues: -1, error: srtImport?.error?.code };
          const srtId = srtImport.data?.asset_ids?.[0] ?? srtImport.data?.assetIds?.[0];
          if (!srtId) return { importOk: true, tracksOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const sourceTrack = await window.yaomu.api.importSubtitleTrack({
            project_id: ${JSON.stringify(createdId)},
            asset_id: srtId,
            language: "en",
            type: "transcription",
          });
          if (!sourceTrack?.ok) return { importOk: true, tracksOk: false, preflightOk: false, issues: -1, error: sourceTrack?.error?.code };
          const translatedTrack = await window.yaomu.api.importSubtitleTrack({
            project_id: ${JSON.stringify(createdId)},
            asset_id: srtId,
            language: "zh",
            type: "translation",
          });
          if (!translatedTrack?.ok) return { importOk: true, tracksOk: false, preflightOk: false, issues: -1, error: translatedTrack?.error?.code };
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke clone",
            template: "tool_voice_clone",
            source_language: "en",
            target_languages: ["zh"],
            input_revision_ids: [audioId],
            validate_timestamps: true,
            adjust_duration: true,
          });
          return {
            importOk: true,
            tracksOk: true,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check(
        "core.voiceCloneTracks",
        clone?.importOk === true && clone?.tracksOk === true,
        clone?.error,
      );
      check(
        "core.voiceClonePreflight",
        clone?.preflightOk === true && (clone?.issues ?? -1) >= 0,
        clone?.error ?? `issues=${clone?.issues ?? "missing"}`,
      );
    }

    // 专属链路页 + 真实 Core 的模板持久化/预检。
    await evaluate(win, `location.hash = "#/tools/custom"`);
    const customBound = await waitFor(
      win,
      `!!document.querySelector('.wb-page') && (document.body.innerText.includes("专属链路") || document.body.innerText.includes("Custom pipeline"))`,
    );
    const customNoError = await evaluate<boolean>(
      win,
      "!document.querySelector('.state-box--error')",
    );
    check("ui.customPipelinePage", customBound && customNoError === true);

    if (createdId) {
      const videoPath = path.join(os.tmpdir(), `yaomu-smoke-custom-${process.pid}.mp4`);
      await writeFile(videoPath, Buffer.from("yaomu-smoke-video"));
      grantFixture(videoPath);
      const custom = await evaluate<{
        importOk: boolean;
        templateOk: boolean;
        preflightOk: boolean;
        issues: number;
        error?: string;
      }>(
        win,
        `(async () => {
          const imported = await window.yaomu.api.importAssets({
            project_id: ${JSON.stringify(createdId)},
            file_grants: [${JSON.stringify(videoPath)}],
            copy_mode: false,
          });
          if (!imported?.ok) return { importOk: false, templateOk: false, preflightOk: false, issues: -1, error: imported?.error?.code };
          const videoId = imported.data?.asset_ids?.[0] ?? imported.data?.assetIds?.[0];
          if (!videoId) return { importOk: false, templateOk: false, preflightOk: false, issues: -1, error: "ASSET_ID_MISSING" };
          const saved = await window.yaomu.api.saveCustomPipelineTemplates({
            items: [{
              id: "smoke-custom",
              name: "Smoke template",
              features: ["translate", "mix"],
              languages: ["zh"],
              source_language: "en",
              validate_timestamps: true,
              adjust_duration: true,
            }],
          });
          const listed = await window.yaomu.api.listCustomPipelineTemplates();
          const templateOk = saved?.ok === true && listed?.ok === true &&
            Array.isArray(listed?.data?.items) && listed.data.items.some((item) => item.id === "smoke-custom");
          const result = await window.yaomu.api.preflight({
            project_id: ${JSON.stringify(createdId)},
            name: "smoke custom",
            template: "custom_pipeline",
            features: ["separation", "translate", "mix"],
            source_language: "en",
            target_languages: ["zh"],
            input_revision_ids: [videoId],
          });
          return {
            importOk: true,
            templateOk,
            preflightOk: result?.ok === true,
            issues: Array.isArray(result?.data?.issues) ? result.data.issues.length : -1,
            error: result?.error?.code,
          };
        })()`,
      );
      check("core.customPipelineTemplates", custom?.templateOk === true, custom?.error);
      check(
        "core.customPipelinePreflight",
        custom?.importOk === true && custom?.preflightOk === true && (custom?.issues ?? -1) >= 0,
        custom?.error ?? `issues=${custom?.issues ?? "missing"}`,
      );
    }

    // 可选真实推理切片（YAOMU_SMOKE_INFERENCE=1）：在打包态应用中安装真实
    // ASR，导入真实语音 WAV，跑 04 提取 + 06 翻译，并校验文本。默认关闭，
    // 不影响现有 ready/no-core 冒烟与生产启动。
    if (process.env.YAOMU_SMOKE_INFERENCE === "1") {
      const inferenceWav = process.env.YAOMU_SMOKE_INFERENCE_WAV ?? "";
      const expectedText =
        process.env.YAOMU_SMOKE_INFERENCE_TEXT ??
        "Hello world. This is a real speech recognition test for YaoMu Studio.";
      const inferenceOut = process.env.YAOMU_SMOKE_INFERENCE_OUT ?? "";
      const wavStat = inferenceWav ? await stat(inferenceWav).catch(() => null) : null;
      if (!inferenceWav || !wavStat?.isFile()) {
        check("inference.input", false, inferenceWav || "missing YAOMU_SMOKE_INFERENCE_WAV");
        check("inference.asr", false, "input missing");
        check("inference.transcribe", false, "input missing");
        check("inference.translate", false, "input missing");
      } else {
        grantFixture(inferenceWav);
        const inference = await evaluate<{
          ok: boolean;
          error?: string;
          asrState?: string;
          asrEngine?: string;
          installPhase?: string;
          installRelease?: string;
          installSeconds?: number;
          projectId?: string;
          runId?: string;
          runStatus?: string;
          nodes?: { key?: string; status?: string }[];
          transcriptionText?: string;
          translationText?: string;
          keywordMatches?: string[];
          tasks?: { id?: string; kind?: string; node?: string; status?: string; error?: string }[];
          logs?: string[];
        }>(
          win,
          `(async () => {
            const api = window.yaomu.api;
            const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
            const asArray = (value) => Array.isArray(value) ? value : (value?.items ?? []);
            const logs = [];
            const log = (message) => logs.push(message);
            const call = async (name, fn) => {
              const response = await fn();
              if (!response?.ok) {
                throw new Error(name + ": " + JSON.stringify(response?.error ?? {}));
              }
              return response.data;
            };
            try {
              let system;
              for (let attempt = 0; attempt < 180; attempt += 1) {
                const response = await api.getSystem();
                if (response?.ok) { system = response.data; break; }
                await sleep(1000);
              }
              if (!system) throw new Error("CORE_NOT_READY");
              log("core ready");

              let capabilities = asArray(await call("capabilities", () => api.getCapabilities()));
              let asr = capabilities.find((item) => item.slot === "asr") ?? {};
              log("asr initial state=" + (asr.state ?? "missing"));

              let installPhase = "already-ready";
              let installRelease = "";
              let installSeconds = 0;
              if (String(asr.state).toLowerCase() !== "ready") {
                const plan = await call("plan-installation", () => api.planInstallation("asr"));
                installRelease = plan.release?.release_id ?? "";
                log("plan release=" + installRelease + " bytes=" + (plan.download_bytes ?? "?"));
                const created = await call("install", () =>
                  api.install({ plan_id: plan.plan_id, plan_hash: plan.plan_hash }),
                );
                let current = created;
                const startedAt = Date.now();
                for (let attempt = 0; attempt < 1800; attempt += 1) {
                  const list = asArray(await call("installations", () => api.getInstallations()));
                  current = list.find((item) => item.id === created.id) ?? current;
                  if (["done", "failed", "canceled"].includes(current.phase)) break;
                  await sleep(1000);
                }
                installPhase = current.phase;
                installSeconds = Math.round((Date.now() - startedAt) / 1000);
                log("installation phase=" + installPhase + " seconds=" + installSeconds);
                if (installPhase !== "done") {
                  throw new Error("INSTALL_" + installPhase + ": " + JSON.stringify(current.error ?? {}));
                }
                capabilities = asArray(await call("capabilities", () => api.getCapabilities()));
                asr = capabilities.find((item) => item.slot === "asr") ?? {};
                log("asr final state=" + (asr.state ?? "missing"));
                if (String(asr.state).toLowerCase() !== "ready") throw new Error("ASR_NOT_READY_" + (asr.state ?? "missing"));
              }

              const project = await call("create-project", () => api.createProject({ name: "真实推理验证" }));
              const imported = await call("import-assets", () =>
                api.importAssets({
                  project_id: project.id,
                  file_grants: [${JSON.stringify(inferenceWav)}],
                  copy_mode: false,
                }),
              );
              const assetId = imported.asset_ids?.[0] ?? imported.assetIds?.[0] ?? "";
              if (!assetId) throw new Error("ASSET_ID_MISSING");
              log("imported asset=" + assetId);

              const run = await call("create-run", () =>
                api.createWorkflowRun({
                  project_id: project.id,
                  name: "真实推理验证",
                  template: "custom_pipeline",
                  features: ["translate"],
                  source_language: "en",
                  target_languages: ["zh"],
                  input_revision_ids: [assetId],
                }),
              );
              log("run=" + run.id + " status=" + run.status);
              let currentRun = run;
              const runStartedAt = Date.now();
              for (let attempt = 0; attempt < 1800; attempt += 1) {
                currentRun = await call("get-run", () => api.getRun(run.id));
                const nodes = (currentRun.nodes ?? [])
                  .map((node) => node.key + ":" + node.status)
                  .join(",");
                if (attempt % 30 === 0) {
                  log("run status=" + currentRun.status + " nodes=" + nodes +
                    " seconds=" + Math.round((Date.now() - runStartedAt) / 1000));
                }
                if (["SUCCEEDED", "FAILED", "CANCELED", "INTERRUPTED"].includes(currentRun.status)) break;
                const translate = (currentRun.nodes ?? []).find((node) => node.key === "06_translate");
                if (translate && ["waiting_review", "done"].includes(translate.status)) break;
                await sleep(1000);
              }
              log("run final status=" + currentRun.status);
              await sleep(2000);
              const detail = await call("get-project", () => api.getProject(project.id));
              const tracks = detail.tracks ?? [];
              const textOf = (track) => {
                if (!track) return "";
                const revision =
                  (track.revisions ?? []).find((item) => item.id === track.current_revision_id) ??
                  (track.revisions ?? [])[0];
                return (revision?.segments ?? []).map((segment) => segment.text).join(" ").trim();
              };
              const transcription = tracks.find((track) => track.type === "transcription");
              const translation = tracks.find(
                (track) => track.type === "translation" && track.language === "zh",
              );
              const transcriptionText = textOf(transcription);
              const translationText = textOf(translation);
              const normalized = transcriptionText.toLowerCase().replace(/[^a-z0-9\\s]/g, " ");
              const keywords = ["hello", "world", "speech", "test", "studio"];
              const keywordMatches = keywords.filter((word) => normalized.includes(word));
              let tasks = [];
              try {
                tasks = asArray(await call("list-tasks", () => api.listTasks({ run_id: run.id })));
              } catch (error) {
                log("list tasks failed: " + String(error));
              }
              return {
                ok: transcriptionText.length > 0 && translationText.length > 0,
                asrState: asr.state,
                asrEngine: asr.engine_name ?? "",
                installPhase,
                installRelease,
                installSeconds,
                projectId: project.id,
                runId: run.id,
                runStatus: currentRun.status,
                nodes: currentRun.nodes,
                transcriptionText,
                translationText,
                keywordMatches,
                tasks: tasks.map((task) => ({
                  id: task.id,
                  kind: task.kind,
                  node: task.node_key,
                  status: task.status,
                  error: task.error?.code ?? task.error_code ?? "",
                })),
                logs,
              };
            } catch (error) {
              return { ok: false, error: String(error), logs };
            }
          })()`,
        );
        if (inferenceOut) {
          await writeFile(inferenceOut, `${JSON.stringify(inference ?? { ok: false }, null, 2)}\n`).catch(
            () => undefined,
          );
        }
        check(
          "inference.asr",
          inference?.installPhase === "done" || inference?.installPhase === "already-ready",
          inference?.error ?? `${inference?.installPhase ?? "missing"} ${inference?.installRelease ?? ""}`,
        );
        check(
          "inference.transcribe",
          (inference?.transcriptionText ?? "").length > 0 &&
            (inference?.keywordMatches ?? []).length >= 3,
          inference?.error ??
            `keywords=${(inference?.keywordMatches ?? []).join(",")} text=${(inference?.transcriptionText ?? "").slice(0, 160)}`,
        );
        check(
          "inference.translate",
          (inference?.translationText ?? "").length > 0,
          inference?.error ?? (inference?.translationText ?? "").slice(0, 160),
        );
        check(
          "inference.run",
          ["SUCCEEDED", "RUNNING"].includes(inference?.runStatus ?? "") ||
            (inference?.nodes ?? []).some(
              (node) =>
                node.key === "06_translate" && ["waiting_review", "done"].includes(node.status ?? ""),
            ),
          inference?.error ??
            `${inference?.runStatus ?? "missing"} ${(inference?.nodes ?? [])
              .map((node) => `${node.key}:${node.status}`)
              .join(",")}`,
        );
      }
    }

    // 回到工作台截图（真实项目已渲染）。
    await evaluate(win, `location.hash = "#/"`);
    await waitFor(win, `document.body.innerText.includes(${JSON.stringify(SMOKE_PROJECT_NAME)})`);
    ({ screenshot, pdf, error: captureError } = await capture(win, mode));
  }

  // Windows 严格冒烟：必须证明本次 Core 自己接受了包内模型侧 Runner。
  // HTTP ready、单独 hello 或外部配置都不算；非 Windows 明确 not-run。
  let coreRunnerEvidence: SmokeCoreRunnerEvidence | undefined;
  if (mode === "ready") {
    const bundled = coreContext?.getBundledRunner() ?? null;
    const source = coreContext?.hasExternalWslRunner()
      ? "external"
      : bundled
        ? "bundled"
        : "unavailable";
    if (coreContext?.requireRunnerModelReady) {
      const observed = await observeRunnerEvidence(
        coreContext,
        coreContext.evidenceTimeoutMs ?? 90_000,
      );
      const ok = observed.ok && source === "bundled";
      coreRunnerEvidence = {
        required: true,
        ok,
        detail: observed.ok && source !== "bundled" ? `source-${source}` : observed.detail,
        source,
        ...(bundled
          ? {
              runnerVersion: bundled.runnerVersion,
              manifestDigest: bundled.manifestDigest,
            }
          : {}),
      };
      check("core.runnerModelReady", ok, coreRunnerEvidence.detail);
    } else {
      coreRunnerEvidence = { required: false, ok: false, detail: "not-run", source };
    }
  }

  check("smoke.screenshot", screenshot !== undefined || pdf !== undefined, screenshot ?? pdf);

  const ok = checks.every((item) => item.ok);
  const result: SmokeResult = {
    mode,
    ok,
    checks,
    consoleErrors,
    securityProbes: { ipcSources: ipcSourceProbe },
  };
  if (coreRunnerEvidence) result.coreRunnerEvidence = coreRunnerEvidence;
  if (screenshot) result.screenshot = screenshot;
  if (pipelineScreenshot) result.pipelineScreenshot = pipelineScreenshot;
  if (pdf) result.pdf = pdf;
  if (captureError) result.captureError = captureError;
  // 单行 JSON 结果，便于脚本解析；不输出令牌或路径以外的敏感值。
  console.log(`SMOKE_RESULT ${JSON.stringify(result)}`);
  return ok ? 0 : 1;
}

/**
 * 开发专用高权限 IPC 来源探针：使用 nodeIntegration + 非隔离窗口发送真实 IPC。
 * 只在未打包运行（app.isPackaged === false）由 smoke.ts 动态导入；发行包通过
 * electron-builder files 负 glob 排除本模块，不得随包分发。
 */
import { mkdir, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import type { DesktopAuthorization } from "./ipc/authorization";
import type { BridgeEnvelope } from "./smoke";

/** 隔离测试窗口拥有 Node 以发送真实 IPC；生产窗口仍使用 sandbox + 无 Node。 */
export async function probeIpcSources(
  authorization: DesktopAuthorization,
  check: (name: string, ok: boolean, detail?: string) => void,
): Promise<void> {
  const { BrowserWindow: ProbeWindow } = await import("electron");
  const dir = path.join(os.tmpdir(), `yaomu-ipc-probe-${process.pid}`);
  await mkdir(dir, { recursive: true });
  const parentFile = path.join(dir, "parent.html");
  const childFile = path.join(dir, "child.html");
  await writeFile(parentFile, '<!doctype html><iframe src="child.html"></iframe>');
  await writeFile(childFile, "<!doctype html><p>IPC source probe</p>");
  const probe = new ProbeWindow({
    show: false,
    webPreferences: {
      nodeIntegration: true,
      nodeIntegrationInSubFrames: true,
      contextIsolation: false,
      sandbox: false,
    },
  });
  try {
    const registeredURL = pathToFileURL(parentFile).href;
    authorization.registerWindow(probe.webContents, registeredURL);
    await probe.loadFile(parentFile);
    const top = await probe.webContents.executeJavaScript(
      `require("electron").ipcRenderer.invoke("yaomu:api:invoke", "getSystem", [])`,
    ) as BridgeEnvelope<unknown>;
    check(
      "security.trustedTopFrame",
      top.ok || top.error?.code === "CORE_NOT_READY",
      top.ok
        ? "trusted"
        : `${top.error?.code ?? "unknown"} registered=${registeredURL} ` +
          `url=${probe.webContents.getURL()} frame=${probe.webContents.mainFrame.url}`,
    );
    const child = probe.webContents.mainFrame.frames[0];
    const rejected = child ? await child.executeJavaScript(`(async () => {
      const ipc = require("electron").ipcRenderer;
      const methods = [
        ["yaomu:api:invoke", "getSystem", []],
        ["yaomu:files:select-files"],
        ["yaomu:files:select-directory"],
        ["yaomu:files:select-directory-files"],
        ["yaomu:files:default-output-dir"],
        ["yaomu:files:open-path", ${JSON.stringify(dir)}],
        ["yaomu:assets:save", { assetId: "asset-probe" }],
        ["yaomu:settings:set-close-behavior", "quit"],
        ["yaomu:notify", { title: "probe", body: "probe" }],
      ];
      const outcomes = [];
      for (const [channel, ...args] of methods) {
        try {
          const result = await ipc.invoke(channel, ...args);
          outcomes.push(result?.error?.code === "LOCAL_ACCESS_DENIED");
        } catch (error) {
          outcomes.push(String(error).includes("LOCAL_ACCESS_DENIED"));
        }
      }
      outcomes.push(ipc.sendSync("yaomu:files:register-drop", ${JSON.stringify(childFile)}) === "");
      return outcomes;
    })()`) as boolean[] : [];
    check("security.realSubframeDenied", rejected.length === 10 && rejected.every(Boolean),
      `${rejected.filter(Boolean).length}/${rejected.length} channels denied`);

    authorization.removeWindow(probe.webContents);
    const unregistered = await probe.webContents.executeJavaScript(
      `require("electron").ipcRenderer.invoke("yaomu:api:invoke", "getSystem", [])`,
    ) as BridgeEnvelope<unknown>;
    check("security.unregisteredWindowDenied", unregistered.error?.code === "LOCAL_ACCESS_DENIED");
    authorization.registerWindow(probe.webContents, pathToFileURL(parentFile).href);
    await probe.loadFile(childFile);
    const wrongURL = await probe.webContents.executeJavaScript(
      `require("electron").ipcRenderer.invoke("yaomu:api:invoke", "getSystem", [])`,
    ) as BridgeEnvelope<unknown>;
    check("security.wrongDocumentDenied", wrongURL.error?.code === "LOCAL_ACCESS_DENIED");
  } finally {
    authorization.removeWindow(probe.webContents);
    probe.destroy();
  }
}

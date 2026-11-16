/**
 * 系统托盘：仅在“关闭行为 = 最小化到托盘”时按需创建。
 *
 * - 主进程是托盘生命周期的唯一权威；Renderer 只通过白名单 IPC 同步设置。
 * - 菜单只保留“显示主窗口 / 退出”，退出复用 `app.quit()`，不新增第二套退出流程。
 * - 创建失败只记录日志并返回 `false`，由调用方回退到 quit 语义，避免窗口隐藏后无法恢复。
 * - 托盘不是安全边界：菜单动作只操作已有窗口与既有退出流程，不暴露任意路径/命令。
 */
import { Menu, Tray, nativeImage } from "electron";
import type { NativeImage } from "electron";
import {
  TRAY_COLOR_PNG_1X,
  TRAY_COLOR_PNG_2X,
  TRAY_TEMPLATE_PNG_1X,
  TRAY_TEMPLATE_PNG_2X,
} from "./tray-icon";

export interface TrayMenuLabels {
  show: string;
  quit: string;
}

export interface CreateTrayOptions {
  onShow: () => void;
  onQuit: () => void;
  /** `app.getLocale()`；中文环境显示中文菜单，其余回退英文。 */
  locale?: string;
  log?: (line: string) => void;
}

let tray: Tray | null = null;

/** 托盘菜单文案；完整 26 语言本地化留给 i18n 子任务。 */
export function trayMenuLabels(locale?: string): TrayMenuLabels {
  const chinese = (locale ?? "").toLowerCase().startsWith("zh");
  return chinese
    ? { show: "显示主窗口", quit: "退出" }
    : { show: "Show Main Window", quit: "Quit" };
}

function trayImage(): NativeImage {
  const template = process.platform === "darwin";
  // 16pt 逻辑尺寸 + 2x 表示：避免 macOS 把 32px 当作 32pt 显得过大，同时保留 Retina 清晰度。
  const image = nativeImage.createFromBuffer(
    Buffer.from(template ? TRAY_TEMPLATE_PNG_1X : TRAY_COLOR_PNG_1X, "base64"),
  );
  image.addRepresentation({
    scaleFactor: 2,
    dataURL: `data:image/png;base64,${template ? TRAY_TEMPLATE_PNG_2X : TRAY_COLOR_PNG_2X}`,
  });
  if (image.isEmpty()) throw new Error("TRAY_ICON_EMPTY");
  if (template) image.setTemplateImage(true);
  return image;
}

/**
 * 创建托盘（幂等）。成功返回 `true`；失败只记录日志并返回 `false`。
 */
export function createTray(options: CreateTrayOptions): boolean {
  if (tray) return true;
  let instance: Tray | null = null;
  try {
    instance = new Tray(trayImage());
    const labels = trayMenuLabels(options.locale);
    instance.setToolTip("YaomuStudio");
    instance.setContextMenu(
      Menu.buildFromTemplate([
        { label: labels.show, click: () => options.onShow() },
        { type: "separator" },
        { label: labels.quit, click: () => options.onQuit() },
      ]),
    );
    // Windows/Linux 左键点击直接恢复窗口；macOS 设置了菜单后点击会弹出菜单，
    // 用户在菜单里选择“显示主窗口”。两种交互都覆盖。
    instance.on("click", () => options.onShow());
    tray = instance;
    return true;
  } catch (error) {
    // 构造成功但菜单/事件注册失败时回收原生托盘，避免留下无法管理的图标。
    try {
      instance?.destroy();
    } catch {
      // 回收失败不改变“本次运行回退 quit”的结果。
    }
    options.log?.(
      `托盘创建失败：${error instanceof Error ? error.message : String(error)}`,
    );
    tray = null;
    return false;
  }
}

/** 销毁托盘（幂等）；应用退出或关闭行为切回 `quit` 时调用。 */
export function destroyTray(): void {
  if (!tray) return;
  const instance = tray;
  tray = null;
  try {
    instance.destroy();
  } catch {
    // 已销毁或平台回收失败；不阻断退出。
  }
}

/** 供测试/诊断确认托盘是否存在（不触发创建）。 */
export function isTrayCreated(): boolean {
  return tray !== null;
}

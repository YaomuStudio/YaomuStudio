/** Main 持有的窗口来源与路径授权；本模块不加载 Electron，便于定向验证。 */
import fs from "node:fs";
import path from "node:path";

export class DesktopAccessDenied extends Error {
  constructor() {
    super("LOCAL_ACCESS_DENIED");
  }
}

export interface SenderContents {
  readonly id: number;
  readonly mainFrame: { readonly url: string };
  getURL(): string;
  isDestroyed(): boolean;
}

export interface IpcSource {
  readonly sender: SenderContents;
  readonly senderFrame: { readonly url: string } | null;
}

/**
 * 只解码 RFC 3986 unreserved 字符的百分号编码，用于判断两个 URL 是否指向同一文档。
 * Node 的 pathToFileURL 会把 `~` 写成 `%7E`（Windows 8.3 短路径如 RUNNER~1），
 * Chromium 的 getURL() 保留字面量，直接比较 href 会把同一文档判成不同来源。
 * 保留字符（如 %2F）不解码，避免放行子路径或近义主机。
 */
function decodeUnreserved(value: string): string {
  return value.replace(/%([0-9A-Fa-f]{2})/g, (encoded, hex: string) => {
    const char = String.fromCharCode(parseInt(hex, 16));
    return /[A-Za-z0-9\-._~]/.test(char) ? char : encoded;
  });
}

/** 只接受同一应用文档的 hash 路由；拒绝同前缀主机、其他文件和子路径。 */
export function isApplicationURL(candidate: string, applicationURL: string): boolean {
  try {
    const current = new URL(candidate);
    const expected = new URL(applicationURL);
    if (!["file:", "http:", "https:"].includes(expected.protocol)) return false;
    if (current.username || current.password || expected.username || expected.password) return false;
    current.hash = "";
    expected.hash = "";
    return decodeUnreserved(current.href) === decodeUnreserved(expected.href);
  } catch {
    return false;
  }
}

interface PathIdentity {
  canonical: string;
  device: number;
  inode: number;
  directory: boolean;
}

function absolutePath(value: unknown): string {
  if (
    typeof value !== "string" || value.length === 0 || value.length > 4_096 ||
    value.includes("\u0000") || !path.isAbsolute(value)
  ) {
    throw new DesktopAccessDenied();
  }
  return path.normalize(value);
}

function identity(target: string): PathIdentity {
  try {
    const canonical = fs.realpathSync.native(target);
    const stat = fs.statSync(canonical);
    if (!stat.isFile() && !stat.isDirectory()) throw new DesktopAccessDenied();
    return { canonical, device: stat.dev, inode: stat.ino, directory: stat.isDirectory() };
  } catch {
    throw new DesktopAccessDenied();
  }
}

function sameIdentity(left: PathIdentity, right: PathIdentity): boolean {
  return left.canonical === right.canonical && left.device === right.device &&
    left.inode === right.inode && left.directory === right.directory;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

/** exact-path grants：选择目录不会自动授权导入其中任意文件。 */
export class WindowPathGrants {
  private readonly inputFiles = new Map<string, PathIdentity>();
  private readonly outputDirectories = new Map<string, PathIdentity>();
  private readonly openPaths = new Map<string, PathIdentity>();

  grantInputFiles(values: readonly string[]): string[] {
    const entries = values.map((value) => {
      const target = absolutePath(value);
      const snapshot = identity(target);
      if (snapshot.directory) throw new DesktopAccessDenied();
      return { target, snapshot };
    });
    for (const { target, snapshot } of entries) {
      this.inputFiles.set(target, snapshot);
      this.openPaths.set(target, snapshot);
    }
    return entries.map(({ target }) => target);
  }

  grantOutputDirectory(value: string): string {
    const target = absolutePath(value);
    const snapshot = identity(target);
    if (!snapshot.directory) throw new DesktopAccessDenied();
    this.outputDirectories.set(target, snapshot);
    this.openPaths.set(target, snapshot);
    return target;
  }

  grantOpenPath(value: string): string {
    const target = absolutePath(value);
    this.openPaths.set(target, identity(target));
    return target;
  }

  assertOpenPath(value: unknown): string {
    return this.assertGranted(this.openPaths, value);
  }

  assertApiPaths(method: string, args: readonly unknown[]): void {
    const input = record(args[0]);
    if (method === "importAssets") {
      const files = input?.file_grants;
      if (!Array.isArray(files) || files.length === 0) {
        throw new DesktopAccessDenied();
      }
      for (const file of files) this.assertGranted(this.inputFiles, file);
    } else if (method === "export") {
      this.assertGranted(this.outputDirectories, input?.output_dir_grant);
    }
  }

  /** 只从明确的 Core 响应字段登记可打开目录，不从 Renderer 参数推导权限。 */
  rememberCorePaths(method: string, data: unknown): void {
    const directories: unknown[] = [];
    if (method === "getProject" || method === "createProject") {
      directories.push(record(data)?.directory);
    } else if (method === "getSnapshot") {
      directories.push(record(record(data)?.project)?.directory);
    } else if (method === "listProjects" && Array.isArray(data)) {
      directories.push(...data.map((project) => record(project)?.directory));
    } else if (method === "export" || method === "listExports") {
      const exports = Array.isArray(data) ? data : [data];
      for (const item of exports) {
        const output = record(item)?.output_path;
        if (typeof output === "string" && path.isAbsolute(output)) directories.push(path.dirname(output));
      }
    }
    for (const directory of directories) {
      if (typeof directory !== "string") continue;
      try {
        const target = absolutePath(directory);
        const snapshot = identity(target);
        if (snapshot.directory) this.openPaths.set(target, snapshot);
      } catch {
        // 缺失/损坏目录不能扩大权限；不把可选展示字段变成业务请求失败。
      }
    }
  }

  private assertGranted(grants: Map<string, PathIdentity>, value: unknown): string {
    const target = absolutePath(value);
    const granted = grants.get(target);
    if (!granted || !sameIdentity(granted, identity(target))) throw new DesktopAccessDenied();
    return target;
  }
}

export interface AuthorizedWindow {
  readonly sender: SenderContents;
  readonly applicationURL: string;
  readonly paths: WindowPathGrants;
}

/** 只由 Main 为实际创建的应用窗口登记，不接受 Renderer 注册窗口或路径授权。 */
export class DesktopAuthorization {
  private readonly windows = new Map<number, AuthorizedWindow>();

  registerWindow(sender: SenderContents, applicationURL: string): void {
    this.windows.set(sender.id, { sender, applicationURL, paths: new WindowPathGrants() });
  }

  resetWindow(sender: SenderContents): void {
    const current = this.windows.get(sender.id);
    if (current?.sender === sender) this.registerWindow(sender, current.applicationURL);
  }

  removeWindow(sender: SenderContents): void {
    if (this.windows.get(sender.id)?.sender === sender) this.windows.delete(sender.id);
  }

  isTrustedContents(sender: SenderContents): boolean {
    try {
      const current = this.windows.get(sender.id);
      return current?.sender === sender && !sender.isDestroyed() &&
        isApplicationURL(sender.getURL(), current.applicationURL) &&
        isApplicationURL(sender.mainFrame.url, current.applicationURL);
    } catch {
      return false;
    }
  }

  requireSender(source: IpcSource): AuthorizedWindow {
    if (
      !this.isTrustedContents(source.sender) || !source.senderFrame ||
      source.senderFrame !== source.sender.mainFrame
    ) {
      throw new DesktopAccessDenied();
    }
    return this.windows.get(source.sender.id)!;
  }

  assertCurrent(source: IpcSource, authority: AuthorizedWindow): void {
    if (this.requireSender(source) !== authority) throw new DesktopAccessDenied();
  }

  /** Main 的测试夹具与原生选择器共用授权模型；没有对应 Renderer API。 */
  requireWindow(sender: SenderContents): AuthorizedWindow {
    return this.requireSender({ sender, senderFrame: sender.mainFrame });
  }
}

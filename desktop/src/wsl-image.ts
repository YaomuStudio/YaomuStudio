/**
 * 缺失 yaomu 发行版时的根文件系统准备。
 *
 * 只在 Windows、且目标发行版名就是 yaomu 时运行。WSL 功能本身不自动安装
 * （不调用 wsl --install），也不调用 wsl --shutdown。导入使用应用数据目录，
 * 若导入改写了用户原来的默认发行版，再改回去。
 *
 * 分发地址固定为 HTTPS，当前没有 SHA-256。不能把这次导入当成已签名准入。
 * 探测函数仍只检查，不在这里之外安装。
 *
 * 取消或无法确认自有 wsl.exe 退出时抛出，调用方不得继续启动 Core。
 * 普通下载/导入失败只返回失败状态，Core 仍可进入编辑与诊断。
 */
import { spawn, type ChildProcess } from "node:child_process";
import { createWriteStream, type WriteStream } from "node:fs";
import fs from "node:fs/promises";
import https from "node:https";
import path from "node:path";
import type { Readable } from "node:stream";
import { abortError, isAbortError } from "./bootstrap";

/** 产品确认的 yaomu 根文件系统。不接受调用方改地址。 */
export const YAOMU_WSL_IMAGE_URL =
  "https://static-pro.guangdianyun.tv/static/common/yaomu-ubuntu.tar.gz";

const DISTRO_NAME = "yaomu";
const ARCHIVE_NAME = "yaomu-ubuntu.tar.gz";
const LIST_TIMEOUT_MS = 20_000;
const IMPORT_TIMEOUT_MS = 45 * 60 * 1000;
const DEFAULT_TIMEOUT_MS = 20_000;
const STOP_GRACE_MS = 2_000;
const LIST_OUTPUT_LIMIT = 64 * 1024;
const MAX_IMAGE_BYTES = 32 * 1024 * 1024 * 1024;
const DOWNLOAD_STALL_MS = 120_000;
const DISK_MARGIN_BYTES = 256 * 1024 * 1024;
const MAX_REDIRECTS = 5;
const PROGRESS_STEP_BYTES = 32 * 1024 * 1024;

const HEADER_TOKENS = new Set(["NAME", "STATE", "VERSION"]);

export type WslImageStatus =
  | { status: "skipped"; reason: "not_windows" | "distro_present" | "distro_override" | "wsl_unavailable" }
  | { status: "imported" }
  | {
      status: "failed";
      reason: "list_failed" | "download_failed" | "insufficient_disk" | "import_failed";
    };

export interface WslImageHttpRequest {
  url: URL;
  headers: Record<string, string>;
  signal?: AbortSignal;
}

export interface WslImageHttpResponse {
  statusCode: number;
  headers: NodeJS.Dict<string | string[]>;
  body: Readable;
  abort: () => void;
}

export interface EnsureWslImageOptions {
  /** 调用方/测试注入的平台；默认 process.platform。 */
  platform?: NodeJS.Platform;
  /** 读取 YAOMU_WSL_DISTRO / YAOMU_WSL_EXE；默认 process.env。 */
  env?: NodeJS.ProcessEnv;
  signal?: AbortSignal;
  /** spawn 注入（测试用）；生产只启动配置的 wsl.exe。 */
  spawnImpl?: typeof spawn;
  /** Electron userData。发行版目录和下载缓存都在其下。 */
  userDataDir: string;
  log?: (line: string) => void;
  /** 替换整个下载；测试不访问网络。默认下载固定 HTTPS 地址。 */
  downloadImpl?: (destination: string, signal?: AbortSignal) => Promise<void>;
  /** 替换 HTTPS 请求；只供下载器测试。 */
  requestImpl?: (request: WslImageHttpRequest) => Promise<WslImageHttpResponse>;
  /** 替换磁盘剩余空间读取。返回 null 表示未知，不因此拒绝下载。 */
  freeBytesImpl?: (directory: string) => Promise<number | null>;
  listTimeoutMs?: number;
  importTimeoutMs?: number;
  stopGraceMs?: number;
}

/**
 * 与 RuntimeController 约定同名：无法确认自有 wsl.exe 退出。
 * 不从 wsl.ts 引入，避免这个模块在测试里拉起 Electron。
 */
export class WslImageCleanupError extends Error {
  readonly reason: "timeout" | "abort" | "error";

  constructor(reason: "timeout" | "abort" | "error") {
    super(`WSL 镜像准备无法确认自有进程退出（${reason}）`);
    this.name = "WslProbeCleanupError";
    this.reason = reason;
  }
}

class WslImageTransferError extends Error {
  readonly reason: "disk" | "download";

  constructor(reason: "disk" | "download") {
    super(reason);
    this.name = "WslImageTransferError";
    this.reason = reason;
  }
}

export function decodeWslOutput(buffer: Buffer): string {
  if (buffer.length === 0) return "";
  if (buffer.length >= 2 && buffer[0] === 0xff && buffer[1] === 0xfe) {
    return buffer.subarray(2).toString("utf16le").replace(/\0/g, "");
  }
  let pairs = 0;
  let nulls = 0;
  const sample = Math.min(buffer.length, 64);
  for (let index = 0; index + 1 < sample; index += 2) {
    pairs += 1;
    if (buffer[index + 1] === 0) nulls += 1;
  }
  if (pairs >= 2 && nulls * 2 >= pairs) {
    return buffer.toString("utf16le").replace(/\0/g, "");
  }
  return buffer.toString("utf8").replace(/^\uFEFF/, "");
}

function isSafeDistroName(value: string): boolean {
  if (!value || value.length > 64 || value.startsWith("-") || HEADER_TOKENS.has(value)) {
    return false;
  }
  return /^[A-Za-z0-9._-]+$/.test(value);
}

/** 安静列表里的发行版名。表头和非法名字丢弃。 */
export function parseWslDistroNames(text: string): string[] {
  const names: string[] = [];
  for (const line of text.split(/\r?\n/)) {
    const name = line.trim();
    if (!isSafeDistroName(name)) continue;
    names.push(name);
  }
  return names;
}

/** 详细列表里带 * 的默认发行版。解析不到时返回 null，不猜测。 */
export function parseWslDefaultDistro(text: string): string | null {
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed.startsWith("*")) continue;
    const name = trimmed.slice(1).trim().split(/\s+/)[0] ?? "";
    if (!isSafeDistroName(name)) continue;
    return name;
  }
  return null;
}

/** 列表失败时的固定分类。先认“WSL 未安装”，避免把不能导入误当成空机器。 */
export function classifyWslListFailure(text: string): "none" | "unavailable" | "unknown" {
  const normalized = text.replace(/\0/g, "").toLowerCase();
  if (
    normalized.includes("not installed") ||
    normalized.includes("optional component") ||
    normalized.includes("0x8007019e") ||
    normalized.includes("0x80370102") ||
    normalized.includes("尚未安装") ||
    normalized.includes("可选组件") ||
    normalized.includes("虚拟化") ||
    normalized.includes("virtualization")
  ) {
    return "unavailable";
  }
  if (
    normalized.includes("no installed distributions") ||
    normalized.includes("没有已安装的分发") ||
    normalized.includes("没有已安装的发行")
  ) {
    return "none";
  }
  return "unknown";
}

type CommandResult =
  | { kind: "exit"; code: number | null; output: Buffer; overflow: boolean }
  | { kind: "spawn-error" }
  | { kind: "timeout" };

function wslCommand(env: NodeJS.ProcessEnv): string {
  const value = env.YAOMU_WSL_EXE?.trim() || "wsl.exe";
  if (!value || value.startsWith("-") || value.includes("\0")) return "wsl.exe";
  return value;
}

async function waitUntil(check: () => boolean, ms: number): Promise<boolean> {
  if (check()) return true;
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value: boolean): void => {
      if (settled) return;
      settled = true;
      clearInterval(poll);
      clearTimeout(timer);
      resolve(value);
    };
    const timer = setTimeout(() => finish(check()), ms);
    const poll = setInterval(() => {
      if (check()) finish(true);
    }, 10);
  });
}

async function stopChild(
  child: ChildProcess,
  exited: () => boolean,
  graceMs: number,
): Promise<boolean> {
  if (exited()) return true;
  try {
    child.kill();
  } catch {
    // 已退出或无法发信号；下面只看退出证据。
  }
  if (await waitUntil(exited, graceMs)) return true;
  try {
    child.kill("SIGKILL");
  } catch {
    // 同上。
  }
  return waitUntil(exited, graceMs);
}

function consumeOutput(
  stream: NodeJS.ReadableStream | null,
  sink: { chunks: Buffer[]; size: number; overflow: boolean },
  limit: number,
): void {
  if (!stream) return;
  stream.on("data", (chunk: Buffer | string) => {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    if (sink.size < limit) {
      const room = limit - sink.size;
      sink.chunks.push(buffer.subarray(0, room));
      sink.size += Math.min(buffer.length, room);
    }
    if (buffer.length > 0 && sink.size >= limit) sink.overflow = true;
  });
}

async function runWslCommand(options: {
  command: string;
  args: string[];
  timeoutMs: number;
  spawnImpl: typeof spawn;
  signal?: AbortSignal;
  stopGraceMs: number;
  collect: boolean;
}): Promise<CommandResult> {
  if (options.signal?.aborted) throw abortError("WSL 镜像准备已取消");

  let child: ChildProcess;
  try {
    child = options.spawnImpl(options.command, options.args, {
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch {
    return { kind: "spawn-error" };
  }

  const sink = { chunks: [] as Buffer[], size: 0, overflow: false };
  const limit = options.collect ? LIST_OUTPUT_LIMIT : 0;
  if (options.collect) {
    consumeOutput(child.stdout, sink, limit);
    consumeOutput(child.stderr, sink, limit);
  } else {
    child.stdout?.resume();
    child.stderr?.resume();
  }

  let exited = child.exitCode !== null || child.signalCode !== null;
  let settled = false;
  let resolveClose!: (result: { kind: "close"; code: number | null } | { kind: "spawn-error" }) => void;
  const closed = new Promise<{ kind: "close"; code: number | null } | { kind: "spawn-error" }>(
    (resolve) => {
      resolveClose = resolve;
    },
  );
  const finish = (result: { kind: "close"; code: number | null } | { kind: "spawn-error" }): void => {
    if (settled) return;
    settled = true;
    resolveClose(result);
  };
  child.on("error", () => {
    if (child.pid === undefined) finish({ kind: "spawn-error" });
  });
  child.once("exit", () => {
    exited = true;
  });
  child.once("close", (code) => {
    exited = true;
    finish({ kind: "close", code });
  });

  const killOnParentExit = (): void => {
    try {
      child.kill("SIGKILL");
    } catch {
      // 进程已退出。
    }
  };
  process.once("exit", killOnParentExit);
  const releaseParentFallback = (): void => {
    process.removeListener("exit", killOnParentExit);
  };

  let timer: NodeJS.Timeout | undefined;
  let onAbort: (() => void) | undefined;
  const budget = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => resolve("timeout"), options.timeoutMs);
  });
  const aborted = options.signal
    ? new Promise<"abort">((resolve) => {
        if (options.signal?.aborted) {
          resolve("abort");
          return;
        }
        onAbort = () => resolve("abort");
        options.signal?.addEventListener("abort", onAbort, { once: true });
      })
    : null;

  try {
    const first = await Promise.race([closed, budget, ...(aborted ? [aborted] : [])]);
    if (typeof first !== "string") {
      if (first.kind === "spawn-error") return { kind: "spawn-error" };
      return {
        kind: "exit",
        code: first.code,
        output: Buffer.concat(sink.chunks),
        overflow: sink.overflow,
      };
    }
    const confirmed = await stopChild(child, () => exited || settled, options.stopGraceMs);
    if (!confirmed) throw new WslImageCleanupError(first === "abort" ? "abort" : "timeout");
    if (first === "abort") throw abortError("WSL 镜像准备已取消");
    return { kind: "timeout" };
  } finally {
    if (timer) clearTimeout(timer);
    if (onAbort && options.signal) options.signal.removeEventListener("abort", onAbort);
    if (exited || settled) releaseParentFallback();
  }
}

function headerValue(headers: NodeJS.Dict<string | string[]>, name: string): string | null {
  const value = headers[name] ?? headers[name.toLowerCase()];
  if (Array.isArray(value)) return value[0] ?? null;
  return value ?? null;
}

function parseTotalBytes(headers: NodeJS.Dict<string | string[]>, statusCode: number, offset: number): number | null {
  const range = headerValue(headers, "content-range");
  const match = range ? /\/(\d+)\s*$/.exec(range) : null;
  if (match) return Number(match[1]);
  const length = headerValue(headers, "content-length");
  if (!length || !/^\d+$/.test(length)) return null;
  const size = Number(length);
  return statusCode === 206 ? offset + size : size;
}

function defaultHttpRequest(request: WslImageHttpRequest): Promise<WslImageHttpResponse> {
  return new Promise((resolve, reject) => {
    if (request.url.protocol !== "https:") {
      reject(new WslImageTransferError("download"));
      return;
    }
    const req = https.request(
      request.url,
      { method: "GET", headers: request.headers },
      (res) => {
        resolve({
          statusCode: res.statusCode ?? 0,
          headers: res.headers,
          body: res,
          abort: () => {
            res.destroy();
            req.destroy();
          },
        });
      },
    );
    const onAbort = (): void => {
      req.destroy();
      reject(abortError("WSL 镜像下载已取消"));
    };
    if (request.signal?.aborted) {
      onAbort();
      return;
    }
    request.signal?.addEventListener("abort", onAbort, { once: true });
    req.on("error", (error) => {
      request.signal?.removeEventListener("abort", onAbort);
      if (request.signal?.aborted || isAbortError(error)) {
        reject(abortError("WSL 镜像下载已取消"));
        return;
      }
      reject(new WslImageTransferError("download"));
    });
    req.on("close", () => request.signal?.removeEventListener("abort", onAbort));
    req.end();
  });
}

async function readBody(options: {
  body: Readable;
  stream: WriteStream;
  signal?: AbortSignal;
  received: number;
  maxBytes: number;
  onBytes: (received: number) => void;
}): Promise<number> {
  const { body, stream } = options;
  let received = options.received;
  let settled = false;
  let pending = 0;
  let ended = false;
  let stall: NodeJS.Timeout | undefined;

  return new Promise((resolve, reject) => {
    const cleanup = (): void => {
      if (stall) clearTimeout(stall);
      options.signal?.removeEventListener("abort", onAbort);
    };
    const fail = (error: Error): void => {
      if (settled) return;
      settled = true;
      cleanup();
      body.destroy();
      stream.destroy();
      reject(error);
    };
    const succeed = (): void => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(received);
    };
    const bump = (): void => {
      if (stall) clearTimeout(stall);
      stall = setTimeout(() => fail(new WslImageTransferError("download")), DOWNLOAD_STALL_MS);
    };
    const finishIfDone = (): void => {
      if (ended && pending === 0) succeed();
    };
    const onAbort = (): void => fail(abortError("WSL 镜像下载已取消"));

    bump();
    if (options.signal?.aborted) {
      onAbort();
      return;
    }
    options.signal?.addEventListener("abort", onAbort, { once: true });
    body.on("data", (chunk: Buffer | string) => {
      if (settled) return;
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      body.pause();
      pending += 1;
      received += buffer.length;
      if (received > options.maxBytes) {
        fail(new WslImageTransferError("download"));
        return;
      }
      options.onBytes(received);
      const write = (): void => {
        pending -= 1;
        if (settled) return;
        bump();
        finishIfDone();
        if (!ended) body.resume();
      };
      if (stream.write(buffer)) write();
      else stream.once("drain", write);
    });
    body.on("end", () => {
      ended = true;
      finishIfDone();
    });
    body.on("error", () => {
      if (options.signal?.aborted) fail(abortError("WSL 镜像下载已取消"));
      else fail(new WslImageTransferError("download"));
    });
    stream.on("error", () => fail(new WslImageTransferError("download")));
  });
}

async function finishWrite(stream: WriteStream): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    stream.once("error", () => reject(new WslImageTransferError("download")));
    stream.end(() => resolve());
  });
}

/**
 * 下载固定根文件系统。已有未完成的 .partial 会带 Range 续传。
 * 只跟随 HTTPS 重定向；HTTP 或超限直接失败，不写最终文件。
 */
export async function downloadFixedWslImage(options: {
  destination: string;
  signal?: AbortSignal;
  requestImpl?: (request: WslImageHttpRequest) => Promise<WslImageHttpResponse>;
  freeBytesImpl?: (directory: string) => Promise<number | null>;
  onProgress?: (received: number, total: number | null) => void;
}): Promise<void> {
  if (options.signal?.aborted) throw abortError("WSL 镜像下载已取消");
  const request = options.requestImpl ?? defaultHttpRequest;
  const partial = `${options.destination}.partial`;
  await fs.mkdir(path.dirname(options.destination), { recursive: true });

  let offset = 0;
  try {
    const existing = await fs.stat(partial);
    if (existing.isFile() && existing.size > 0 && existing.size < MAX_IMAGE_BYTES) {
      offset = existing.size;
    }
  } catch {
    offset = 0;
  }

  let current = new URL(YAOMU_WSL_IMAGE_URL);
  let response: WslImageHttpResponse | null = null;
  for (let redirect = 0; redirect <= MAX_REDIRECTS; redirect += 1) {
    if (current.protocol !== "https:") throw new WslImageTransferError("download");
    const headers: Record<string, string> = {};
    if (offset > 0) headers.Range = `bytes=${offset}-`;
    response = await request({ url: current, headers, signal: options.signal });
    if (![301, 302, 303, 307, 308].includes(response.statusCode)) break;
    const location = headerValue(response.headers, "location");
    response.abort();
    response = null;
    if (!location || redirect === MAX_REDIRECTS) throw new WslImageTransferError("download");
    current = new URL(location, current);
  }
  if (!response) throw new WslImageTransferError("download");

  try {
    if (response.statusCode === 200) offset = 0;
    else if (response.statusCode !== 206) throw new WslImageTransferError("download");
    const total = parseTotalBytes(response.headers, response.statusCode, offset);
    if (total !== null && total > MAX_IMAGE_BYTES) throw new WslImageTransferError("download");
    const free = options.freeBytesImpl
      ? await options.freeBytesImpl(path.dirname(options.destination))
      : await freeBytes(path.dirname(options.destination));
    if (total !== null && free !== null && free < total - offset + DISK_MARGIN_BYTES) {
      throw new WslImageTransferError("disk");
    }
    const stream = createWriteStream(partial, { flags: offset > 0 ? "a" : "w" });
    try {
      const received = await readBody({
        body: response.body,
        stream,
        signal: options.signal,
        received: offset,
        maxBytes: MAX_IMAGE_BYTES,
        onBytes: (bytes) => options.onProgress?.(bytes, total),
      });
      await finishWrite(stream);
      if (total !== null && received !== total) throw new WslImageTransferError("download");
    } catch (error) {
      stream.destroy();
      throw error;
    }
  } finally {
    response.abort();
  }

  await fs.rm(options.destination, { force: true });
  await fs.rename(partial, options.destination);
}

async function freeBytes(directory: string): Promise<number | null> {
  try {
    const stats = await fs.statfs(directory);
    return Number(stats.bavail) * Number(stats.bsize);
  } catch {
    return null;
  }
}

async function fileSize(file: string): Promise<number | null> {
  try {
    const stats = await fs.stat(file);
    if (!stats.isFile() || stats.size <= 0 || stats.size > MAX_IMAGE_BYTES) return null;
    return stats.size;
  } catch {
    return null;
  }
}

async function removeOwnedInstallDir(directory: string, root: string): Promise<void> {
  const base = path.resolve(root);
  const target = path.resolve(directory);
  const relative = path.relative(base, target);
  if (relative.startsWith("..") || path.isAbsolute(relative)) return;
  let stats: Awaited<ReturnType<typeof fs.lstat>>;
  try {
    stats = await fs.lstat(target);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
    throw error;
  }
  if (stats.isSymbolicLink()) {
    await fs.unlink(target);
    return;
  }
  const real = await fs.realpath(target);
  const realRelative = path.relative(base, real);
  if (realRelative.startsWith("..") || path.isAbsolute(realRelative)) return;
  await fs.rm(real, { recursive: true, force: true });
}

/**
 * Windows 启动准备：yaomu 已存在则跳过；不存在则下载并 wsl --import。
 * YAOMU_WSL_DISTRO 指向其他名字时不导入，避免覆盖开发机正在用的发行版。
 */
export async function ensureYaomuWslImage(
  options: EnsureWslImageOptions,
): Promise<WslImageStatus> {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  const log = options.log ?? (() => undefined);
  const spawnImpl = options.spawnImpl ?? spawn;
  const stopGraceMs = options.stopGraceMs ?? STOP_GRACE_MS;

  if (platform !== "win32") {
    log("[bootstrap] yaomu image skipped: not-windows");
    return { status: "skipped", reason: "not_windows" };
  }
  const configured = env.YAOMU_WSL_DISTRO?.trim() || DISTRO_NAME;
  if (configured !== DISTRO_NAME) {
    log("[bootstrap] yaomu image skipped: distro-override");
    return { status: "skipped", reason: "distro_override" };
  }

  const command = wslCommand(env);
  const list = await runWslCommand({
    command,
    args: ["-l", "-q"],
    timeoutMs: options.listTimeoutMs ?? LIST_TIMEOUT_MS,
    spawnImpl,
    signal: options.signal,
    stopGraceMs,
    collect: true,
  });
  if (list.kind === "spawn-error") {
    log("[bootstrap] yaomu image skipped: wsl-unavailable");
    return { status: "skipped", reason: "wsl_unavailable" };
  }
  if (list.kind === "timeout" || (list.kind === "exit" && list.overflow)) {
    log("[bootstrap] yaomu image failed: list");
    return { status: "failed", reason: "list_failed" };
  }

  let names: string[] = [];
  if (list.kind === "exit" && list.code === 0) {
    names = parseWslDistroNames(decodeWslOutput(list.output));
  } else if (list.kind === "exit") {
    const failure = classifyWslListFailure(decodeWslOutput(list.output));
    if (failure === "unavailable") {
      log("[bootstrap] yaomu image skipped: wsl-unavailable");
      return { status: "skipped", reason: "wsl_unavailable" };
    }
    if (failure === "unknown") {
      log("[bootstrap] yaomu image failed: list");
      return { status: "failed", reason: "list_failed" };
    }
  }
  if (names.includes(DISTRO_NAME)) {
    log("[bootstrap] yaomu image skipped: distro-present");
    return { status: "skipped", reason: "distro_present" };
  }

  const previous = await readDefaultDistro({
    command,
    spawnImpl,
    signal: options.signal,
    stopGraceMs,
    timeoutMs: options.listTimeoutMs ?? LIST_TIMEOUT_MS,
  });

  const cacheDir = path.join(options.userDataDir, "wsl", "cache");
  const archive = path.join(cacheDir, ARCHIVE_NAME);
  const installDir = path.join(options.userDataDir, "wsl", "distros", DISTRO_NAME);
  await fs.mkdir(cacheDir, { recursive: true });
  try {
    if ((await fileSize(archive)) === null) {
      let lastLogged = -PROGRESS_STEP_BYTES;
      const download = options.downloadImpl ?? ((destination, signal) => downloadFixedWslImage({
        destination,
        signal,
        requestImpl: options.requestImpl,
        freeBytesImpl: options.freeBytesImpl,
        onProgress: (received, total) => {
          const done = total !== null && received >= total;
          if (!done && received - lastLogged < PROGRESS_STEP_BYTES) return;
          lastLogged = received;
          log(
            `[bootstrap] yaomu image download bytes=${received} total=${total ?? "unknown"}`,
          );
        },
      }));
      await download(archive, options.signal);
    } else {
      log("[bootstrap] yaomu image archive reused");
    }
  } catch (error) {
    if (isAbortError(error) || error instanceof WslImageCleanupError) throw error;
    if (error instanceof WslImageTransferError && error.reason === "disk") {
      log("[bootstrap] yaomu image failed: disk");
      return { status: "failed", reason: "insufficient_disk" };
    }
    log("[bootstrap] yaomu image failed: download");
    return { status: "failed", reason: "download_failed" };
  }

  if ((await fileSize(archive)) === null) {
    log("[bootstrap] yaomu image failed: download");
    return { status: "failed", reason: "download_failed" };
  }

  await fs.mkdir(path.dirname(installDir), { recursive: true });
  await removeOwnedInstallDir(installDir, path.join(options.userDataDir, "wsl", "distros"));
  const imported = await runWslCommand({
    command,
    args: ["--import", DISTRO_NAME, installDir, archive, "--version", "2"],
    timeoutMs: options.importTimeoutMs ?? IMPORT_TIMEOUT_MS,
    spawnImpl,
    signal: options.signal,
    stopGraceMs,
    collect: false,
  });
  if (imported.kind !== "exit" || imported.code !== 0) {
    log("[bootstrap] yaomu image failed: import");
    return { status: "failed", reason: "import_failed" };
  }

  let restoreFailed = false;
  if (previous && previous !== DISTRO_NAME) {
    const current = await readDefaultDistro({
      command,
      spawnImpl,
      signal: options.signal,
      stopGraceMs,
      timeoutMs: options.listTimeoutMs ?? DEFAULT_TIMEOUT_MS,
    });
    if (current === DISTRO_NAME) {
      const restored = await runWslCommand({
        command,
        args: ["--set-default", previous],
        timeoutMs: options.listTimeoutMs ?? DEFAULT_TIMEOUT_MS,
        spawnImpl,
        signal: options.signal,
        stopGraceMs,
        collect: false,
      });
      restoreFailed = restored.kind !== "exit" || restored.code !== 0;
    }
  }

  // 发行版已经注册。删掉压缩包，避免根文件系统在磁盘上留两份。
  await fs.rm(archive, { force: true }).catch(() => undefined);
  log(restoreFailed
    ? "[bootstrap] yaomu image imported; default-restore-failed"
    : "[bootstrap] yaomu image imported");
  return { status: "imported" };
}

async function readDefaultDistro(options: {
  command: string;
  spawnImpl: typeof spawn;
  signal?: AbortSignal;
  stopGraceMs: number;
  timeoutMs: number;
}): Promise<string | null> {
  const listed = await runWslCommand({
    command: options.command,
    args: ["-l", "-v"],
    timeoutMs: options.timeoutMs,
    spawnImpl: options.spawnImpl,
    signal: options.signal,
    stopGraceMs: options.stopGraceMs,
    collect: true,
  });
  if (listed.kind !== "exit" || listed.code !== 0 || listed.overflow) return null;
  return parseWslDefaultDistro(decodeWslOutput(listed.output));
}

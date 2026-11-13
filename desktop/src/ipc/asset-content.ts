/**
 * 资产另存的纯逻辑：输入校验、URL 构造、流式下载与原子写入。
 *
 * 不依赖 Electron，便于在 Node 下直接验证；主进程适配层只负责保存对话框。
 * 目标路径由调用方（主进程）从系统对话框取得，Renderer 不能传入。
 */
import { randomUUID } from "node:crypto";
import { mkdir, open, rename, rm, type FileHandle } from "node:fs/promises";
import path from "node:path";

export interface AssetSaveInput {
  assetId: string;
  suggestedName?: string;
  title?: string;
}

export interface AssetContentConnection {
  baseUrl: string;
  token: string;
}

export interface DownloadAssetOptions {
  connection: AssetContentConnection;
  assetId: string;
  targetPath: string;
  fetchImpl?: typeof fetch;
  timeoutMs?: number;
}

const MAX_ASSET_ID = 200;
const MAX_NAME = 120;
const MAX_TITLE = 120;
/** 建议文件名先按较宽上限保留，取 basename 后再截到 MAX_NAME，避免长路径截断丢掉真实文件名。 */
const MAX_NAME_INPUT = 4096;
const DEFAULT_TIMEOUT_MS = 60_000;
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/g;

/**
 * 资产 ID 的共享校验规则（另存与播放协议共用）：不透明字符串，
 * 不允许空值、路径分隔符、NUL 或 `.`/`..`。只返回布尔值，错误码由调用方决定。
 */
export function isValidAssetId(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= MAX_ASSET_ID &&
    value !== "." &&
    value !== ".." &&
    !/[/\\\u0000]/.test(value)
  );
}

function cleanText(value: unknown, max: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const text = value.replace(CONTROL_CHARS, "").trim();
  return text.length > 0 ? text.slice(0, max) : undefined;
}

/** 校验 Renderer 传入的另存参数；只接受资产 ID 与展示用文案。 */
export function validateAssetSaveInput(raw: unknown): AssetSaveInput {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new Error("ASSET_SAVE_INPUT_INVALID");
  }
  const record = raw as Record<string, unknown>;
  const assetId = record.assetId;
  if (!isValidAssetId(assetId)) {
    throw new Error("ASSET_SAVE_INPUT_INVALID");
  }
  const suggestedName = cleanText(record.suggestedName, MAX_NAME_INPUT);
  const title = cleanText(record.title, MAX_TITLE);
  return {
    assetId,
    ...(suggestedName ? { suggestedName } : {}),
    ...(title ? { title } : {}),
  };
}

/** 把建议文件名压成 basename；空值回退到 asset-<id>，避免把路径带进对话框默认值。 */
export function sanitizeSuggestedName(value: string, assetId: string): string {
  const base = path.basename(value.replace(/\\/g, "/")).replace(CONTROL_CHARS, "").trim();
  if (base.length === 0 || base === "." || base === "..") return `asset-${assetId}`;
  return base.slice(0, MAX_NAME);
}

/** 资产内容端点；不接受 Renderer 提供的 URL。 */
export function assetContentUrl(baseUrl: string, assetId: string): string {
  return `${baseUrl.replace(/\/+$/, "")}/api/v1/assets/${encodeURIComponent(assetId)}/content`;
}

async function closeQuietly(handle: FileHandle): Promise<void> {
  await handle.close().catch(() => undefined);
}

async function replaceFile(temp: string, target: string): Promise<void> {
  try {
    await rename(temp, target);
  } catch (error) {
    // Windows 上 rename 覆盖已有文件会失败；保存对话框已确认覆盖，删旧文件后重试。
    const code = (error as NodeJS.ErrnoException).code;
    if (code !== "EEXIST" && code !== "EPERM") throw error;
    await rm(target, { force: true });
    await rename(temp, target);
  }
}

/**
 * 本模块抛出的错误只允许是安全错误码；fs/fetch 原始错误可能带本机路径，
 * 在离开主进程前统一收敛，避免经 IPC 泄漏给 Renderer。
 */
function safeDownloadError(error: unknown): Error {
  if (error instanceof Error) {
    if (/^ASSET_SAVE_[A-Z0-9_]+$/.test(error.message)) return error;
    if (error.name === "AbortError" || error.name === "TimeoutError") {
      return new Error("ASSET_SAVE_FETCH_FAILED");
    }
  }
  return new Error("ASSET_SAVE_WRITE_FAILED");
}

/**
 * 从 Core 读取资产内容并写入 targetPath。
 * 先写同目录临时文件，成功后 rename；失败时清理临时文件，不留下半截目标。
 */
export async function downloadAssetContent(options: DownloadAssetOptions): Promise<void> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const target = path.resolve(options.targetPath);
  const dir = path.dirname(target);
  try {
    await mkdir(dir, { recursive: true });
  } catch {
    throw new Error("ASSET_SAVE_WRITE_FAILED");
  }

  let response: Response;
  try {
    response = await fetchImpl(assetContentUrl(options.connection.baseUrl, options.assetId), {
      method: "GET",
      headers: {
        Accept: "application/octet-stream",
        "X-YaoMu-Token": options.connection.token,
      },
      redirect: "error",
      signal: AbortSignal.timeout(options.timeoutMs ?? DEFAULT_TIMEOUT_MS),
    });
  } catch {
    throw new Error("ASSET_SAVE_FETCH_FAILED");
  }

  if (!response.ok) {
    await response.body?.cancel().catch(() => undefined);
    throw new Error(`ASSET_SAVE_HTTP_${response.status}`);
  }
  if (!response.body) {
    throw new Error("ASSET_SAVE_EMPTY_BODY");
  }

  const temp = path.join(dir, `.yaomu-save-${randomUUID()}.tmp`);
  let handle: FileHandle;
  try {
    handle = await open(temp, "wx", 0o600);
  } catch {
    await response.body.cancel().catch(() => undefined);
    throw new Error("ASSET_SAVE_WRITE_FAILED");
  }
  let written = 0;
  try {
    for await (const chunk of response.body as unknown as AsyncIterable<Uint8Array>) {
      // FileHandle.write 可能短写；循环到本块写完，避免大媒体静默截断。
      let offset = 0;
      while (offset < chunk.byteLength) {
        const { bytesWritten } = await handle.write(chunk, offset, chunk.byteLength - offset);
        if (bytesWritten <= 0) throw new Error("ASSET_SAVE_WRITE_FAILED");
        offset += bytesWritten;
        written += bytesWritten;
      }
    }
    if (written === 0) throw new Error("ASSET_SAVE_EMPTY_BODY");
    await handle.sync();
    await closeQuietly(handle);
    await replaceFile(temp, target);
  } catch (error) {
    await closeQuietly(handle);
    await rm(temp, { force: true }).catch(() => undefined);
    throw safeDownloadError(error);
  }
}

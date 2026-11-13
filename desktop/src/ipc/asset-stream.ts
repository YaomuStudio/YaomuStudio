/**
 * 资产播放协议（yaomu-asset）的纯逻辑：URL 解析、资产 ID 校验、Range 过滤与响应映射。
 *
 * 不依赖 Electron，便于在 Node 下直接单测；主进程只负责注册 scheme 与 handler。
 * Renderer 只拿 `yaomu-asset://asset/<assetId>`，实例令牌只存在于主进程。
 */
import { assetContentUrl, isValidAssetId, type AssetContentConnection } from "./asset-content";

export const ASSET_SCHEME = "yaomu-asset";
export const ASSET_HOST = "asset";

/** Range 只允许单区间语法；多区间/超长直接忽略（回退整段响应）。 */
const MAX_RANGE_LENGTH = 100;
const RANGE_PATTERN = /^bytes=\d{0,18}-\d{0,18}$/;
/** 只透传播放需要的响应头；不透传 set-cookie、location 等。 */
const PASS_THROUGH_HEADERS = [
  "content-type",
  "content-length",
  "content-range",
  "accept-ranges",
  "cache-control",
] as const;
const MAX_URL_LENGTH = 4096;

export type AssetStreamConnection = AssetContentConnection;
export type AssetStreamConnectionGetter = () => AssetStreamConnection | null;

/** Electron `protocol.handle` 传入的 Request 子集；结构兼容 GlobalRequest。 */
export interface AssetStreamRequest {
  url: string;
  method: string;
  headers: { get(name: string): string | null };
}

export type AssetStreamHandler = (request: AssetStreamRequest) => Promise<Response>;

/** 生成播放地址；非法 ID 返回空串（preload 内有一份等价实现，sandbox 不能 require 本地模块）。 */
export function assetPlaybackUrl(assetId: unknown): string {
  if (!isValidAssetId(assetId)) return "";
  return `${ASSET_SCHEME}://${ASSET_HOST}/${encodeURIComponent(assetId)}`;
}

/** 解析 `yaomu-asset://asset/<assetId>`；任何多余路径段、查询、片段或非法 ID 都拒绝。 */
export function parseAssetUrl(raw: unknown): string | null {
  if (typeof raw !== "string" || raw.length === 0 || raw.length > MAX_URL_LENGTH) return null;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== `${ASSET_SCHEME}:`) return null;
  if (url.hostname !== ASSET_HOST) return null;
  if (url.search !== "" || url.hash !== "") return null;
  const path = url.pathname;
  if (!path.startsWith("/") || path.length <= 1) return null;
  const encoded = path.slice(1);
  // 编码后的分隔符不构成新路径段，但仍拒绝，避免二次解码绕过。
  if (encoded.includes("/")) return null;
  let assetId: string;
  try {
    assetId = decodeURIComponent(encoded);
  } catch {
    return null;
  }
  return isValidAssetId(assetId) ? assetId : null;
}

/** 只转发合法的单区间 Range；非法值忽略而不是拒绝整个请求。 */
export function sanitizeRange(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (trimmed.length === 0 || trimmed.length > MAX_RANGE_LENGTH) return undefined;
  if (!RANGE_PATTERN.test(trimmed)) return undefined;
  const spec = trimmed.slice("bytes=".length);
  const [start, end] = spec.split("-");
  if (start === "" && end === "") return undefined;
  return `bytes=${start}-${end}`;
}

function jsonResponse(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: { code } }), {
    status,
    headers: {
      "content-type": "application/json",
      "cache-control": "no-store",
      "access-control-allow-origin": "*",
    },
  });
}

function responseHeaders(upstream: Response): Headers {
  const headers = new Headers();
  for (const name of PASS_THROUGH_HEADERS) {
    const value = upstream.headers.get(name);
    if (value !== null) headers.set(name, value);
  }
  headers.set("access-control-allow-origin", "*");
  return headers;
}

/**
 * 创建协议 handler：带实例令牌向 Core 取资产内容，并原样返回状态/Range 头。
 *
 * - 只接受 GET/HEAD；Core 未就绪返回 503；非法地址/ID 返回 400。
 * - 不跟随重定向；不设置短超时（长视频预览不能被中途打断）。
 * - 永不把令牌、本机路径或 Core URL 写进响应或日志。
 */
export function createAssetStreamHandler(
  getConnection: AssetStreamConnectionGetter,
  fetchImpl: typeof fetch = fetch,
): AssetStreamHandler {
  return async (request) => {
    const method = request.method.toUpperCase();
    if (method !== "GET" && method !== "HEAD") return jsonResponse(405, "METHOD_NOT_ALLOWED");

    const assetId = parseAssetUrl(request.url);
    if (!assetId) return jsonResponse(400, "ASSET_URL_INVALID");

    const connection = getConnection();
    if (!connection) return jsonResponse(503, "CORE_NOT_READY");

    const headers: Record<string, string> = {
      Accept: "application/octet-stream",
      "X-YaoMu-Token": connection.token,
    };
    const range = sanitizeRange(request.headers.get("range"));
    if (range) headers.Range = range;

    let upstream: Response;
    try {
      upstream = await fetchImpl(assetContentUrl(connection.baseUrl, assetId), {
        method,
        headers,
        redirect: "error",
      });
    } catch {
      return jsonResponse(502, "ASSET_STREAM_FAILED");
    }

    if (method === "HEAD") {
      // HEAD 不返回 body；取消上游流避免悬挂连接。
      await upstream.body?.cancel().catch(() => undefined);
      return new Response(null, { status: upstream.status, headers: responseHeaders(upstream) });
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: responseHeaders(upstream),
    });
  };
}

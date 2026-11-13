/**
 * 内部 API 代理：Renderer 只能调用白名单方法，主进程附加实例令牌并转发到 Core。
 *
 * - 方法名 → HTTP 路由的映射是本文件唯一的白名单；不存在任意通道透传。
 * - 令牌只用于请求头，不进入 URL、日志或返回值。
 * - 错误统一转成可序列化信封 {ok:false,error}，避免 Electron IPC 丢失自定义错误字段。
 */
import { randomUUID } from "node:crypto";
import type { CoreConnection, FetchLike } from "../bootstrap";
import {
  DesktopAuthorization,
  type IpcSource,
  type WindowPathGrants,
} from "./authorization";

export interface ApiProxyError {
  code: string;
  messageKey: string;
  retryable: boolean;
  actionIds: string[];
  requestId?: string;
  status?: number;
}

export type ApiProxyResult =
  | { ok: true; data?: unknown }
  | { ok: false; error: ApiProxyError };

export type ConnectionProvider = () => CoreConnection | null;

type Args = readonly unknown[];
type QueryValue = string | number | undefined;

interface ApiRoute {
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  path: (args: Args) => string;
  query?: (args: Args) => Record<string, QueryValue>;
  body?: (args: Args) => unknown;
  /** 状态变更请求携带 Idempotency-Key；只读评估型 POST（preflight/plan）不携带。 */
  idempotent?: boolean;
  /** 单次调用上限；缺省用调用器默认值。 */
  timeoutMs?: number;
}

/** 默认单次调用上限；本地 Core 的读写正常在毫秒级完成。 */
const DEFAULT_API_TIMEOUT_MS = 30_000;

/**
 * 提交链路（预检 + 创建 Run）的上限：Core 在事务内完成冻结配置与任务/事件写入。
 * 本机存储偶发停顿（实测一次约 82s）时，30s 上限会让写事务随连接断开回滚，
 * 客户端还把“慢”误报成“核心未就绪”。放宽上限只影响异常停顿时的等待，
 * 不改变正常路径的响应时间。
 */
const SUBMIT_TIMEOUT_MS = 120_000;

/** AbortSignal.timeout 在 Node 中以 TimeoutError 拒绝；兼容 AbortError 命名。 */
function isTimeoutError(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;
  const name = (error as { name?: unknown }).name;
  return name === "TimeoutError" || name === "AbortError";
}

const SLOTS = [
  "asr",
  "translate",
  "tts",
  "voice-clone",
  "subtitle-erase",
  "separation",
  "ocr",
  "ffmpeg",
] as const;

const MODEL_SLOTS = SLOTS.slice(0, 6);

function isSlot(value: unknown): value is (typeof SLOTS)[number] {
  return typeof value === "string" && (SLOTS as readonly string[]).includes(value);
}

function isModelSlot(value: unknown): value is (typeof MODEL_SLOTS)[number] {
  return typeof value === "string" && (MODEL_SLOTS as readonly string[]).includes(value);
}

/** 不透明 ID 校验：拒绝空值、路径穿越与超长输入。 */
function idArg(args: Args, index: number, field: string): string {
  const value = args[index];
  if (typeof value !== "string" || value.length === 0 || value.length > 200) {
    throw new InvalidArgumentError(`${field} 必须是非空字符串`);
  }
  if (value === "." || value === ".." || /[/\\\u0000]/.test(value)) {
    throw new InvalidArgumentError(`${field} 含非法字符`);
  }
  return encodeURIComponent(value);
}

function slotValue(args: Args, index: number, modelOnly = false): string {
  const value = args[index];
  if (modelOnly ? !isModelSlot(value) : !isSlot(value)) {
    throw new InvalidArgumentError("slot 不在能力清单内");
  }
  return value as string;
}

function slotArg(args: Args, index: number, modelOnly = false): string {
  return encodeURIComponent(slotValue(args, index, modelOnly));
}

function objectArg(args: Args, index: number, field: string): Record<string, unknown> {
  const value = args[index];
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new InvalidArgumentError(`${field} 必须是对象`);
  }
  return value as Record<string, unknown>;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

class InvalidArgumentError extends Error {}

const ROUTES: Record<string, ApiRoute> = {
  getSystem: { method: "GET", path: () => "/api/v1/system" },
  getCapabilities: { method: "GET", path: () => "/api/v1/capabilities" },
  getDevices: { method: "GET", path: () => "/api/v1/devices" },
  getEnvironment: {
    method: "GET",
    path: () => "/api/v1/environment",
    query: (args) => ({ refresh: args[0] === true ? "true" : undefined }),
  },
  getStorageUsage: { method: "GET", path: () => "/api/v1/storage/usage" },
  getLocalApiStats: { method: "GET", path: () => "/api/v1/local-api/stats" },
  getLocalApiSettings: { method: "GET", path: () => "/api/v1/local-api/settings" },
  updateLocalApiSettings: {
    method: "PUT",
    path: () => "/api/v1/local-api/settings",
    body: (args) => objectArg(args, 0, "settings"),
    idempotent: true,
  },
  issueLocalApiToken: {
    method: "POST",
    path: () => "/api/v1/local-api/token",
    idempotent: true,
  },
  revokeLocalApiToken: {
    method: "DELETE",
    path: () => "/api/v1/local-api/token",
    idempotent: true,
  },
  getEngineConfig: {
    method: "GET",
    path: (args) => `/api/v1/engines/${slotArg(args, 0, true)}/config`,
  },
  setEngineConfig: {
    method: "PUT",
    path: (args) => `/api/v1/engines/${slotArg(args, 0, true)}/config`,
    body: (args) => objectArg(args, 1, "engineConfig"),
    idempotent: true,
  },

  listProjects: { method: "GET", path: () => "/api/v1/projects" },
  getProject: { method: "GET", path: (args) => `/api/v1/projects/${idArg(args, 0, "id")}` },
  createProject: {
    method: "POST",
    path: () => "/api/v1/projects",
    body: (args) => objectArg(args, 0, "project"),
    idempotent: true,
  },
  updateProject: {
    method: "PATCH",
    path: (args) => `/api/v1/projects/${idArg(args, 0, "id")}`,
    body: (args) => objectArg(args, 1, "project"),
    idempotent: true,
  },
  deleteProject: {
    method: "DELETE",
    path: (args) => `/api/v1/projects/${idArg(args, 0, "id")}`,
    idempotent: true,
  },
  importAssets: {
    method: "POST",
    path: () => "/api/v1/assets/imports",
    body: (args) => objectArg(args, 0, "import"),
    idempotent: true,
  },
  relocateAsset: {
    method: "POST",
    path: (args) => `/api/v1/assets/${idArg(args, 0, "assetId")}/relocate`,
    body: (args) => objectArg(args, 1, "relocate"),
    idempotent: true,
  },

  listTasks: {
    method: "GET",
    path: () => "/api/v1/tasks",
    query: (args) => {
      const query = args[0] && typeof args[0] === "object" ? (args[0] as Record<string, unknown>) : {};
      return {
        project_id: optionalString(query.project_id),
        run_id: optionalString(query.run_id),
        status: optionalString(query.status),
        cursor: optionalString(query.cursor),
        limit: typeof query.limit === "number" ? query.limit : undefined,
      };
    },
  },
  getTask: { method: "GET", path: (args) => `/api/v1/tasks/${idArg(args, 0, "id")}` },
  getExecutionLog: {
    method: "GET",
    path: (args) => `/api/v1/tasks/${idArg(args, 0, "id")}/execution-log`,
    query: (args) => {
      const options =
        args[1] && typeof args[1] === "object" ? (args[1] as Record<string, unknown>) : {};
      return {
        attempt: typeof options.attempt === "number" ? options.attempt : undefined,
        tail: typeof options.tail === "number" ? options.tail : undefined,
      };
    },
  },
  cancelTask: {
    method: "POST",
    path: (args) => `/api/v1/tasks/${idArg(args, 0, "id")}/cancel`,
    idempotent: true,
  },
  retryTask: {
    method: "POST",
    path: (args) => `/api/v1/tasks/${idArg(args, 0, "id")}/retry`,
    idempotent: true,
  },
  getRun: {
    method: "GET",
    path: (args) => `/api/v1/workflow-runs/${idArg(args, 0, "id")}`,
  },
  getSnapshot: {
    method: "GET",
    path: () => "/api/v1/snapshot",
    query: (args) => ({ project_id: optionalString(args[0]) }),
  },

  preflight: {
    method: "POST",
    path: () => "/api/v1/tasks/preflight",
    body: (args) => objectArg(args, 0, "preflight"),
    timeoutMs: SUBMIT_TIMEOUT_MS,
  },
  createWorkflowRun: {
    method: "POST",
    path: () => "/api/v1/workflow-runs",
    body: (args) => objectArg(args, 0, "run"),
    idempotent: true,
    timeoutMs: SUBMIT_TIMEOUT_MS,
  },
  pauseRun: {
    method: "POST",
    path: (args) => `/api/v1/workflow-runs/${idArg(args, 0, "id")}/pause`,
    idempotent: true,
  },
  resumeRun: {
    method: "POST",
    path: (args) => `/api/v1/workflow-runs/${idArg(args, 0, "id")}/resume`,
    idempotent: true,
  },

  saveSubtitle: {
    method: "PUT",
    path: (args) => `/api/v1/subtitle-tracks/${idArg(args, 0, "trackId")}/revisions`,
    body: (args) => objectArg(args, 1, "revision"),
    idempotent: true,
  },
  importSubtitleTrack: {
    method: "POST",
    path: () => "/api/v1/subtitle-tracks/imports",
    body: (args) => objectArg(args, 0, "subtitleTrack"),
    idempotent: true,
  },
  listCustomPipelineTemplates: {
    method: "GET",
    path: () => "/api/v1/custom-pipeline/templates",
  },
  saveCustomPipelineTemplates: {
    method: "PUT",
    path: () => "/api/v1/custom-pipeline/templates",
    body: (args) => objectArg(args, 0, "templates"),
    idempotent: true,
  },
  generateCandidates: {
    method: "POST",
    path: (args) => `/api/v1/segments/${idArg(args, 0, "segmentId")}/candidates`,
    body: (args) => objectArg(args, 1, "candidates"),
    idempotent: true,
  },
  listCandidates: {
    method: "GET",
    path: (args) => `/api/v1/segments/${idArg(args, 0, "segmentId")}/candidates`,
    query: (args) => ({ language: optionalString(args[1]) }),
  },
  selectCandidate: {
    method: "PUT",
    path: (args) => `/api/v1/segments/${idArg(args, 0, "segmentId")}/selection`,
    body: (args) => objectArg(args, 1, "selection"),
    idempotent: true,
  },
  getSelection: {
    method: "GET",
    path: (args) => `/api/v1/segments/${idArg(args, 0, "segmentId")}/selection`,
    query: (args) => ({ language: optionalString(args[1]) }),
  },
  listReviews: {
    method: "GET",
    path: (args) => `/api/v1/projects/${idArg(args, 0, "id")}/reviews`,
    query: (args) => ({ run_id: optionalString(args[1]) }),
  },
  getReview: {
    method: "GET",
    path: (args) => `/api/v1/reviews/${idArg(args, 0, "id")}`,
  },
  approveReview: {
    method: "POST",
    path: (args) => `/api/v1/reviews/${idArg(args, 0, "reviewId")}/approve`,
    body: (args) => objectArg(args, 1, "review"),
    idempotent: true,
  },

  export: {
    method: "POST",
    path: () => "/api/v1/exports",
    body: (args) => objectArg(args, 0, "export"),
    idempotent: true,
  },
  listExports: {
    method: "GET",
    path: () => "/api/v1/exports",
    query: (args) => ({
      project_id: optionalString(args[0]),
    }),
  },

  getInstallations: { method: "GET", path: () => "/api/v1/installations" },
  planInstallation: {
    method: "POST",
    path: () => "/api/v1/installations/plan",
    body: (args) => ({ slot: slotValue(args, 0) }),
  },
  install: {
    method: "POST",
    path: () => "/api/v1/installations",
    body: (args) => objectArg(args, 0, "installation"),
    idempotent: true,
  },
  cancelInstallation: {
    method: "POST",
    path: (args) => `/api/v1/installations/${idArg(args, 0, "id")}/cancel`,
    idempotent: true,
  },
  rollbackInstallation: {
    method: "POST",
    path: (args) => `/api/v1/capabilities/${slotArg(args, 0, true)}/rollback`,
    idempotent: true,
  },
  deleteInstallation: {
    method: "DELETE",
    path: (args) => `/api/v1/capabilities/${slotArg(args, 0, true)}/installation`,
    idempotent: true,
  },
};

function errorResult(
  code: string,
  messageKey: string,
  requestId: string,
  options: { retryable?: boolean; actionIds?: string[]; status?: number } = {},
): ApiProxyResult {
  return {
    ok: false,
    error: {
      code,
      messageKey,
      retryable: options.retryable ?? false,
      actionIds: options.actionIds ?? [],
      requestId,
      status: options.status,
    },
  };
}

const STATUS_MAP: Record<number, { code: string; messageKey: string }> = {
  400: { code: "INVALID_REQUEST", messageKey: "api.invalidRequest" },
  401: { code: "LOCAL_AUTH_REQUIRED", messageKey: "api.authRequired" },
  403: { code: "LOCAL_ACCESS_DENIED", messageKey: "api.accessDenied" },
  404: { code: "NOT_FOUND", messageKey: "api.notFound" },
  409: { code: "CONFLICT", messageKey: "api.conflict" },
  422: { code: "INVALID_REQUEST", messageKey: "api.invalidRequest" },
  507: { code: "INSUFFICIENT_DISK", messageKey: "api.insufficientDisk" },
  503: { code: "CORE_NOT_READY", messageKey: "api.coreNotReady" },
};

function mapErrorBody(body: unknown, status: number, requestId: string): ApiProxyResult {
  if (typeof body === "object" && body !== null) {
    const error = (body as { error?: unknown }).error;
    if (typeof error === "object" && error !== null) {
      const record = error as Record<string, unknown>;
      if (typeof record.code === "string" && typeof record.message_key === "string") {
        return errorResult(record.code, record.message_key, requestId, {
          retryable: record.retryable === true,
          actionIds: Array.isArray(record.action_ids)
            ? record.action_ids.filter((item): item is string => typeof item === "string")
            : [],
          status,
        });
      }
    }
  }
  const fallback = STATUS_MAP[status] ?? { code: "INVALID_REQUEST", messageKey: "api.httpError" };
  return errorResult(fallback.code, fallback.messageKey, requestId, { status });
}

export interface ApiInvokerOptions {
  timeoutMs?: number;
}

/**
 * 构造白名单代理调用器。返回 Promise<ApiProxyResult>，不抛异常（除 IPC 本身失败）。
 * 单独导出便于无 electron 的 node 层验证。
 */
export function createApiInvoker(
  getConnection: ConnectionProvider,
  fetchImpl: FetchLike = fetch,
  options: ApiInvokerOptions = {},
): (method: unknown, args: unknown, paths?: WindowPathGrants) => Promise<ApiProxyResult> {
  const defaultTimeoutMs = options.timeoutMs ?? DEFAULT_API_TIMEOUT_MS;
  return async (method: unknown, rawArgs: unknown, paths?: WindowPathGrants): Promise<ApiProxyResult> => {
    const requestId = randomUUID();
    if (typeof method !== "string" || !Object.prototype.hasOwnProperty.call(ROUTES, method)) {
      return errorResult("INVALID_REQUEST", "api.unknownMethod", requestId);
    }
    const route = ROUTES[method];
    const args: Args = Array.isArray(rawArgs) ? rawArgs : [];
    if (method === "importAssets" || method === "export") {
      try {
        if (!paths) throw new Error("missing path grants");
        paths.assertApiPaths(method, args);
      } catch {
        return errorResult("LOCAL_ACCESS_DENIED", "api.accessDenied", requestId, { status: 403 });
      }
    }
    const connection = getConnection();
    if (!connection) return errorResult("CORE_NOT_READY", "api.coreNotReady", requestId, { retryable: true });

    let url: string;
    let bodyText: string | undefined;
    try {
      const path = route.path(args);
      const search = new URLSearchParams();
      for (const [key, value] of Object.entries(route.query?.(args) ?? {})) {
        if (value !== undefined) search.set(key, String(value));
      }
      const query = search.toString();
      url = `${connection.baseUrl}${path}${query ? `?${query}` : ""}`;
      if (route.body) bodyText = JSON.stringify(route.body(args));
    } catch {
      return errorResult("INVALID_REQUEST", "api.invalidRequest", requestId);
    }

    const headers: Record<string, string> = {
      Accept: "application/json",
      "X-YaoMu-Token": connection.token,
      request_id: requestId,
    };
    if (route.idempotent) headers["Idempotency-Key"] = randomUUID();
    if (bodyText !== undefined) headers["Content-Type"] = "application/json";

    let response: Response;
    try {
      response = await fetchImpl(url, {
        method: route.method,
        headers,
        body: bodyText,
        signal: AbortSignal.timeout(route.timeoutMs ?? defaultTimeoutMs),
        redirect: "error",
      });
    } catch (error) {
      // 超时表示 Core 仍在服务、但超过等待上限（例如本机存储短暂停顿）；
      // 与“连不上 Core”分开提示，避免把慢磁盘报成核心未就绪。
      if (isTimeoutError(error)) {
        return errorResult("CORE_NOT_READY", "api.requestTimeout", requestId, { retryable: true });
      }
      return errorResult("CORE_NOT_READY", "api.coreNotReady", requestId, { retryable: true });
    }

    if (response.status === 204) return { ok: true, data: undefined };

    let text = "";
    try {
      text = await response.text();
    } catch {
      return errorResult("INVALID_REQUEST", "api.invalidResponse", requestId, {
        status: response.status,
      });
    }
    let parsed: unknown;
    if (text.length > 0) {
      try {
        parsed = JSON.parse(text) as unknown;
      } catch {
        return errorResult("INVALID_REQUEST", "api.invalidResponse", requestId, {
          status: response.status,
        });
      }
    }
    if (!response.ok) return mapErrorBody(parsed, response.status, requestId);
    if (parsed === undefined) {
      return errorResult("INVALID_REQUEST", "api.invalidResponse", requestId, {
        status: response.status,
      });
    }
    return { ok: true, data: parsed };
  };
}

/** 来源校验先于业务转发；响应目录只能登记给发起请求的同一文档。 */
export function createAuthorizedApiHandler(
  invoke: ReturnType<typeof createApiInvoker>,
  authorization: DesktopAuthorization,
): (event: IpcSource, method: unknown, args: unknown) => Promise<ApiProxyResult> {
  return async (event, method, args) => {
    try {
      const authority = authorization.requireSender(event);
      const result = await invoke(method, args, authority.paths);
      authorization.assertCurrent(event, authority);
      if (result.ok && typeof method === "string") authority.paths.rememberCorePaths(method, result.data);
      return result;
    } catch {
      return errorResult("LOCAL_ACCESS_DENIED", "api.accessDenied", randomUUID(), { status: 403 });
    }
  };
}

/** 注册 IPC 通道：应用顶层来源、白名单方法、窗口路径授权逐层校验。 */
export function registerApiProxy(
  invoke: ReturnType<typeof createApiInvoker>,
  authorization: DesktopAuthorization,
): void {
  // 延迟加载 electron：白名单代理逻辑可在普通 node 进程里独立验证。
  const { ipcMain } = require("electron") as typeof import("electron");
  ipcMain.handle("yaomu:api:invoke", createAuthorizedApiHandler(invoke, authorization));
}

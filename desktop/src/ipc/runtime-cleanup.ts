import { app, ipcMain } from "electron";
import { DesktopAuthorization } from "./authorization";
import type { ApiProxyResult } from "./api";
import type { RuntimeController } from "../runtime";
import { cleanYaomuWslRuntime } from "../runtime-maintenance";

const ACTIVE_TASK_STATUSES = [
  "QUEUED",
  "WAITING_RESOURCE",
  "RUNNING",
  "PAUSE_REQUESTED",
  "PAUSED",
  "CANCEL_REQUESTED",
] as const;

interface TaskListLike {
  items?: unknown[];
}

export interface RuntimeCleanupError {
  code: string;
  messageKey: string;
  retryable: boolean;
}

export type RuntimeCleanupResult =
  | { ok: true; removed: boolean; reason?: "not_installed" }
  | { ok: false; error: RuntimeCleanupError };

async function hasActiveTasks(
  invoke: (method: unknown, args: unknown) => Promise<ApiProxyResult>,
): Promise<{ active: boolean; error?: RuntimeCleanupError }> {
  for (const status of ACTIVE_TASK_STATUSES) {
    const result = await invoke("listTasks", [{ status, limit: 1 }]);
    if (!result.ok) {
      return {
        active: true,
        error: {
          code: result.error.code,
          messageKey: result.error.messageKey,
          retryable: result.error.retryable,
        },
      };
    }
    const data = result.data as TaskListLike | undefined;
    if ((data?.items?.length ?? 0) > 0) return { active: true };
  }
  return { active: false };
}

/**
 * 应用自有 WSL 运行环境的独立维护入口。
 * 前置：Windows、Core 可用、没有任何活动任务；流程：停 Core → 注销 yaomu → 删除应用镜像目录。
 * 该操作会停止当前 Core；界面只提示用户重启应用，不在后台重新导入 WSL。
 */
export function registerRuntimeCleanupIpc(
  authorization: DesktopAuthorization,
  controller: RuntimeController,
  invoke: (method: unknown, args: unknown) => Promise<ApiProxyResult>,
): void {
  ipcMain.handle("yaomu:runtime:clean-wsl", async (event): Promise<RuntimeCleanupResult> => {
    authorization.requireSender(event);
    if (process.platform !== "win32") {
      return {
        ok: false,
        error: { code: "UNSUPPORTED", messageKey: "settings.storage.runtimeUnsupported", retryable: false },
      };
    }
    const check = await hasActiveTasks(invoke);
    if (check.error) return { ok: false, error: check.error };
    if (check.active) {
      return {
        ok: false,
        error: {
          code: "RESOURCE_BUSY",
          messageKey: "settings.storage.runtimeBusy",
          retryable: true,
        },
      };
    }
    try {
      await controller.stop();
      const result = await cleanYaomuWslRuntime({ userDataDir: app.getPath("userData") });
      return { ok: true, removed: result.removed, reason: result.reason };
    } catch {
      return {
        ok: false,
        error: {
          code: "RUNTIME_CLEANUP_FAILED",
          messageKey: "settings.storage.runtimeCleanupFailed",
          retryable: false,
        },
      };
    }
  });
}

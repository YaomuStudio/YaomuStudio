import { ref } from "vue";
import { api, ApiError } from "@/api";
import type { CreateWorkflowRunInput } from "@/api/types";
import type { Asset, PipelineNodeKey, PreflightIssue, Project, RunStatus, WorkflowRun } from "@/domain/types";
import {
  dedupePreflightIssues,
  displayErrorFields,
  type DisplayErrorFields,
} from "@/i18n/errorDisplay";

export type ToolRunPhase = "idle" | "importing" | "preflighting" | "running" | "done" | "failed";

const TERMINAL_RUN_STATUSES = new Set<RunStatus>([
  "SUCCEEDED",
  "PARTIAL",
  "FAILED",
  "CANCELED",
]);

const LAST_RUN_PREFIX = "yaomu.toolRun.";

function rememberLastRun(template: string | undefined, runId: string): void {
  if (!template) return;
  try {
    sessionStorage.setItem(`${LAST_RUN_PREFIX}${template}`, runId);
  } catch {
    // 会话存储不可用只影响“返回页面后回看”，不影响 Run 本身。
  }
}

function readLastRun(template: string): string | null {
  try {
    return sessionStorage.getItem(`${LAST_RUN_PREFIX}${template}`);
  } catch {
    return null;
  }
}

function forgetLastRun(template: string): void {
  try {
    sessionStorage.removeItem(`${LAST_RUN_PREFIX}${template}`);
  } catch {
    // 同上。
  }
}

export interface ToolRunOptions {
  projectName: string;
  /** 桌面桥授权的真实路径；浏览器 mock 可用 File.name。 */
  filePath: string;
  /** 不含 projectId/name/inputRevisionIds；由 composable 填充。 */
  payload: Omit<CreateWorkflowRunInput, "projectId" | "name" | "inputRevisionIds">;
  /** 导入成功后、preflight 前执行（如从 SRT Asset 建原文轨）。抛错则不创建 Run。 */
  afterImport?: (ctx: {
    projectId: string;
    assetIds: string[];
  }) => Promise<{ reuseSubtitleTrackId?: string } | void>;
  /**
   * 轮询到 WAITING_REVIEW 时调用；解析后继续轮询。审核等待不消耗运行超时。
   * 未提供时按普通轮询处理，超时失败。
   */
  waitForReview?: (ctx: { projectId: string; run: WorkflowRun }) => Promise<void>;
  pollMs?: number;
  timeoutMs?: number;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** 传输/核心暂时不可用可重试；业务拒绝（如 preflight、404）不能吞。 */
function isRetryable(err: unknown): boolean {
  return err instanceof ApiError && err.retryable;
}

/**
 * 读取某个节点最近一次成功 Task 的产物 Asset。
 * 以 Task.artifactIds 为准，避免把导入输入或历史资产误当成本次结果。
 */
export async function loadNodeArtifacts(
  runId: string,
  projectId: string,
  nodeKey: PipelineNodeKey,
): Promise<Asset[]> {
  const tasks = await api.listTasks({ runId, limit: 200 });
  const task = tasks.items.find(
    (item) => item.runId === runId && item.nodeKey === nodeKey && item.status === "SUCCEEDED",
  );
  if (!task || task.artifactIds.length === 0) return [];
  const project = await api.getProject(projectId);
  const byID = new Map(project.assets.map((asset) => [asset.id, asset]));
  return task.artifactIds
    .map((id) => byID.get(id))
    .filter((asset): asset is Asset => asset !== undefined);
}

/**
 * 读取某节点所有成功 Task 的产物（08 按语言各有一个 Task）。
 * 与 loadNodeArtifacts 的单 Task 语义分开，避免改变既有工具页结果。
 */
export async function loadAllNodeArtifacts(
  runId: string,
  projectId: string,
  nodeKey: PipelineNodeKey,
): Promise<Asset[]> {
  const tasks = await api.listTasks({ runId, limit: 200 });
  const project = await api.getProject(projectId);
  const byID = new Map(project.assets.map((asset) => [asset.id, asset]));
  const ids = new Set<string>();
  for (const task of tasks.items) {
    if (task.runId !== runId || task.nodeKey !== nodeKey || task.status !== "SUCCEEDED") continue;
    for (const id of task.artifactIds) ids.add(id);
  }
  return [...ids]
    .map((id) => byID.get(id))
    .filter((asset): asset is Asset => asset !== undefined);
}

/**
 * 工具页共享的 Core 流程：创建项目 → 导入 → preflight → 创建 Run → 轮询终态。
 * 只读取 Core 状态，不在本地推进节点或伪造成功。
 */
export function useToolRun() {
  const busy = ref(false);
  const phase = ref<ToolRunPhase>("idle");
  const projectId = ref("");
  const run = ref<WorkflowRun | null>(null);
  const issues = ref<PreflightIssue[]>([]);
  /** 只保存已知本地键或已解码的安全字段；渲染时由 errorText 解析，未知错误不落到原始 message。 */
  const error = ref<DisplayErrorFields | null>(null);
  /** PARTIAL 是终态但仍有已完成结果；页面据此显示部分完成警告。 */
  const partial = ref(false);

  async function pollRun(
    runId: string,
    pollMs: number,
    timeoutMs: number,
    waitForReview?: ToolRunOptions["waitForReview"],
  ): Promise<WorkflowRun | null> {
    let deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      let current: WorkflowRun;
      try {
        current = await api.getRun(runId);
      } catch (err) {
        // Core 是唯一业务状态权威：传输层/核心暂时不可用不代表任务失败。
        // 在超时预算内继续轮询，等 Core 恢复后读取真实终态。
        if (!isRetryable(err)) throw err;
        await delay(Math.min(pollMs * 2, 2_000));
        continue;
      }
      run.value = current;
      if (TERMINAL_RUN_STATUSES.has(current.status)) return current;
      if (current.status === "WAITING_REVIEW" && waitForReview) {
        await waitForReview({ projectId: projectId.value, run: current });
        // 人工审核不占运行超时；放行后重新计时，并留出 Core 推进的间隔。
        deadline = Date.now() + timeoutMs;
        await delay(pollMs);
        continue;
      }
      await delay(pollMs);
    }
    return null;
  }

  /**
   * 恢复该工具上一次已结束的 Run，供返回页面后回看进度与执行过程。
   * 运行中的 Run 不恢复：静态卡片会误导为仍在自动推进。
   */
  async function restore(template: string): Promise<WorkflowRun | null> {
    if (run.value) return run.value;
    const lastRunId = readLastRun(template);
    if (!lastRunId) return null;
    try {
      const previous = await api.getRun(lastRunId);
      if (!TERMINAL_RUN_STATUSES.has(previous.status)) return null;
      projectId.value = previous.projectId;
      run.value = previous;
      partial.value = previous.status === "PARTIAL";
      if (previous.status === "SUCCEEDED" || previous.status === "PARTIAL") {
        phase.value = "done";
      } else {
        phase.value = "failed";
        error.value = { messageKey: "tools.flow.runFailed" };
      }
      return previous;
    } catch {
      // 项目或 Run 已删除：忘掉这个会话记录，不把它当成业务错误。
      forgetLastRun(template);
      return null;
    }
  }

  async function start(opts: ToolRunOptions): Promise<{ project: Project; run: WorkflowRun } | null> {
    busy.value = true;
    phase.value = "importing";
    projectId.value = "";
    run.value = null;
    issues.value = [];
    error.value = null;
    partial.value = false;
    // 项目先建后跑：若 Run 最终没建起来（预检阻断、导入失败、调用异常），
    // 回滚刚创建的空项目，避免列表里留下无任务的孤儿卡片。
    let createdProjectId = "";
    let runStarted = false;
    const discardOrphanProject = async (): Promise<void> => {
      if (!createdProjectId || runStarted) return;
      const orphan = createdProjectId;
      createdProjectId = "";
      projectId.value = "";
      try {
        await api.deleteProject(orphan);
      } catch {
        // 回滚失败不掩盖原始错误；用户仍可在项目列表手动删除。
      }
    };
    try {
      const project = await api.createProject({ name: opts.projectName });
      createdProjectId = project.id;
      projectId.value = project.id;
      const imported = await api.importAssets({
        projectId: project.id,
        paths: [opts.filePath],
        copyMode: true,
      });
      if (imported.assetIds.length === 0) {
        error.value = { messageKey: "tools.flow.importEmpty" };
        phase.value = "failed";
        await discardOrphanProject();
        return null;
      }
      let afterImport: { reuseSubtitleTrackId?: string } | void | undefined;
      if (opts.afterImport) {
        afterImport = await opts.afterImport({ projectId: project.id, assetIds: imported.assetIds });
      }

      const payload: CreateWorkflowRunInput = {
        ...opts.payload,
        projectId: project.id,
        name: opts.projectName,
        inputRevisionIds: imported.assetIds,
        ...(afterImport?.reuseSubtitleTrackId
          ? { reuseSubtitleTrackId: afterImport.reuseSubtitleTrackId }
          : {}),
      };
      phase.value = "preflighting";
      const preflight = await api.preflight(payload);
      const uniqueIssues = dedupePreflightIssues(preflight.issues);
      issues.value = uniqueIssues;
      if (uniqueIssues.some((issue) => issue.severity === "blocker")) {
        error.value = { messageKey: "tools.flow.preflightBlocked" };
        phase.value = "failed";
        await discardOrphanProject();
        return null;
      }

      phase.value = "running";
      const created = await api.createWorkflowRun(payload);
      runStarted = true;
      run.value = created;
      rememberLastRun(payload.template, created.id);
      const final = await pollRun(
        created.id,
        opts.pollMs ?? 500,
        opts.timeoutMs ?? 60 * 60 * 1000,
        opts.waitForReview,
      );
      if (!final) {
        error.value = { messageKey: "tools.flow.timeout" };
        phase.value = "failed";
        return null;
      }
      if (final.status !== "SUCCEEDED" && final.status !== "PARTIAL") {
        error.value = { messageKey: "tools.flow.runFailed" };
        phase.value = "failed";
        return null;
      }
      partial.value = final.status === "PARTIAL";
      phase.value = "done";
      const fresh = await api.getProject(project.id);
      return { project: fresh, run: final };
    } catch (err) {
      error.value = displayErrorFields(err);
      phase.value = "failed";
      await discardOrphanProject();
      return null;
    } finally {
      busy.value = false;
    }
  }

  /** 连续创建：清空当前 Run 视图，但保留已创建的项目（可在项目页查看/删除）。 */
  function reset(): void {
    busy.value = false;
    phase.value = "idle";
    projectId.value = "";
    run.value = null;
    issues.value = [];
    error.value = null;
    partial.value = false;
  }

  return { busy, phase, projectId, run, issues, error, partial, start, restore, reset };
}

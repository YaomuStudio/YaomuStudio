import { expect, test, type Page } from "@playwright/test";
import {
  appendExecutionLog,
  holdExecutionLogRead,
  readLastExecutionLogTaskId,
  readMockCallCounters,
  releaseExecutionLogRead,
  SAMPLE_MEDIA,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

// 本分支没有任务中心（/tasks 重定向到项目），任务实时进度 UI 未实现；
// 这里保留仍在使用的执行日志存储、桥接映射与工具页日志面板回归。

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("execution log store rejects stale attempts and keeps current attempt lines", async ({
  page,
}) => {
  await page.goto("/#/");
  const lines = await page.evaluate(async () => {
    const module = (await import(/* @vite-ignore */ "/src/stores/executionLogs.ts")) as {
      useExecutionLogsStore: () => {
        clear: (taskId: string) => void;
        seed: (taskId: string, attempt: number, lines: unknown[]) => void;
        handleEvent: (event: unknown) => void;
        linesFor: (taskId: string) => { line: string }[];
      };
    };
    const store = module.useExecutionLogsStore();
    const taskId = "task-log-e2e";
    store.clear(taskId);
    store.seed(taskId, 1, [
      { seq: 1, stream: "runner", line: "attempt1 line1" },
      { seq: 2, stream: "runner", line: "attempt1 line2" },
    ]);
    store.handleEvent({
      schemaVersion: 1,
      seq: 0,
      type: "task.execution_log",
      taskId,
      data: {
        task_id: taskId,
        attempt: 2,
        stream: "runner",
        line: "attempt2 live",
        seq: 1,
      },
    });
    store.seed(taskId, 1, [{ seq: 3, stream: "runner", line: "attempt1 late replay" }]);
    store.handleEvent({
      schemaVersion: 1,
      seq: 0,
      type: "task.execution_log",
      taskId,
      data: {
        task_id: taskId,
        attempt: 1,
        stream: "runner",
        line: "attempt1 stale event",
        seq: 4,
      },
    });
    store.seed(taskId, 2, [{ seq: 1, stream: "runner", line: "attempt2 replay" }]);
    return store.linesFor(taskId).map((line) => line.line);
  });
  expect(lines).toEqual(["attempt2 replay"]);
});

test("bridge mapping keeps missing schema/seq/progress unknown instead of legal defaults", async ({
  page,
}) => {
  await page.goto("/#/");
  const result = await page.evaluate(async () => {
    const module = (await import(/* @vite-ignore */ "/src/api/bridge.ts")) as {
      createBridgeApi: (bridge: unknown) => {
        subscribeEvents: (callback: (event: unknown) => void) => () => void;
        listTasks: () => Promise<{ items: { progress?: number }[] }>;
      };
    };
    let emit: (event: unknown) => void = () => undefined;
    const rawTasks = {
      items: [
        {
          id: "task-a",
          project_id: "proj-a",
          kind: "node",
          status: "RUNNING",
          progress: 42,
          artifact_ids: [],
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "task-b",
          project_id: "proj-a",
          kind: "node",
          status: "RUNNING",
          progress: "not-a-percent",
          artifact_ids: [],
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "task-c",
          project_id: "proj-a",
          kind: "node",
          status: "RUNNING",
          artifact_ids: [],
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
    };
    const bridge = new Proxy(
      {},
      {
        get: (_target, property) => {
          if (property === "subscribeEvents") {
            return (callback: (event: unknown) => void) => {
              emit = callback;
              return () => undefined;
            };
          }
          if (property === "listTasks") {
            return () => Promise.resolve({ ok: true, data: rawTasks });
          }
          return () => Promise.resolve({ ok: true, data: {} });
        },
      },
    );
    const api = module.createBridgeApi(bridge);
    const events: Record<string, unknown>[] = [];
    api.subscribeEvents((event) => events.push(event as Record<string, unknown>));
    emit({
      type: "task.progress",
      project_id: "proj-a",
      task_id: "task-a",
      state_version: 3,
      data: { step_id: "node", attempt: 1, server_epoch: "1", progress: 42 },
    });
    const pageResult = await api.listTasks();
    return {
      schemaIsNaN: Number.isNaN(events[0]?.schemaVersion),
      seqIsNaN: Number.isNaN(events[0]?.seq),
      validProgress: pageResult.items[0]?.progress,
      invalidProgressUnknown: pageResult.items[1]?.progress === undefined,
      missingProgressUnknown: pageResult.items[2]?.progress === undefined,
    };
  });
  expect(result).toEqual({
    schemaIsNaN: true,
    seqIsNaN: true,
    validProgress: 42,
    invalidProgressUnknown: true,
    missingProgressUnknown: true,
  });
});

test("execution log panel keeps a newer attempt when the previous attempt replay returns late", async ({
  page,
}) => {
  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E stale log project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await expect(page.getByText(SAMPLE_MEDIA.name)).toBeVisible();

  await holdExecutionLogRead(page);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect
    .poll(async () => (await readMockCallCounters(page)).executionLogCalls ?? 0)
    .toBeGreaterThan(0);

  const taskId = await readLastExecutionLogTaskId(page);
  expect(taskId).not.toBe("");
  await appendExecutionLog(page, taskId, 2, "runner", "attempt2 current line");
  await releaseExecutionLogRead(page);

  await expect(page.getByText("attempt2 current line")).toBeVisible();
  await expect(page.getByText("启动 FlashCLI 分离")).toHaveCount(0);
  await expect(page.getByText("FlashCLI 分离完成")).toHaveCount(0);
});

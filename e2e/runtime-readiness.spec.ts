import { expect, test, type Page } from "@playwright/test";
import {
  emitRuntimeSnapshot,
  holdNextProjectRead,
  readApproveCalls,
  readBusinessCalls,
  readProjectReadCalls,
  readRuntimeRetryCalls,
  releaseProjectReadGate,
  releaseRuntimeRetry,
  releaseRuntimeSnapshot,
  releaseSaveSubtitle,
  runtimeSnapshot,
  seedHeldSaveSubtitle,
  seedMockControl,
  seedOnboardedSettings,
  seedProjectReadGate,
  seedRuntimeBridge,
  setProjectNameFixture,
} from "./support";

// 宿主 runtime 桥 E2E：浏览器没有 preload，用例在导航前注入显式 fixture。
// 业务数据仍由 VITE_E2E_FIXTURES 的测试夹具提供；这里只验证门控、顺序与恢复边界，
// 不把 fake runtime 当作生产数据，也不替代真实 Electron/Core 验收。
// 所有等待使用可释放的 promise 屏障，不使用固定延时。

/** 新连接：先 starting（沿用旧 generation），再 ready（新 generation）触发一次刷新。 */
async function reconnect(page: Page, revision: number, generation: number): Promise<void> {
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision, phase: "starting", generation: generation - 1 }),
  );
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: revision + 1, phase: "ready", generation }),
  );
}

async function readLastSavedRevision(page: Page): Promise<unknown> {
  return page.evaluate(() => {
    const control = (
      window as unknown as { __yaomuMock?: { lastSavedRevision?: unknown } }
    ).__yaomuMock;
    return control?.lastSavedRevision ?? null;
  });
}

async function readLastSavedRevisionId(page: Page): Promise<string> {
  return page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { lastSavedRevision?: { id?: string } };
      }
    ).__yaomuMock;
    return control?.lastSavedRevision?.id ?? "";
  });
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("shows the startup panel before first ready and ignores a late older snapshot", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "preparing" }),
    holdSnapshot: true,
  });

  await page.goto("/#/projects");

  const panel = page.getByTestId("runtime-startup-panel");
  await expect(panel).toBeVisible();
  await expect(page.getByTestId("runtime-startup-phase")).toHaveText(
    "Preparing the local environment",
  );
  // 业务页未挂载、未读取 Core。
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toHaveCount(0);
  expect(await readBusinessCalls(page)).toEqual([]);

  // ready 前本地主题控件可用。
  await panel.getByRole("tab", { name: "Dark" }).click();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.dataset.theme))
    .toBe("dark");

  // 先收到更新通知（revision 2 ready），再释放较旧的 getSnapshot 回包（revision 1）。
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: 2, phase: "ready", generation: 1 }),
  );
  await expect(page.getByTestId("runtime-startup-panel")).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();
  await expect
    .poll(async () => (await readBusinessCalls(page)).length)
    .toBeGreaterThan(0);

  await releaseRuntimeSnapshot(page);
  // 旧 preparing 快照不得回退状态或重新挂初始化面板。
  await expect(page.getByTestId("runtime-startup-panel")).toHaveCount(0);
  await expect(page.getByTestId("runtime-unavailable-banner")).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();
});

test("does not flash the startup panel when the host becomes ready quickly", async ({ page }) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "preparing" }),
    holdSnapshot: true,
  });
  // 记录首次挂载期间初始化面板是否出现过；延迟展示生效时应一次都不出现。
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as unknown as { __startupPanelSeen?: string[] }).__startupPanelSeen = seen;
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of Array.from(record.addedNodes)) {
          if (
            node instanceof Element &&
            (node.matches('[data-testid="runtime-startup-panel"]') ||
              node.querySelector('[data-testid="runtime-startup-panel"]') !== null)
          ) {
            seen.push("visible");
            return;
          }
        }
      }
    });
    // 观察 document：init script 阶段 documentElement 可能尚未可观察。
    observer.observe(document, { childList: true, subtree: true });
  });

  await page.goto("/#/");
  // 在初始化面板延迟阈值内就绪：业务页直接出现，面板从未挂载。
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: 2, phase: "ready", generation: 1 }),
  );
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  const seen = await page.evaluate(() => {
    const value = (window as unknown as { __startupPanelSeen?: string[] }).__startupPanelSeen;
    return Array.isArray(value) ? value : null;
  });
  expect(seen).toEqual([]);
  await releaseRuntimeSnapshot(page);
});

test("clears the Core-ready status as soon as the host leaves ready", async ({ page }) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });

  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  const coreStatus = page.locator(".statusbar__item").first();
  await expect(coreStatus).toContainText("Local service ready");

  // 只进入 restarting（未产生新的 ready generation，也没有业务读失败）：
  // banner/状态栏必须同时立即显示不可用。
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: 2, phase: "restarting", generation: 1 }),
  );
  await expect(page.getByTestId("runtime-unavailable-banner")).toBeVisible();
  await expect(coreStatus).toContainText("Local service offline");

  // 新连接 ready 后由真实的 capabilities/system 读回恢复。
  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: 3, phase: "ready", generation: 2 }),
  );
  await expect(page.getByTestId("runtime-unavailable-banner")).toHaveCount(0);
  await expect(coreStatus).toContainText("Local service ready");
});

test("shows a safe failed state and retries once through the host bridge", async ({ page }) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({
      revision: 1,
      phase: "failed",
      reason: "startup_failed",
      canRetry: true,
    }),
    holdRetry: true,
    retrySnapshot: runtimeSnapshot({ revision: 2, phase: "starting" }),
  });

  await page.goto("/#/");

  const panel = page.getByTestId("runtime-startup-panel");
  await expect(panel).toBeVisible();
  await expect(page.getByTestId("runtime-startup-phase")).toHaveText("Startup failed");
  await expect(page.getByTestId("runtime-startup-reason")).toContainText(
    "The local service could not start.",
  );

  const retry = page.getByTestId("runtime-startup-retry");
  await expect(retry).toBeEnabled();
  await retry.click();
  // 重试进行中只有一个在飞请求：按钮 loading/disabled，重复点击不会新开启动。
  await expect(retry).toBeDisabled();
  expect(await readRuntimeRetryCalls(page)).toBe(1);

  await releaseRuntimeRetry(page);
  await expect(page.getByTestId("runtime-startup-phase")).toHaveText(
    "Starting the local service",
  );
  await expect(page.getByTestId("runtime-startup-retry")).toHaveCount(0);

  await emitRuntimeSnapshot(
    page,
    runtimeSnapshot({ revision: 3, phase: "ready", generation: 1 }),
  );
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  expect(await readRuntimeRetryCalls(page)).toBe(1);
});

test("keeps dirty subtitle drafts and the base revision when the host reconnects", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });

  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByText("星际译制 · 试播集")).toBeVisible();
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Runtime draft");
  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(1);

  await reconnect(page, 2, 2);

  await expect(page.getByTestId("runtime-recovery-notice")).toBeVisible();
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Runtime draft");
  // 不自动保存、不自动放行审核。
  expect(await readLastSavedRevision(page)).toBeNull();
  expect(await readApproveCalls(page)).toBe(0);
});

test("keeps a held save and does not auto-submit when the host reconnects", async ({ page }) => {
  await seedHeldSaveSubtitle(page);
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });

  await page.goto("/#/projects/proj-demo/correction");
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Held runtime draft");
  await page.getByRole("button", { name: /^Save( corrections)?$/ }).click();
  await expect(page.getByText("Saving…", { exact: true })).toBeVisible();

  await reconnect(page, 2, 2);
  await expect(page.getByTestId("runtime-recovery-notice")).toBeVisible();
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Held runtime draft");
  expect(await readLastSavedRevision(page)).toBeNull();

  await releaseSaveSubtitle(page);
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  expect(await readLastSavedRevision(page)).not.toBeNull();
  await expect(page.getByTestId("runtime-recovery-notice")).toHaveCount(0);
  // 显式保存成功后仍必须 hydrate 新 revision（force 只绕过 dirty，不绕过代次）。
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Held runtime draft");
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
});

test("ManualCorrection save refresh invalidated by a new generation ends loading and keeps the draft", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await page.goto("/#/projects/proj-demo/correction");
  const firstRow = page.getByTestId("segment-row").first();
  await expect(firstRow.getByTestId("segment-translation")).toBeVisible();
  await firstRow.getByTestId("segment-translation").fill("Save then reconnect draft");

  const readsBefore = await readProjectReadCalls(page);
  await holdNextProjectRead(page);
  await page.getByRole("button", { name: /^Save( corrections)?$/ }).click();
  // save PUT 成功后才发起的强制 GET 已被挂起。
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(readsBefore);
  const savedBefore = await readLastSavedRevisionId(page);
  expect(savedBefore).not.toBe("");

  // runtime 跨 generation：挂起的刷新 token 失效。
  await reconnect(page, 2, 2);
  await releaseProjectReadGate(page);

  // 不得永久 loading；原草稿仍可见，基准未被假同步。
  await expect(page.getByText("Loading correction workspace…")).toHaveCount(0);
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Save then reconnect draft");
  await expect(page.getByTestId("save-refresh-pending")).toBeVisible();
  await expect(page.locator(".mcs-status")).toHaveText(/Reload required/);
  // PUT 只成功一次，未自动重放。
  expect(await readLastSavedRevisionId(page)).toBe(savedBefore);
});

test("Pipeline save refresh invalidated by a new generation reports pending refresh", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await page.goto("/#/projects/proj-demo/pipeline");
  await expect(
    page.getByRole("heading", { level: 2, name: "Subtitle correction" }),
  ).toBeVisible();
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.locator("textarea").fill("Pipeline save then reconnect draft");

  const readsBefore = await readProjectReadCalls(page);
  await holdNextProjectRead(page);
  await page.getByRole("button", { name: "Save corrections" }).click();
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(readsBefore);
  const savedBefore = await readLastSavedRevisionId(page);
  expect(savedBefore).not.toBe("");

  await reconnect(page, 2, 2);
  await releaseProjectReadGate(page);

  // 不得把未应用的刷新当成完整同步。
  await expect(page.getByTestId("save-refresh-pending")).toBeVisible();
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toHaveCount(0);
  await expect(firstRow.locator("textarea")).toHaveValue("Pipeline save then reconnect draft");
  await expect(page.getByText("Unsaved corrections", { exact: true })).toBeVisible();
  expect(await readLastSavedRevisionId(page)).toBe(savedBefore);
});

test("does not hydrate a stale ManualCorrection recovery read over a new draft", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await page.goto("/#/projects/proj-demo/correction");
  const firstRow = page.getByTestId("segment-row").first();
  await expect(firstRow.getByTestId("segment-translation")).toBeVisible();

  const readsBefore = await readProjectReadCalls(page);
  await holdNextProjectRead(page);
  await reconnect(page, 2, 2);
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(readsBefore);

  // 后台恢复读取在途时用户开始编辑；编辑器保持挂载。
  await firstRow.getByTestId("segment-translation").fill("Recovered race draft");
  await releaseProjectReadGate(page);

  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Recovered race draft");
  await expect(page.getByTestId("runtime-recovery-notice")).toBeVisible();
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("does not hydrate a stale Pipeline refresh over a new draft", async ({ page }) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await page.goto("/#/projects/proj-demo/pipeline");
  await expect(
    page.getByRole("heading", { level: 2, name: "Subtitle correction" }),
  ).toBeVisible();
  const firstRow = page.getByTestId("segment-row").first();
  await expect(firstRow.locator("textarea")).toBeVisible();

  const readsBefore = await readProjectReadCalls(page);
  await holdNextProjectRead(page);
  await reconnect(page, 2, 2);
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(readsBefore);

  await firstRow.locator("textarea").fill("Pipeline recovered race draft");
  await releaseProjectReadGate(page);

  await expect(firstRow.locator("textarea")).toHaveValue("Pipeline recovered race draft");
  await expect(page.getByTestId("runtime-recovery-notice")).toBeVisible();
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("discards an initial pre-reconnect ManualCorrection read after a new generation", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await seedProjectReadGate(page, "Snapshot A");

  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByText("Loading correction workspace…")).toBeVisible();
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(0);

  await setProjectNameFixture(page, "Snapshot B");
  await reconnect(page, 2, 2);
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot B" })).toBeVisible();
  await expect(page.getByTestId("segment-row")).toHaveCount(8);

  // 旧代初次读取晚回：不得把 Snapshot A 或旧编辑器写回当前 generation。
  await releaseProjectReadGate(page);
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot B" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot A" })).toHaveCount(0);
  await expect(page.getByTestId("segment-row")).toHaveCount(8);
});

test("discards an initial pre-reconnect Pipeline read after a new generation", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await seedProjectReadGate(page, "Snapshot A");

  await page.goto("/#/projects/proj-demo/pipeline");
  await expect(page.getByText("Loading project…").first()).toBeVisible();
  await expect.poll(() => readProjectReadCalls(page)).toBeGreaterThan(0);

  await setProjectNameFixture(page, "Snapshot B");
  await reconnect(page, 2, 2);
  await expect(page.locator(".project-select .ym-select__value")).toHaveText("Snapshot B");
  await expect(page.getByTestId("segment-row")).toHaveCount(8);

  await releaseProjectReadGate(page);
  await expect(page.locator(".project-select .ym-select__value")).toHaveText("Snapshot B");
  await expect(page.getByTestId("segment-row")).toHaveCount(8);
});

test("rejects malformed runtime snapshots without touching store state", async ({ page }) => {
  await page.goto("/#/");
  const result = await page.evaluate(async () => {
    const module = await import("/src/stores/runtime.ts");
    const base = {
      schemaVersion: 1,
      revision: 1,
      generation: 0,
      phase: "preparing",
      reason: null,
      canRetry: false,
    };
    return {
      valid: module.parseRuntimeSnapshot(base) !== null,
      extraField: module.parseRuntimeSnapshot({ ...base, extra: true }) === null,
      illegalPhase: module.parseRuntimeSnapshot({ ...base, phase: "booting" }) === null,
      lowRevision: module.parseRuntimeSnapshot({ ...base, revision: 0 }) === null,
      illegalCanRetry: module.parseRuntimeSnapshot({ ...base, canRetry: "no" }) === null,
      nonFailedReason: module.parseRuntimeSnapshot({ ...base, reason: "startup_failed" }) === null,
      retryableCleanup:
        module.parseRuntimeSnapshot({
          ...base,
          phase: "failed",
          reason: "cleanup_unconfirmed",
          canRetry: true,
        }) === null,
      readyWithoutGeneration:
        module.parseRuntimeSnapshot({ ...base, phase: "ready", generation: 0 }) === null,
    };
  });
  expect(result).toMatchObject({
    valid: true,
    extraField: true,
    illegalPhase: true,
    lowRevision: true,
    illegalCanRetry: true,
    nonFailedReason: true,
    retryableCleanup: true,
    readyWithoutGeneration: true,
  });
});

test("refreshes ProjectDetail on a new connection and rejects the stale read", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });
  await seedProjectReadGate(page, "Snapshot A");

  await page.goto("/#/projects/proj-demo");
  // 第一次 getProject 被挂起：页面仍在加载，没有旧数据可回退。
  await expect(page.getByText("Loading project…")).toBeVisible();

  await setProjectNameFixture(page, "Snapshot B");
  await reconnect(page, 2, 2);
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot B" })).toBeVisible();

  // 释放旧 generation 的读取：不得把 Snapshot A 写回当前页面。
  await releaseProjectReadGate(page);
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot B" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Snapshot A" })).toHaveCount(0);

  // 重命名对话框草稿是本地状态：切到列表后再次重连不得重挂对话框或覆盖输入。
  await page.goto("/#/projects");
  const renameCard = page
    .locator("article.recent-card")
    .filter({ hasText: "Snapshot B" });
  await renameCard.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByTestId("project-rename-input").fill("Local draft");
  await reconnect(page, 4, 3);
  await expect(page.getByTestId("project-rename-input")).toHaveValue("Local draft");
});

test("keeps dirty Pipeline drafts and refreshes a clean editor without remounting", async ({
  page,
}) => {
  await seedRuntimeBridge(page, {
    initial: runtimeSnapshot({ revision: 1, phase: "ready", generation: 1 }),
  });

  await page.goto("/#/projects/proj-demo/pipeline");
  await expect(
    page.getByRole("heading", { level: 2, name: "Subtitle correction" }),
  ).toBeVisible();
  await expect(page.getByTestId("segment-row")).toHaveCount(8);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.locator("textarea").fill("Pipeline runtime draft");
  await reconnect(page, 2, 2);

  await expect(page.getByTestId("runtime-recovery-notice")).toBeVisible();
  await expect(firstRow.locator("textarea")).toHaveValue("Pipeline runtime draft");
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("keeps the existing load path for a complete bridge without runtime", async ({ page }) => {
  // 旧完整 preload：有业务 api、没有 runtime；必须按原路径立即加载，不无限等待。
  await page.addInitScript(() => {
    const ok = (data: unknown) => ({ ok: true, data });
    const bridge = new Proxy(
      {},
      {
        get(_target, property) {
          if (property === "subscribeEvents") return () => () => {};
          return async () => ok({});
        },
      },
    );
    const scope = window as unknown as { yaomu?: Record<string, unknown> };
    scope.yaomu = { platform: "browser", update: {}, api: bridge };
  });

  await page.goto("/#/");
  await expect(page.getByTestId("runtime-startup-panel")).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
});

test("keeps test fixtures without a runtime bridge on the immediate load path", async ({ page }) => {
  await page.goto("/#/");
  await expect(page.getByTestId("runtime-startup-panel")).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  const mode = await page.evaluate(async () => {
    const module = await import("/src/api/index.ts");
    return module.apiMode;
  });
  expect(mode).toBe("fixtures");
});

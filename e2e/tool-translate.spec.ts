import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_SRT,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

interface RunInputProbe {
  template?: string;
  sourceLanguage?: string;
  targetLanguages?: string[];
  inputRevisionIds?: string[];
}

interface ImportTrackProbe {
  projectId?: string;
  assetId?: string;
  language?: string;
}

interface ExportInputProbe {
  runId?: string;
  kind?: string;
  format?: string;
  inputRevisionId?: string;
}

async function openTranslate(page: Page): Promise<void> {
  await page.goto("/#/tools/translate");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle translation" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E translate project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_SRT);
  await expect(page.getByText(SAMPLE_SRT.name)).toBeVisible();
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "简体中文", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
}

async function createRun(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create" }).click();
}

async function probe<T>(page: Page, key: string): Promise<T | null> {
  return page.evaluate((name) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    return (control?.[name] as T | undefined) ?? null;
  }, key);
}

async function setMockFlag(page: Page, key: string): Promise<void> {
  await page.evaluate((name) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control[name] = true;
  }, key);
}

async function setMockNumber(page: Page, key: string, value: number): Promise<void> {
  await page.evaluate(
    ({ name, val }) => {
      const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
      if (control) control[name] = val;
    },
    { name: key, val: value },
  );
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("imports an SRT, translates it, and exports without a run gate", async ({ page }) => {
  await openTranslate(page);
  await createRun(page);

  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible();
  await expect(page.getByText("Translated zh-CN line two.")).toBeVisible();

  const imported = await probe<ImportTrackProbe>(page, "lastImportTrackInput");
  expect(imported?.language).toBe("en");
  expect(imported?.assetId).toBeTruthy();

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.template).toBe("tool_translate");
  expect(input?.sourceLanguage).toBe("en");
  expect(input?.targetLanguages).toEqual(["zh-CN"]);
  expect(input?.inputRevisionIds?.length).toBe(1);

  await page.getByRole("button", { name: "Export SRT" }).click();
  await expect(page.getByText(/Exported to outputs\/.*\.srt/)).toBeVisible();
  const exported = await probe<ExportInputProbe>(page, "lastExportInput");
  expect(exported?.kind).toBe("subtitle");
  expect(exported?.format).toBe("srt");
  expect(exported?.runId).toBeUndefined();
  expect(exported?.inputRevisionId).toBeTruthy();
});

test("keeps polling after a retryable core read failure", async ({ page }) => {
  await openTranslate(page);
  await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control.getRunFailures = 1;
  });
  await createRun(page);

  // 一次短暂的 Core 读取失败不代表任务失败；轮询应继续到真实终态。
  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible();
});

test("shows completed results and a warning for a partial run", async ({ page }) => {
  await openTranslate(page);
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "日本語", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
  await setMockFlag(page, "toolRunPartial");
  await createRun(page);

  await expect(page.getByText("Task partially completed. Completed results are shown below.")).toBeVisible();
  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible();
  await expect(page.getByText("Translation finished but one or more target tracks were missing.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Export SRT" })).toBeVisible();
});

test("shows elapsed time and an active marker while translation is running", async ({ page }) => {
  await openTranslate(page);
  await setMockNumber(page, "toolRunDelayMs", 5000);
  await createRun(page);

  // 云端翻译没有逐句进度：运行中的节点不能只显示冻结的 0%，必须能看出仍在处理。
  await expect(page.getByText(/Running for \d+:\d{2}/)).toBeVisible();
  await expect(page.getByText("Working…", { exact: true })).toBeVisible();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect(page.getByText("启动 mlstools 字幕翻译")).toBeVisible();

  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText(/Running for \d+:\d{2}/)).toHaveCount(0);
  await expect(page.getByText("mlstools 字幕翻译完成")).toBeVisible();
});

test("does not claim translation finished when a partial run produced no tracks", async ({ page }) => {
  await openTranslate(page);
  await setMockFlag(page, "toolRunPartialNoTracks");
  await createRun(page);

  await expect(
    page.getByText(
      "Translation did not finish: no target-language track was created. Check the translation service credentials or the execution log, then retry.",
    ),
  ).toBeVisible();
  // 一个语种都没产出时，不能同时显示“部分完成/已完成结果”或“翻译完成但缺轨”。
  await expect(
    page.getByText("Task partially completed. Completed results are shown below."),
  ).toHaveCount(0);
  await expect(
    page.getByText("Translation finished but one or more target tracks were missing."),
  ).toHaveCount(0);
  await expect(page.getByText("Translated zh-CN line one.")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Export SRT" })).toHaveCount(0);
});

test("does not show translations when the run fails", async ({ page }) => {
  await openTranslate(page);
  await setMockFlag(page, "toolRunFailure");
  await createRun(page);

  await expect(page.getByText("The task did not succeed.")).toBeVisible();
  await expect(page.getByText("Translated zh-CN line one.")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Export SRT" })).toHaveCount(0);
});

test("keeps translations visible when export fails", async ({ page }) => {
  await openTranslate(page);
  await createRun(page);
  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible();

  await setMockFlag(page, "exportFailure");
  await page.getByRole("button", { name: "Export SRT" }).click();

  await expect(page.getByText("Export failed.")).toBeVisible();
  await expect(page.getByText("Translated zh-CN line one.")).toBeVisible();
});

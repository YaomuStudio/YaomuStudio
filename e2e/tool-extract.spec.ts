import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_MEDIA,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

interface RunInputProbe {
  template?: string;
  sourceLanguage?: string;
  targetLanguages?: string[];
  inputRevisionIds?: string[];
  extract?: { mode?: string; ocrLanguage?: string };
}

interface ExportInputProbe {
  runId?: string;
  kind?: string;
  format?: string;
  inputRevisionId?: string;
}

async function openExtract(page: Page): Promise<void> {
  await page.goto("/#/tools/extract");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle extraction" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E extract project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await expect(page.getByText(SAMPLE_MEDIA.name)).toBeVisible();
}

async function createRun(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create" }).click();
}

async function runInput(page: Page): Promise<RunInputProbe | null> {
  return page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { lastRunInput?: RunInputProbe } }).__yaomuMock;
    return control?.lastRunInput ?? null;
  });
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

test("extracts a source into a transcription track and completes an SRT export", async ({ page }) => {
  await openExtract(page);
  await createRun(page);
  // 提取工具的 02 分析在本版本由 Core 跳过，进度里不应出现“Analyze / 跳过”。
  await expect(page.locator(".node-row").filter({ hasText: "Analyze" })).toHaveCount(0);
  await expect(page.locator(".node-row").first()).toContainText("Import");

  await expect(page.getByText("Extracted line one.")).toBeVisible();
  await expect(page.getByText("Extracted line two.")).toBeVisible();
  await expect(page.getByText("00:00:00,000 → 00:00:01,600")).toBeVisible();

  const input = await runInput(page);
  expect(input?.template).toBe("tool_extract");
  expect(input?.sourceLanguage).toBe("zh");
  expect(input?.targetLanguages).toEqual([]);
  expect(input?.inputRevisionIds?.length).toBe(1);
  expect(input?.extract?.mode).toBe("asr");
  expect(input?.extract?.ocrLanguage).toBeUndefined();

  await page.getByRole("button", { name: "Export SRT" }).click();
  await expect(page.getByText(/^Exported to /)).toBeVisible();

  const exportInput = await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { lastExportInput?: ExportInputProbe } }).__yaomuMock;
    return control?.lastExportInput ?? null;
  });
  expect(exportInput?.kind).toBe("subtitle");
  expect(exportInput?.format).toBe("srt");
  // 提取工具导出是无 Run 的工具导出：不传 run_id，避免无 08 的 Run 触发交付审核。
  expect(exportInput?.runId).toBeUndefined();
  expect(exportInput?.inputRevisionId).toBeTruthy();
});

test("maps OCR mode to the OCR language fields", async ({ page }) => {
  await openExtract(page);
  await page.locator("label.ym-radio__item").filter({ hasText: "OCR on-screen text" }).click();
  await createRun(page);
  await expect(page.getByText("Extracted line one.")).toBeVisible();

  const input = await runInput(page);
  expect(input?.sourceLanguage).toBe("chinese");
  expect(input?.extract).toEqual({ mode: "ocr", ocrLanguage: "chinese", ocrQuality: "fine" });
});

test("maps ASR + OCR mode to ISO source and OCR language", async ({ page }) => {
  await openExtract(page);
  await page.locator("label.ym-radio__item").filter({ hasText: "ASR + OCR smart correction" }).click();
  await createRun(page);
  await expect(page.getByText("Extracted line one.")).toBeVisible();

  const input = await runInput(page);
  expect(input?.sourceLanguage).toBe("zh");
  expect(input?.extract).toEqual({ mode: "asr_ocr", ocrLanguage: "chinese", ocrQuality: "fine" });
});

test("shows an execution log while ASR extraction is running", async ({ page }) => {
  await openExtract(page);
  await setMockNumber(page, "toolRunDelayMs", 5000);
  await createRun(page);

  // ASR 长时间没有逐句进度：运行节点与执行过程必须能看出仍在处理。
  await expect(page.getByText("Working…", { exact: true })).toBeVisible();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect(page.getByText("启动 FlashCLI ASR")).toBeVisible();
  // 结果列是 flex 布局：卡片不得被压缩到 overflow 裁掉日志正文。
  expect(
    await page.locator(".execution-log__body").evaluate((el) => {
      const card = el.closest(".ym-card");
      return card ? card.scrollHeight > card.clientHeight + 1 : true;
    }),
  ).toBe(false);

  await expect(page.getByText("Extracted line one.")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("ASR 完成，生成 2 条字幕")).toBeVisible();
});

test("restores the last run and its execution log after leaving the page", async ({ page }) => {
  await openExtract(page);
  await createRun(page);
  await expect(page.getByText("ASR 完成，生成 2 条字幕")).toBeVisible();

  // 工具页只在当前页面会话里持有 Run；离开再返回时必须从 Core 恢复最近一次已结束的 Run，
  // 否则执行过程只在 cache 文件里，界面看不到。
  await page.evaluate(() => {
    location.hash = "#/";
  });
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  await page.evaluate(() => {
    location.hash = "#/tools/extract";
  });

  await expect(page.getByRole("heading", { level: 1, name: "Subtitle extraction" })).toBeVisible();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect(page.getByText("ASR 完成，生成 2 条字幕")).toBeVisible();
});

test("shows the worker failure when extraction ends without a transcription track", async ({ page }) => {
  await openExtract(page);
  await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { toolExtractPartialFailure?: boolean } }).__yaomuMock;
    if (control) control.toolExtractPartialFailure = true;
  });
  await createRun(page);

  await expect(page.getByText("The worker failed. Check the execution log, then retry.")).toBeVisible();
  await expect(page.getByText("The task finished but no transcription track was returned.")).toHaveCount(0);
  await expect(page.getByText("Extracted line one.")).toHaveCount(0);
});

test("does not show a result when the tool run fails", async ({ page }) => {
  await openExtract(page);
  await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { toolRunFailure?: boolean } }).__yaomuMock;
    if (control) control.toolRunFailure = true;
  });
  await createRun(page);

  await expect(page.getByText("The task did not succeed.")).toBeVisible();
  await expect(page.getByText("Extracted line one.")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Export SRT" })).toHaveCount(0);
});

test("shows an export failure without claiming success", async ({ page }) => {
  await openExtract(page);
  await createRun(page);
  await expect(page.getByText("Extracted line one.")).toBeVisible();

  await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { exportFailure?: boolean } }).__yaomuMock;
    if (control) control.exportFailure = true;
  });
  await page.getByRole("button", { name: "Export SRT" }).click();
  await expect(page.getByText("Export failed.")).toBeVisible();
  await expect(page.getByText(/^Exported to /)).toHaveCount(0);
});

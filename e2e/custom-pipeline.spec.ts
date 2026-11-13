import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_AUDIO,
  SAMPLE_MEDIA,
  SAMPLE_SRT,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

interface RunInputProbe {
  template?: string;
  features?: string[];
  sourceLanguage?: string;
  targetLanguages?: string[];
  reuseSubtitleTrackId?: string;
  /** Removed field; kept on the probe type only to assert it is absent. */
  voiceAuthorization?: boolean;
  eraseMode?: string;
  validateTimestamps?: boolean;
  adjustDuration?: boolean;
}

interface ImportTrackProbe {
  type?: string;
  language?: string;
}

async function openCustom(page: Page): Promise<void> {
  await page.goto("/#/tools/custom");
  await expect(page.getByRole("heading", { level: 1, name: "Custom pipeline" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E custom project");
  await page.locator('input[accept^="video/*"]').setInputFiles(SAMPLE_MEDIA);
}

function feature(page: Page, label: string) {
  return page.locator("label.ym-checkbox").filter({ hasText: label });
}

async function chooseTarget(page: Page, label: string): Promise<void> {
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: label, exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
}

async function probe<T>(page: Page, key: string): Promise<T | null> {
  return page.evaluate((name) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    return (control?.[name] as T | undefined) ?? null;
  }, key);
}

async function setMockValue(page: Page, key: string, value: unknown): Promise<void> {
  await page.evaluate(({ name, val }) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control[name] = val;
  }, { name: key, val: value });
}


test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("runs translate+mix with reviews", async ({ page }) => {
  await openCustom(page);
  await feature(page, "Subtitle translation").click();
  await feature(page, "Mixed delivery").click();
  await chooseTarget(page, "简体中文");
  await page.getByRole("button", { name: "Create" }).click();

  await expect(page.getByText("Translation review")).toBeVisible();
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Delivery review")).toBeVisible();
  await page.getByRole("button", { name: "Approve" }).click();

  await expect(page.getByText("Mixed videos")).toBeVisible();
  await expect(page.getByText("zh-CN-mix.mp4")).toBeVisible();
  await expect(page.getByText("2 lines")).toBeVisible();

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.template).toBe("custom_pipeline");
  expect(input?.features).toEqual(["translate", "mix"]);
  // 源语言未手动设置且没有原文字幕时自动识别，不提交显式语言。
  expect(input?.sourceLanguage).toBeUndefined();
  expect(input?.targetLanguages).toEqual(["zh-CN"]);

  const mixRow = page.locator(".asset-row").filter({ hasText: "zh-CN-mix.mp4" });
  await mixRow.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(/Saved to ~\/Downloads\//)).toBeVisible();
  expect(await probe(page, "lastSaveAssetInput")).not.toBeNull();
});

test("uses imported source and translated subtitles without a translation review", async ({ page }) => {
  await openCustom(page);
  await feature(page, "Subtitle translation").click();
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles(SAMPLE_SRT);
  await page.locator(".file-row__lang .ym-select__trigger").click();
  await page.locator('.ym-select__option[data-value="zh-CN"]').click();
  await page.getByRole("button", { name: "Create" }).click();

  await expect(page.getByRole("button", { name: "Export SRT" })).toBeVisible();
  await expect(page.locator(".asset-row__name").filter({ hasText: "简体中文" })).toBeVisible();
  await expect(page.getByText("Translation review")).toHaveCount(0);

  const imports = await probe<ImportTrackProbe[]>(page, "lastImportTrackInputs");
  expect(imports?.map((item) => `${item.type}:${item.language}`)).toEqual([
    "transcription:en",
    "translation:zh-CN",
  ]);
  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.reuseSubtitleTrackId).toBeTruthy();
});

test("creates clone without an authorization step", async ({ page }) => {
  await openCustom(page);
  await feature(page, "High-emotion cloned dubbing").click();
  await chooseTarget(page, "简体中文");
  await page.getByRole("button", { name: "Create" }).click();

  await expect
    .poll(async () => (await probe<RunInputProbe>(page, "lastRunInput"))?.template)
    .toBe("custom_pipeline");
  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.features).toEqual(["translate", "clone"]);
  expect(input?.voiceAuthorization).toBeUndefined();
});

test("rejects audio input for erase with a readable error", async ({ page }) => {
  await page.goto("/#/tools/custom");
  await page.getByPlaceholder("Project name").fill("E2E custom audio erase");
  await page.locator('input[accept^="video/*"]').setInputFiles(SAMPLE_AUDIO);
  await feature(page, "Subtitle erase").click();
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Preflight found a blocker. Fix it and try again.")).toBeVisible();
  expect(await probe<RunInputProbe>(page, "lastRunInput")).toBeNull();
});

test("keeps duration adjustment off by default and separates frozen run options", async ({ page }) => {
  await openCustom(page);
  await setMockValue(page, "customSkipReview", true);
  await feature(page, "Subtitle translation").click();
  await chooseTarget(page, "简体中文");

  const adjust = page.getByRole("checkbox", { name: "Dynamic duration adjustment" });
  const validate = page.getByRole("checkbox", { name: "Validate subtitle timestamps" });
  await expect(adjust).not.toBeChecked();
  await expect(adjust).toBeDisabled();
  await expect(validate).not.toBeChecked();

  await validate.check({ force: true });
  await page.getByRole("button", { name: "Create" }).click();
  await expect
    .poll(async () => (await probe<RunInputProbe>(page, "lastRunInput"))?.template)
    .toBe("custom_pipeline");

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.validateTimestamps).toBe(true);
  expect(input?.adjustDuration).toBe(false);

  const frozen = page.getByTestId("run-options");
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("On");
  await expect(frozen.locator('[data-option="adjustDuration"]')).toContainText("Off");

  // 当前表单切换不替代已创建 Run 的冻结摘要。
  await validate.uncheck({ force: true });
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("On");
});


test("mix exposes subtitle style settings and submits them", async ({ page }) => {
  await openCustom(page);
  await feature(page, "Subtitle translation").click();
  await feature(page, "Mixed delivery").click();
  await chooseTarget(page, "简体中文");

  // 冗余说明已移除，避免重复解释管线顺序与自动包含节点。
  await expect(page.getByText("Select one or more steps; they run in pipeline order.")).toHaveCount(0);
  await expect(page.getByText("Will include:")).toHaveCount(0);

  const style = page.locator(".style-panel");
  await expect(style.getByText("Subtitle style")).toBeVisible();
  await style.locator("label").filter({ hasText: "Font size" }).locator("input").fill("42");
  await style.locator("label").filter({ hasText: "Horizontal position (0–1)" }).locator("input").fill("0.35");
  await style.locator("label").filter({ hasText: "Vertical position (0–1)" }).locator("input").fill("0.8");

  await page.getByRole("button", { name: "Create" }).click();
  await expect.poll(async () => (await probe<RunInputProbe>(page, "lastRunInput"))?.template).toBe(
    "custom_pipeline",
  );
  const input = await probe<RunInputProbe & { subtitleStyle?: Record<string, unknown> }>(
    page,
    "lastRunInput",
  );
  expect(input?.subtitleStyle).toMatchObject({ fontSize: 42, x: 0.35, y: 0.8 });
});

test("auto-detects the source language without a manual field", async ({ page }) => {
  await openCustom(page);
  await feature(page, "Subtitle translation").click();
  await chooseTarget(page, "ไทย");

  // 源语言不再由用户设置；上传字幕后按内容自动识别。
  await expect(page.getByText("Source language", { exact: true })).toHaveCount(0);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles({
    name: "wsy泰语cs_原文字幕.srt",
    mimeType: "application/x-subrip",
    buffer: Buffer.from("1\n00:00:00,000 --> 00:00:01,000\n你好，这是原文字幕。\n"),
  });
  await expect(page.getByText("Detected language: 简体中文")).toBeVisible();

  await page.getByRole("button", { name: "Create" }).click();
  await expect
    .poll(async () => (await probe<RunInputProbe>(page, "lastRunInput"))?.template)
    .toBe("custom_pipeline");
  const input = await probe<RunInputProbe>(page, "lastRunInput");
  // 内容识别出的源语言随请求提交，用于原文轨与翻译对齐。
  expect(input?.sourceLanguage).toBe("zh-CN");
});

test("locks subtitle translation while clone or mix is selected", async ({ page }) => {
  await openCustom(page);
  const translate = feature(page, "Subtitle translation");
  const clone = feature(page, "High-emotion cloned dubbing");
  const mix = feature(page, "Mixed delivery");

  await mix.click();
  await expect(translate.locator("input")).toBeChecked();
  await expect(translate.locator("input")).toBeDisabled();
  await expect(
    page.getByText(
      "Required by cloned dubbing and mixed delivery; enabled automatically.",
    ),
  ).toBeVisible();

  // 依赖存续期间不能把字幕翻译单独取消，也不带走用户的主选择。
  await expect(translate.locator("input")).toBeChecked();
  await expect(mix.locator("input")).toBeChecked();

  await mix.click();
  await expect(translate.locator("input")).toBeEnabled();
  await translate.click();
  await expect(translate.locator("input")).not.toBeChecked();

  await clone.click();
  await expect(translate.locator("input")).toBeChecked();
  await expect(translate.locator("input")).toBeDisabled();
});

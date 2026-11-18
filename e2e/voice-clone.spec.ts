import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_AUDIO,
  SAMPLE_SRT,
  SAMPLE_TRANSLATED_SRT,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

interface RunInputProbe {
  template?: string;
  sourceLanguage?: string;
  targetLanguages?: string[];
  validateTimestamps?: boolean;
  adjustDuration?: boolean;
}

interface ImportTrackProbe {
  type?: string;
  language?: string;
  assetId?: string;
}

interface SaveAssetProbe {
  assetId?: string;
  suggestedName?: string;
}

async function chooseTargets(page: Page, names: string[]): Promise<void> {
  await page.getByTestId("language-picker-trigger").click();
  for (const name of names) {
    await page.getByRole("option", { name, exact: true }).click();
  }
  await page.getByTestId("language-picker-trigger").click();
}

async function openClone(page: Page): Promise<void> {
  await page.goto("/#/voice-clone");
  await expect(page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" })).toBeVisible();
  await page.locator('input[accept^=".mp3"]').setInputFiles(SAMPLE_AUDIO);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await chooseTargets(page, ["简体中文"]);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles(SAMPLE_TRANSLATED_SRT);
}

async function createRun(page: Page): Promise<void> {
  await page.getByPlaceholder("Project name").fill("E2E clone project");
  await page.getByRole("button", { name: "Create" }).click();
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

test("requires a project name before creating a clone run", async ({ page }) => {
  await page.goto("/#/voice-clone");
  await expect(page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" })).toBeVisible();
  await page.locator('input[accept^=".mp3"]').setInputFiles(SAMPLE_AUDIO);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await chooseTargets(page, ["简体中文"]);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles(SAMPLE_TRANSLATED_SRT);

  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Enter a project name.")).toBeVisible();
  expect(await probe<RunInputProbe>(page, "lastRunInput")).toBeNull();
});

test("rejects duplicate translated-file languages before creating a run", async ({ page }) => {
  await page.goto("/#/voice-clone");
  await expect(page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" })).toBeVisible();
  await page.locator('input[accept^=".mp3"]').setInputFiles(SAMPLE_AUDIO);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await chooseTargets(page, ["简体中文", "日本語"]);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles([
    SAMPLE_TRANSLATED_SRT,
    { ...SAMPLE_TRANSLATED_SRT, name: "e2e-sample.zh-CN.copy.srt" },
  ]);

  await createRun(page);

  await expect(
    page.getByText(
      "Each translated file needs a unique target language; add more target languages or remove extra files.",
    ),
  ).toBeVisible();
  expect(await probe<RunInputProbe>(page, "lastRunInput")).toBeNull();
});

test("clones translated lines and saves a candidate", async ({ page }) => {
  await openClone(page);
  await createRun(page);

  await expect(page.getByText("full-zh-CN.wav")).toBeVisible();

  const imports = await probe<ImportTrackProbe[]>(page, "lastImportTrackInputs");
  expect(imports?.map((item) => `${item.type}:${item.language}`)).toEqual([
    "transcription:en",
    "translation:zh-CN",
  ]);
  expect(imports?.every((item) => item.assetId)).toBe(true);

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.template).toBe("tool_voice_clone");
  expect(input?.sourceLanguage).toBe("en");
  expect(input?.targetLanguages).toEqual(["zh-CN"]);
  expect(input?.validateTimestamps).toBe(false);
  expect(input?.adjustDuration).toBe(false);

  await page.getByRole("button", { name: "Save audio" }).first().click();
  await expect(page.getByText(/Saved to ~\/Downloads\/clone-.*\.wav/)).toBeVisible();
  const saved = await probe<SaveAssetProbe>(page, "lastSaveAssetInput");
  expect(saved?.assetId).toBeTruthy();
  expect(saved?.suggestedName).toMatch(/^clone-zh-CN\.wav$/);
});

test("shows the execution log for a completed clone", async ({ page }) => {
  await openClone(page);
  await createRun(page);

  await expect(page.getByText("full-zh-CN.wav")).toBeVisible();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect(page.getByText("启动受管 FlashCLI 克隆")).toBeVisible();
  await expect(page.getByText("克隆音频合成完成")).toBeVisible();
});

test("does not show candidates when the clone run fails", async ({ page }) => {
  await openClone(page);
  await setMockValue(page, "toolRunFailure", true);
  await createRun(page);

  await expect(page.getByText("The task did not succeed.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save audio" })).toHaveCount(0);
});

test("shows a readable no-candidate state", async ({ page }) => {
  await openClone(page);
  await setMockValue(page, "voiceCloneNoCandidates", true);
  await createRun(page);

  await expect(page.getByText("Clone finished, but the full dubbing track was not produced.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save audio" })).toHaveCount(0);
});

test("keeps duration adjustment unavailable and shows frozen run options", async ({ page }) => {
  await openClone(page);

  const adjust = page.getByRole("checkbox", { name: "Dynamic duration adjustment" });
  const validate = page.getByRole("checkbox", { name: "Validate subtitle timestamps" });
  await expect(adjust).not.toBeChecked();
  await expect(adjust).toBeDisabled();
  await expect(validate).not.toBeChecked();

  // 时间戳校验仍可切换，并传入实际选择。
  await validate.check({ force: true });
  await createRun(page);
  await expect(page.getByText("full-zh-CN.wav")).toBeVisible();

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.validateTimestamps).toBe(true);
  expect(input?.adjustDuration).toBe(false);

  // 只读摘要来自 Run.params，不被当前表单值替代。
  const frozen = page.getByTestId("run-options");
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("On");
  await expect(frozen.locator('[data-option="adjustDuration"]')).toContainText("Off");

  await validate.uncheck({ force: true });
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("On");
});

test("rejects out-of-range timestamps only while validation is enabled", async ({ page }) => {
  await openClone(page);
  await setMockValue(page, "cloneTimelineIssue", "out_of_range");
  await page
    .getByRole("checkbox", { name: "Validate subtitle timestamps" })
    .check({ force: true });

  await createRun(page);
  await expect(
    page.getByText(
      "Subtitle timing exceeds the selected media duration. Fix the subtitle timing before creating the run.",
    ),
  ).toBeVisible();
  expect(await probe<RunInputProbe>(page, "lastRunInput")).toBeNull();

  await page
    .getByRole("checkbox", { name: "Validate subtitle timestamps" })
    .uncheck({ force: true });
  await createRun(page);
  await expect(page.getByText("full-zh-CN.wav")).toBeVisible();

  const input = await probe<RunInputProbe>(page, "lastRunInput");
  expect(input?.validateTimestamps).toBe(false);
  expect(input?.adjustDuration).toBe(false);
});

test("requires one translated subtitle per target language", async ({ page }) => {
  await page.goto("/#/voice-clone");
  await expect(page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" })).toBeVisible();
  await page.locator('input[accept^=".mp3"]').setInputFiles(SAMPLE_AUDIO);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await chooseTargets(page, ["简体中文", "日本語"]);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles(SAMPLE_TRANSLATED_SRT);

  await createRun(page);

  await expect(
    page.getByText(
      "Target languages and translated subtitles must match one-to-one (2 languages, 1 files).",
    ),
  ).toBeVisible();
  expect(await probe<RunInputProbe>(page, "lastRunInput")).toBeNull();
});

test("does not offer the source language as a dubbing target", async ({ page }) => {
  await page.goto("/#/voice-clone");
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);

  await page.getByTestId("language-picker-trigger").click();
  await expect(page.getByRole("option", { name: "English", exact: true })).toHaveCount(0);
  await expect(page.getByRole("option", { name: "简体中文", exact: true })).toBeVisible();
  await page.getByTestId("language-picker-trigger").click();
});

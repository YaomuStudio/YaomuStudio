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
}

interface SaveAssetProbe {
  assetId?: string;
  suggestedName?: string;
}

async function openSeparate(page: Page): Promise<void> {
  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E separate project");
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

async function saveInput(page: Page): Promise<SaveAssetProbe | null> {
  return page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { lastSaveAssetInput?: SaveAssetProbe } })
      .__yaomuMock;
    return control?.lastSaveAssetInput ?? null;
  });
}

async function setMockFlag(page: Page, key: string): Promise<void> {
  await page.evaluate((name) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control[name] = true;
  }, key);
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("separates a source into two tracks and saves one", async ({ page }) => {
  await openSeparate(page);

  // 配置区不再展示输出轨道卡；新建提示统一使用 CreationNotice。
  await expect(page.locator(".track-card")).toHaveCount(0);
  const notice = page.locator(".creation-notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText(
    "Use the original mix containing vocals and background music or ambience. Already-separated stems do not need this tool.",
  );
  await expect(notice).toContainText(
    "Clear, prominent vocals separate better; overlapping speakers or strong noise can reduce quality.",
  );

  await createRun(page);

  await expect(page.getByText("voice.wav")).toBeVisible();
  await expect(page.getByText("ambience.wav")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(2);
  await expect(page.getByText(
    "The separated vocal track can be used directly as the source for high-emotion clone dubbing.",
  )).toBeVisible();

  const input = await runInput(page);
  expect(input?.template).toBe("tool_separate");
  expect(input?.sourceLanguage).toBe("und");
  expect(input?.targetLanguages).toEqual([]);
  expect(input?.inputRevisionIds?.length).toBe(1);

  await page.getByRole("button", { name: "Save" }).first().click();
  await expect(page.getByText(/Saved to ~\/Downloads\/voice\.wav/)).toBeVisible();
  const saved = await saveInput(page);
  expect(saved?.assetId).toBeTruthy();
  expect(saved?.suggestedName).toBe("voice.wav");
});

test("shows sanitized execution log for a completed separation", async ({ page }) => {
  await openSeparate(page);
  await createRun(page);

  await expect(page.getByText("voice.wav")).toBeVisible();
  await expect(page.getByText("Execution log")).toBeVisible();
  await expect(page.getByText("启动 FlashCLI 分离")).toBeVisible();
  await expect(page.getByText("FlashCLI 分离完成")).toBeVisible();
});

test("does not show tracks when the separation run fails", async ({ page }) => {
  await openSeparate(page);
  await setMockFlag(page, "toolRunFailure");
  await createRun(page);

  await expect(page.getByText("The task did not succeed.")).toBeVisible();
  await expect(page.getByText("voice.wav")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(0);
});

test("keeps tracks visible when saving fails", async ({ page }) => {
  await openSeparate(page);
  await createRun(page);
  await expect(page.getByText("voice.wav")).toBeVisible();

  await setMockFlag(page, "saveAssetFailure");
  await page.getByRole("button", { name: "Save" }).first().click();

  await expect(page.getByText("Saving the track failed.")).toBeVisible();
  await expect(page.getByText("voice.wav")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(2);
});

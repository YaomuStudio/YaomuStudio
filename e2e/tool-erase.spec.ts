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
  eraseMode?: string;
  extract?: { mode?: string; ocrLanguage?: string };
}

interface SaveAssetProbe {
  assetId?: string;
  suggestedName?: string;
}

async function openErase(page: Page): Promise<void> {
  await page.goto("/#/tools/erase");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle erase" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E erase project");
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

test("erases a video with the default OCR handling and saves the result", async ({ page }) => {
  await openErase(page);
  await createRun(page);

  await expect(page.getByText("clean.mp4")).toBeVisible();
  const input = await runInput(page);
  expect(input?.template).toBe("tool_erase");
  expect(input?.sourceLanguage).toBe("chinese");
  expect(input?.targetLanguages).toEqual([]);
  expect(input?.eraseMode).toBe("dialog_only");
  expect(input?.extract).toEqual({ mode: "ocr", ocrLanguage: "chinese", ocrQuality: "fine" });
  expect(input?.inputRevisionIds?.length).toBe(1);

  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(/Saved to ~\/Downloads\/clean\.mp4/)).toBeVisible();
  const saved = await saveInput(page);
  expect(saved?.assetId).toBeTruthy();
  expect(saved?.suggestedName).toBe("clean.mp4");
});

test("does not show a result when the erase run fails", async ({ page }) => {
  await openErase(page);
  await setMockFlag(page, "toolRunFailure");
  await createRun(page);

  await expect(page.getByText("The task did not succeed.")).toBeVisible();
  await expect(page.getByText("clean.mp4")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(0);
});

test("keeps the result visible when saving fails", async ({ page }) => {
  await openErase(page);
  await createRun(page);
  await expect(page.getByText("clean.mp4")).toBeVisible();

  await setMockFlag(page, "saveAssetFailure");
  await page.getByRole("button", { name: "Save" }).click();

  await expect(page.getByText("Saving the cleaned video failed.")).toBeVisible();
  await expect(page.getByText("clean.mp4")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toBeVisible();
});

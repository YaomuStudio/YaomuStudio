import { expect, test } from "@playwright/test";
import {
  SAMPLE_MEDIA,
  createProject,
  importSampleMedia,
  openPipelineForProject,
  seedMockControl,
  seedOnboardedSettings,
  seedRunParams,
} from "./support";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  // 让 mock 记录 createWorkflowRun 的入参，用于断言 input_revision_ids。
  await seedMockControl(page);
});

interface RunInputProbe {
  inputRevisionIds?: string[];
  targetLanguages?: string[];
  validateTimestamps?: boolean;
  adjustDuration?: boolean;
}

test("submits the selected source asset as input_revision_ids", async ({ page }) => {
  const projectId = await createProject(page, "E2E source project");
  await openPipelineForProject(page, projectId);
  await importSampleMedia(page);

  const first = page.locator("li.asset").filter({ hasText: SAMPLE_MEDIA.name });
  await expect(first.getByText("Source", { exact: true })).toBeVisible();
  const firstId = await first.getAttribute("data-asset-id");
  expect(firstId).toBeTruthy();

  // 再导入一个源文件：未手动选择时应继续默认第一个。
  const secondMedia = { ...SAMPLE_MEDIA, name: "e2e-sample-2.mp4" };
  await page.locator('input[type="file"]').setInputFiles(secondMedia);
  await expect(page.getByRole("status").filter({ hasText: "1 file imported" })).toBeVisible();
  const second = page.locator("li.asset").filter({ hasText: secondMedia.name });
  await expect(second).toBeVisible();
  await expect(first.getByText("Source", { exact: true })).toBeVisible();

  // 手动切换源素材后，Source 标记与提交 ID 都应切换。
  await second.click();
  await expect(second.getByText("Source", { exact: true })).toBeVisible();
  await expect(first.getByText("Source", { exact: true })).toHaveCount(0);
  const secondId = await second.getAttribute("data-asset-id");
  expect(secondId).toBeTruthy();
  expect(secondId).not.toBe(firstId);

  await page.getByRole("button", { name: "Start transcription" }).click();
  await expect(page.getByRole("heading", { name: "Dubbing configuration" })).toBeVisible();
  await page.getByRole("button", { name: "Start dubbing" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Workflow created" }),
  ).toBeVisible();

  const input = await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { lastRunInput?: RunInputProbe } }
    ).__yaomuMock;
    return control?.lastRunInput ?? null;
  });
  expect(input?.inputRevisionIds).toEqual([secondId]);
});

test("selects target languages from the grouped language picker", async ({ page }) => {
  const projectId = await createProject(page, "E2E language project");
  await openPipelineForProject(page, projectId);
  await importSampleMedia(page);
  await page.getByRole("button", { name: "Start transcription" }).click();
  await expect(page.getByRole("heading", { name: "Dubbing configuration" })).toBeVisible();

  await page.getByTestId("language-picker-trigger").click();
  await expect(page.getByText("More languages", { exact: true })).toBeVisible();
  await page.getByRole("option", { name: "日本語" }).click();
  await expect(page.locator(".language-chip").filter({ hasText: "日本語" })).toBeVisible();

  await page.getByRole("button", { name: "Start dubbing" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Workflow created" }),
  ).toBeVisible();

  const input = await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { lastRunInput?: RunInputProbe } }
    ).__yaomuMock;
    return control?.lastRunInput ?? null;
  });
  expect(input?.targetLanguages).toEqual(["en", "ja"]);
});

test("keeps duration adjustment disabled and submits the timestamp choice", async ({ page }) => {
  const projectId = await createProject(page, "E2E options project");
  await openPipelineForProject(page, projectId);
  await importSampleMedia(page);
  await page.getByRole("button", { name: "Start transcription" }).click();
  await expect(page.getByRole("heading", { name: "Dubbing configuration" })).toBeVisible();

  const adjust = page.getByRole("checkbox", { name: "Dynamically adjust video duration" });
  const validate = page.getByRole("checkbox", { name: "Validate subtitle timestamps" });
  await expect(adjust).not.toBeChecked();
  await expect(adjust).toBeDisabled();
  await expect(validate).not.toBeChecked();

  await validate.check({ force: true });
  await page.getByRole("button", { name: "Start dubbing" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Workflow created" }),
  ).toBeVisible();

  const input = await page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { lastRunInput?: RunInputProbe } }
    ).__yaomuMock;
    return control?.lastRunInput ?? null;
  });
  expect(input?.validateTimestamps).toBe(true);
  expect(input?.adjustDuration).toBe(false);

  const frozen = page.getByTestId("run-options");
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("On");
  await expect(frozen.locator('[data-option="adjustDuration"]')).toContainText("Off");
});

test("shows frozen historical run options without replacing form defaults", async ({ page }) => {
  await seedRunParams(page, {
    "proj-demo": { validateTimestamps: false, adjustDuration: true },
  });
  await page.goto("/#/pipeline?project=proj-demo");
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();

  const frozen = page.getByTestId("run-options");
  await expect(frozen.locator('[data-option="validateTimestamps"]')).toContainText("Off");
  await expect(frozen.locator('[data-option="adjustDuration"]')).toContainText(
    "Recorded on · not applied",
  );

  // 打开配置页时仍使用新建表单默认值，不把历史冻结值回填为表单值。
  await page.getByRole("button", { name: "Edit configuration" }).click();
  await expect(
    page.getByRole("checkbox", { name: "Validate subtitle timestamps" }),
  ).not.toBeChecked();
  const adjust = page.getByRole("checkbox", { name: "Dynamically adjust video duration" });
  await expect(adjust).not.toBeChecked();
  await expect(adjust).toBeDisabled();
});


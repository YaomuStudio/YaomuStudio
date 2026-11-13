import { expect, test } from "@playwright/test";
import {
  SAMPLE_MEDIA,
  createProject,
  importSampleMedia,
  openPipelineForProject,
  seedOnboardedSettings,
} from "./support";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

// 用例只走浏览器 mock：不启动 Core/Electron，不涉及真实媒体或模型执行。
test("imports a media file through the pipeline and advances to config", async ({ page }) => {
  const projectId = await createProject(page, "E2E import project");
  await openPipelineForProject(page, projectId);

  // 新项目导入前的空状态。
  await expect(page.getByText("No media imported")).toBeVisible();

  await importSampleMedia(page);

  const asset = page.locator("li.asset").filter({ hasText: SAMPLE_MEDIA.name });
  await expect(asset.getByText("Ready", { exact: true })).toBeVisible();
  await expect(page.getByText("No media imported")).toHaveCount(0);

  // 资产存在后，步骤可从导入推进到配置。
  await page.getByRole("button", { name: "Start transcription" }).click();
  await expect(page.getByRole("heading", { name: "Dubbing configuration" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Start dubbing" })).toBeVisible();
});

test("offers relocation for a missing source file", async ({ page }) => {
  // mock 导入时把该文件标为 missing，模拟外部源文件已被移动/删除。
  await page.addInitScript((name) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), missingImportNames: [name] };
  }, SAMPLE_MEDIA.name);

  const projectId = await createProject(page, "E2E relocate project");
  await openPipelineForProject(page, projectId);
  await importSampleMedia(page);

  const asset = page.locator("li.asset").filter({ hasText: SAMPLE_MEDIA.name });
  await expect(asset.getByText("Missing", { exact: true })).toBeVisible();

  // 浏览器 mock 没有桌面文件桥：按钮存在，点击后给出可读提示而不是静默失败。
  await asset.getByRole("button", { name: "Relocate" }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Relocating a source file requires the desktop app" }),
  ).toBeVisible();
});

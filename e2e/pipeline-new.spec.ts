import { expect, test } from "@playwright/test";
import { SAMPLE_MEDIA, seedMockControl, seedOnboardedSettings } from "./support";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("redirects legacy correction and pipeline links to the new routes", async ({ page }) => {
  await page.goto("/#/projects/proj-demo/correction");
  await expect(page).toHaveURL(/#\/manual-correction\/proj-demo$/);
  await expect(page.getByTestId("segment-row").first()).toBeVisible();

  await page.goto("/#/pipeline?project=proj-demo");
  await expect(page).toHaveURL(/#\/projects\/proj-demo\/pipeline(\?.*)?$/);
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();

  await page.goto("/#/pipeline");
  await expect(page).toHaveURL(/#\/pipeline\/new$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "New full pipeline" }),
  ).toBeVisible();
});

test("video source dropzone keeps its upload styling", async ({ page }) => {
  await page.goto("/#/pipeline/new");
  const dropzone = page.locator(".ym-dropzone");
  await expect(dropzone).toBeVisible();
  const style = await dropzone.evaluate((element) => {
    const computed = getComputedStyle(element);
    return {
      borderWidth: parseFloat(computed.borderTopWidth),
      background: computed.backgroundColor,
      height: element.getBoundingClientRect().height,
    };
  });
  expect(style.borderWidth).toBeGreaterThan(0);
  expect(style.background).not.toBe("rgba(0, 0, 0, 0)");
  expect(style.height).toBeGreaterThanOrEqual(100);
});

test("creates a project from the new page and enters the project pipeline", async ({ page }) => {
  await page.goto("/#/pipeline/new");
  await expect(
    page.getByRole("heading", { level: 1, name: "New full pipeline" }),
  ).toBeVisible();

  await page.getByRole("textbox", { name: "Project name" }).fill("E2E new pipeline");
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "English", exact: true }).click();
  await page.locator('.ym-dropzone__input').setInputFiles(SAMPLE_MEDIA);
  await expect(page.getByText(SAMPLE_MEDIA.name)).toBeVisible();

  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/#\/projects\/[^/]+\/pipeline\?/);
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();

  // 新建页把语言/擦除/校验/时长/双语作为 query 传给项目流水线。
  const url = new URL(page.url());
  expect(url.hash).toContain("autostart=1");
  expect(url.hash).toContain("langs=en");
  expect(url.hash).toContain("erase=dialog_only");
  expect(url.hash).toContain("validate=0");

  // autostart 真实触发预检/创建，参数来自新建页。
  await expect
    .poll(async () =>
      page.evaluate(() => {
        const scope = window as Window & {
          __yaomuMock?: { lastRunInput?: { targetLanguages?: string[]; validateTimestamps?: boolean } };
        };
        return scope.__yaomuMock?.lastRunInput ?? null;
      }),
    )
    .not.toBeNull();
  const runInput = await page.evaluate(() => {
    const scope = window as Window & {
      __yaomuMock?: { lastRunInput?: { targetLanguages?: string[]; validateTimestamps?: boolean } };
    };
    return scope.__yaomuMock?.lastRunInput ?? null;
  });
  expect(runInput?.targetLanguages).toEqual(["en"]);
  expect(runInput?.validateTimestamps).toBe(false);
});

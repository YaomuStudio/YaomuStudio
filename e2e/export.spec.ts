import { expect, test } from "@playwright/test";
import { seedMockControl, seedOnboardedSettings } from "./support";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

// 使用 mock 预置的 proj-demo：06 翻译处于等待复核，Pipeline 直接进入第 3 步。
test("confirms delivery review and queues an export record", async ({ page }) => {
  await page.goto("/#/pipeline?project=proj-demo");
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();

  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(page.getByRole("heading", { name: "Delivery review" })).toBeVisible();

  // A/B 收敛为“原声 / 最终混音”；没有后端依据的 dubbed/mixed 选项不出现。
  await expect(page.getByRole("tab", { name: "Original audio", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Final mix", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Dubbed audio", exact: true })).toHaveCount(0);
  await expect(page.getByText(/No final mix preview yet/)).toBeVisible();

  // 无交付放行时导出被阻断；勾选确认会绑定 Core 门槛摘要并放行。
  const exportButton = page.getByRole("button", { name: "Start export" });
  await expect(exportButton).toBeDisabled();
  await expect(page.getByText("Confirm delivery review before exporting")).toBeVisible();

  await page
    .getByText("I confirm the translation, voice selection and subtitle style", { exact: true })
    .click();
  // 另一条 current 交付门槛仍需逐条放行；全部放行后才恢复总体确认。
  await page
    .locator('[data-testid="review-gate"][data-source-kind="delivery_revision"]')
    .getByRole("button", { name: "Approve" })
    .click();
  await expect(exportButton).toBeEnabled();
  await exportButton.click();

  await expect(page.getByRole("status").filter({ hasText: "Export queued" })).toBeVisible();
  const record = page.locator(".export__record");
  await expect(record.getByText("Latest export")).toBeVisible();
  await expect(record.getByText("Queued", { exact: true })).toBeVisible();
  await expect(record.locator(".export__record-id")).toHaveText(/^exp-/);
});

test("exports bilingual subtitles and sends the flag to Core", async ({ page }) => {
  await seedMockControl(page);
  await page.goto("/#/pipeline?project=proj-demo");
  await page.getByRole("tab", { name: "Delivery & export" }).click();

  // 视频导出不显示双语选项。
  await expect(page.getByText("Bilingual subtitles", { exact: true })).toHaveCount(0);

  await page.getByRole("tab", { name: "Subtitles" }).click();
  const bilingual = page.getByText("Include original + translation lines", { exact: true });
  await expect(bilingual).toBeVisible();
  await bilingual.click();

  await page
    .getByText("I confirm the translation, voice selection and subtitle style", { exact: true })
    .click();
  // 字幕导出优先放行当前 revision；final mix 的当前 pending 仍需单独放行。
  await page
    .locator('[data-testid="review-gate"][data-source-kind="delivery_mix"]')
    .getByRole("button", { name: "Approve" })
    .click();
  const exportButton = page.getByRole("button", { name: "Start export" });
  await expect(exportButton).toBeEnabled();
  await exportButton.click();
  await expect(page.getByRole("status").filter({ hasText: "Export queued" })).toBeVisible();

  const sent = await page.evaluate(() => {
    const scope = window as Window & {
      __yaomuMock?: { lastExportInput?: { kind?: string; format?: string; bilingual?: boolean } };
    };
    return scope.__yaomuMock?.lastExportInput;
  });
  expect(sent?.kind).toBe("subtitle");
  expect(sent?.format).toBe("srt");
  expect(sent?.bilingual).toBe(true);
});


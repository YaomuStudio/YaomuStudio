import { expect, test } from "@playwright/test";
import { clearMockFault, seedMockFault, seedOnboardedSettings } from "./support";

const CORE_NOT_READY = "The local service is not ready. Check the local logs and retry.";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

// 故障注入只经 ui/src/api/mock.ts 的 DEV 开关生效（window.__yaomuMock.fail）。
test("shows a readable error with retry instead of a blank page", async ({ page }) => {
  await seedMockFault(page, "getProject");
  await page.goto("/#/projects/proj-demo/pipeline");

  // 应用壳仍在：错误可读、有重试入口，不是白屏。
  await expect(page.getByRole("navigation", { name: "primary" })).toBeVisible();
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByText(CORE_NOT_READY).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry" }).first()).toBeVisible();

  // 清除故障后重试可恢复项目列表并绑定项目。
  await clearMockFault(page);
  await page.getByRole("button", { name: "Retry" }).first().click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo\/pipeline$/);
  await expect(page.getByText(CORE_NOT_READY)).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Current run" })).toBeVisible();
});

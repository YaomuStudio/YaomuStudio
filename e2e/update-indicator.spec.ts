import { expect, test } from "@playwright/test";
import {
  emitUpdateAvailable,
  emitUpdateDownloaded,
  emitUpdateError,
  emitUpdateProgress,
  emitUpdateStatus,
  readUpdateCalls,
  seedOnboardedSettings,
  seedUpdateBridge,
} from "./support";

// 浏览器 E2E 只验证顶栏入口对有限 updater 状态的消费；
// 不触发真实下载/安装，也不证明打包态升级或排空。

test("the update indicator stays hidden when no update is available", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");

  await expect(page.getByTestId("update-indicator")).toHaveCount(0);
});

test("clicking an available update starts the download without leaving the page", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toBeVisible();
  await expect(indicator).toHaveAttribute("aria-label", /2\.0\.0/);
  const before = page.url();
  await indicator.click();

  expect((await readUpdateCalls(page)).download).toBe(1);
  expect(page.url()).toBe(before);
});

test("a downloading indicator does not trigger a second download", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await page.getByTestId("update-indicator").click();
  await emitUpdateProgress(page, { percent: 42 });

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toHaveAttribute("aria-label", /42%/);
  await indicator.click();
  expect((await readUpdateCalls(page)).download).toBe(1);
});

test("a downloaded indicator enters the guarded install path", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateDownloaded(page, "2.0.0");

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toBeVisible();
  await expect(indicator).toHaveAttribute("aria-label", /ready/i);
  await indicator.click();

  expect((await readUpdateCalls(page)).install).toBe(1);
});

test("a repeated availability event does not downgrade a downloaded update", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateDownloaded(page, "2.0.0");
  await emitUpdateAvailable(page, "2.0.0");

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toHaveAttribute("aria-label", /ready/i);
  await expect(page.getByTestId("update-toast")).toBeVisible();
});

test("a background checking/idle cycle does not downgrade a downloaded update", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateDownloaded(page, "2.0.0");

  // 真实 4h/聚焦检查会先发 checking，再发 available 或 idle；不得丢掉待安装状态。
  await emitUpdateStatus(page, "checking");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateStatus(page, "idle");

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toHaveAttribute("aria-label", /ready/i);
  await expect(page.getByTestId("update-toast")).toBeVisible();
});

test("a check error keeps a downloaded update ready", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateDownloaded(page, "2.0.0");
  await emitUpdateError(page);

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toHaveAttribute("aria-label", /ready/i);
  await expect(page.getByTestId("update-toast")).toBeVisible();
});

test("a background check failure keeps an available update visible", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/projects");
  await emitUpdateAvailable(page, "2.0.0");
  await emitUpdateStatus(page, "checking");
  await emitUpdateStatus(page, "idle");

  const indicator = page.getByTestId("update-indicator");
  await expect(indicator).toBeVisible();
  await expect(indicator).toHaveAttribute("aria-label", /2\.0\.0/);
  await expect(page.getByTestId("update-toast")).toHaveCount(0);
});

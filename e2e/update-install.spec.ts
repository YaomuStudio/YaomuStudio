import { expect, test } from "@playwright/test";
import {
  emitUpdateDownloaded,
  emitUpdateInstallState,
  readUpdateCalls,
  releaseUpdateState,
  seedOnboardedSettings,
  seedUpdateBridge,
  updateInstallSnapshot,
} from "./support";

// 浏览器 E2E：只验证 UI 对有限宿主安装状态的消费与安全文案，
// 不触发真实 updater，也不能证明签名、Windows 升级或回滚。

test("waiting state offers cancellation and cancel only clears host consent", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, {
    installResult: updateInstallSnapshot({
      revision: 2,
      phase: "waiting",
      reason: "active_work",
      version: "2.0.0",
      canCancel: true,
    }),
    cancelResult: updateInstallSnapshot({ revision: 3, phase: "idle" }),
  });

  await page.goto("/#/settings/about");
  await emitUpdateDownloaded(page, "2.0.0");
  await expect(page.getByTestId("update-install")).toBeEnabled();
  await page.getByTestId("update-install").click();

  const status = page.getByTestId("update-install-status");
  await expect(status).toContainText("Waiting for active work to finish before installing version 2.0.0");
  await expect(page.getByTestId("update-install-cancel")).toBeVisible();
  await expect(page.getByTestId("update-install")).toBeDisabled();
  expect((await readUpdateCalls(page)).install).toBe(1);

  await page.getByTestId("update-install-cancel").click();
  await expect(status).toHaveCount(0);
  await expect(page.getByTestId("update-install-cancel")).toHaveCount(0);
  expect(await readUpdateCalls(page)).toMatchObject({ install: 1, cancel: 1 });
});

test("preparing/stopping are noncancelable and disable repeated check/download", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, {
    installResult: updateInstallSnapshot({
      revision: 2,
      phase: "waiting",
      version: "2.0.0",
      canCancel: true,
    }),
  });
  await page.goto("/#/settings/about");
  await emitUpdateDownloaded(page, "2.0.0");
  await page.getByTestId("update-install").click();
  await expect(page.getByTestId("update-install-cancel")).toBeVisible();

  await emitUpdateInstallState(
    page,
    updateInstallSnapshot({
      revision: 3,
      phase: "preparing",
      version: "2.0.0",
    }),
  );
  await expect(page.getByTestId("update-install-status")).toContainText(
    "This step cannot be canceled",
  );
  await expect(page.getByTestId("update-install-cancel")).toHaveCount(0);
  await expect(page.getByTestId("update-install")).toBeDisabled();
  await expect(page.getByTestId("update-check")).toBeDisabled();

  await emitUpdateInstallState(
    page,
    updateInstallSnapshot({ revision: 4, phase: "stopping", version: "2.0.0" }),
  );
  await expect(page.getByTestId("update-install-status")).toContainText(
    "Stopping the local engine",
  );
  await expect(page.getByTestId("update-install-cancel")).toHaveCount(0);
  await expect(page.getByTestId("update-install")).toBeDisabled();
});

test("blocked state shows finite safe reason and restart guidance, never raw errors", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, {
    initial: updateInstallSnapshot({
      revision: 4,
      phase: "blocked",
      reason: "restart_required",
      version: "2.0.0",
    }),
  });
  await page.goto("/#/settings/about");
  await emitUpdateDownloaded(page, "2.0.0");

  await expect(page.getByTestId("update-install-status")).toContainText(
    "The app must be restarted before it can install an update.",
  );
  await expect(page.getByTestId("update-install-restart")).toContainText(
    "The update will not take effect until you restart",
  );
  // 不可逆清理后的 blocked 原因不允许再次点击安装；页面不出现原始异常文本。
  await expect(page.getByTestId("update-install")).toBeDisabled();

  // runtime 身份变化丢弃旧同意但不封死入口：用户可在新连接上重新显式同意。
  await emitUpdateInstallState(
    page,
    updateInstallSnapshot({
      revision: 5,
      phase: "blocked",
      reason: "runtime_changed",
      version: "2.0.0",
    }),
  );
  await expect(page.getByTestId("update-install-status")).toContainText(
    "The local engine identity changed",
  );
  await expect(page.getByTestId("update-install")).toBeEnabled();
  await expect(page.getByTestId("update-install-restart")).toHaveCount(0);
});

test("older revisions never overwrite newer host installation state", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, {
    initial: updateInstallSnapshot({
      revision: 5,
      phase: "waiting",
      reason: "request_in_flight",
      version: "2.0.1",
      canCancel: true,
    }),
  });
  await page.goto("/#/settings/about");
  await expect(page.getByTestId("update-install-status")).toContainText(
    "Waiting for in-flight requests",
  );
  await expect(page.getByTestId("update-install-cancel")).toBeVisible();

  await emitUpdateInstallState(
    page,
    updateInstallSnapshot({ revision: 2, phase: "preparing", version: "9.9.9" }),
  );
  await expect(page.getByTestId("update-install-status")).toContainText(
    "Waiting for in-flight requests",
  );
  await expect(page.getByTestId("update-install-cancel")).toBeVisible();
});

test("reload hydrates the current host state after the pushed snapshot listener is attached", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, {
    holdState: true,
    initial: updateInstallSnapshot({
      revision: 7,
      phase: "waiting",
      reason: "active_work",
      version: "3.0.0",
      canCancel: true,
    }),
  });
  await page.goto("/#/settings/about");

  await expect(page.getByTestId("update-install-status")).toHaveCount(0);
  await expect.poll(async () => (await readUpdateCalls(page)).getState).toBe(1);
  await releaseUpdateState(page);
  await expect(page.getByTestId("update-install-status")).toContainText("version 3.0.0");
  await expect(page.getByTestId("update-install-cancel")).toBeVisible();
});

test("a legacy update bridge cannot fall back to the unsafe install entry", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, { legacy: true });
  await page.goto("/#/settings/about");
  await emitUpdateDownloaded(page, "2.0.0");

  await expect(page.getByTestId("update-install-bridge-outdated")).toContainText(
    "older update component",
  );
  await expect(page.getByTestId("update-install")).toBeDisabled();
  await page.getByTestId("update-install").click({ force: true });
  expect((await readUpdateCalls(page)).install).toBe(0);

  // 既有检查/下载偏好保留：旧桥仍可检查，但不进入未守卫安装。
  await page.getByTestId("update-check").click();
  expect((await readUpdateCalls(page)).check).toBe(1);
});

test("malformed host snapshot is rejected and shows a fixed safe message", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page, { malformedState: true });
  await page.goto("/#/settings/about");

  await expect(page.getByTestId("update-install-error")).toContainText(
    "The update installation request failed. No installer was started.",
  );
  await expect(page.getByTestId("update-install-status")).toHaveCount(0);
});

test("dismissing the download toast keeps the downloaded version available", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedUpdateBridge(page);
  await page.goto("/#/settings/about");
  await emitUpdateDownloaded(page, "2.0.0");

  const toast = page.getByTestId("update-toast");
  await expect(toast).toBeVisible();
  await page.getByTestId("update-toast-later").click();
  await expect(toast).toHaveCount(0);

  // 稍后只关闭提示，不丢下载版本；关于页安装入口与顶栏入口仍在。
  await expect(page.getByTestId("update-install")).toBeVisible();
  await expect(page.getByTestId("update-indicator")).toBeVisible();
});

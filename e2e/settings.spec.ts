import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import {
  clearBusinessCalls,
  readBusinessCalls,
  seedMockControl,
  seedOnboardedSettings,
  seedUpdateBridge,
} from "./support";

/**
 * 一次性预置已引导设置：持久化用例会 reload，若每次导航都重写
 * localStorage 会把旧值盖回去，得到假失败。sessionStorage 在每个页面上下文
 * 内跨导航保留，因此只在首次进入时写入；其余不 reload 的用例直接复用
 * 共享 `seedOnboardedSettings`。
 */
async function seedSettingsOnce(page: Page): Promise<void> {
  await page.addInitScript(() => {
    if (sessionStorage.getItem("__yaomuSettingsSeeded")) return;
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({ onboarded: true, locale: "en" }),
    );
    sessionStorage.setItem("__yaomuSettingsSeeded", "1");
  });
}

function readSettings(page: Page) {
  return page.evaluate(() => {
    const raw = localStorage.getItem("yaomu.settings");
    return raw ? JSON.parse(raw) : {};
  });
}

test("appearance scale applies on release and persists", async ({ page }) => {
  await seedSettingsOnce(page);
  await page.goto("/#/settings/appearance");
  const range = page.locator(".scale-range");
  await expect(range).toBeVisible();

  // 拖动中只更新草稿；松开（change）后才应用，避免页面缩放时滑块自身移动造成漂移。
  await range.evaluate((element) => {
    const input = element as HTMLInputElement;
    input.value = "125";
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue("zoom")))
    .toBe("1");
  expect((await readSettings(page)).uiScale).toBeUndefined();

  await range.evaluate((element) => {
    (element as HTMLInputElement).dispatchEvent(new Event("change", { bubbles: true }));
  });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue("zoom")))
    .toBe("1.25");
  expect((await readSettings(page)).uiScale).toBe(125);

  await page.reload();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue("zoom")))
    .toBe("1.25");

  await page.getByRole("button", { name: "Reset to 100%" }).click();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue("zoom")))
    .toBe("1");
  expect((await readSettings(page)).uiScale).toBe(100);
});

test("scale tick labels follow the 80–150 value positions", async ({ page }) => {
  await seedSettingsOnce(page);
  await page.goto("/#/settings/appearance");
  const labels = page.locator(".scale-ticks span");
  await expect(labels).toHaveCount(4);

  const percents = await page.evaluate(() => {
    const ticks = [...document.querySelectorAll<HTMLElement>(".scale-ticks span")];
    const container = ticks[0]?.parentElement;
    if (!container) return [];
    const box = container.getBoundingClientRect();
    return ticks.map((tick) => {
      const rect = tick.getBoundingClientRect();
      return Math.round(((rect.left + rect.width / 2 - box.left) / box.width) * 1000) / 10;
    });
  });
  // 100% 与 125% 不是等距刻度：分别位于 28.6% 和 64.3%，不能按四分位摆放。
  expect(percents[0]).toBeCloseTo(0, 0);
  expect(percents[1]).toBeCloseTo(28.6, 0);
  expect(percents[2]).toBeCloseTo(64.3, 0);
  expect(percents[3]).toBeCloseTo(100, 0);
});

test("update preferences live on About and persist without a reload flash", async ({ page }) => {
  await seedSettingsOnce(page);
  await seedUpdateBridge(page);
  await page.goto("/#/settings/about");
  const autoCheck = page.getByRole("checkbox", { name: "Automatic update checks" });
  const autoDownload = page.getByRole("checkbox", { name: "Download updates automatically" });
  await expect(autoCheck).toBeChecked();
  await expect(autoDownload).not.toBeChecked();

  await autoCheck.uncheck({ force: true });
  await autoDownload.check({ force: true });

  await expect.poll(async () => (await readSettings(page)).autoCheckUpdates).toBe(false);
  await expect.poll(async () => (await readSettings(page)).autoDownloadUpdates).toBe(true);
  await page.reload();
  await expect(
    page.getByRole("checkbox", { name: "Automatic update checks" }),
  ).not.toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "Download updates automatically" }),
  ).toBeChecked();
});

test("general page no longer duplicates the update switches", async ({ page }) => {
  await seedSettingsOnce(page);
  await page.goto("/#/settings/general");
  await expect(
    page.getByRole("checkbox", { name: "Automatic update checks" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("checkbox", { name: "Download updates automatically" }),
  ).toHaveCount(0);
});

test("storage usage and clean dialog reflect the current category", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/storage");
  await expect(page.getByRole("heading", { name: "Storage" })).toBeVisible();
  const clean = page.getByRole("button", { name: "Clean" }).first();
  await expect(clean).toBeVisible();
  await clean.click();

  const dialog = page.getByRole("dialog").filter({ hasText: "Clean" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Model packages");
  await dialog.getByRole("button", { name: "Cancel" }).last().click();
  await expect(dialog).toBeHidden();
});

test("device selection asks for confirmation before switching all slots", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/devices");
  await expect(page.getByRole("heading", { name: "Compute devices" })).toBeVisible();
  // 设备行与汇总卡消费探测数值；mock 覆盖当前宿主平台。
  await expect(page.locator(".device__meta").first()).toContainText(/ \/ /);
  await expect(page.locator(".summary__item").nth(2)).toContainText(/ \/ /);
  // 六个模型槽位逐项展示执行占用状态。
  await expect(page.locator(".slot-row")).toHaveCount(6);
  await expect(page.locator(".slot-row").first()).toContainText("Running");
  await expect(page.locator(".slot-row").first()).toContainText("1 active");
  await expect(page.locator(".slot-row").nth(1)).toContainText("Idle");
  await expect(page.locator(".summary__item").nth(1)).toContainText("1 of 6 models running");
  const radios = page.getByRole("radio");
  await expect(radios.first()).toBeChecked();
  if ((await radios.count()) > 1) {
    await radios.nth(1).check({ force: true });
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
  }
});

test("status bar shows the selected compute device after switching", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/devices");
  await expect(page.getByRole("heading", { name: "Compute devices" })).toBeVisible();

  // Linux mock 只有一块设备，没有可切换目标。
  test.skip((await page.getByRole("radio").count()) < 2, "single compute device");

  const statusDevice = page
    .locator("footer.statusbar")
    .locator(".statusbar__item")
    .filter({ hasText: "Device" });
  // 初始为推荐设备（macOS MPS / Windows CUDA mock），不是 CPU。
  await expect(statusDevice).not.toContainText("CPU");

  await page.locator("label.ym-radio__item").filter({ hasText: /CPU\)?$/ }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Switch device" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Compute device updated for models." }),
  ).toBeVisible();

  // 状态栏必须跟随用户选择，而不是继续显示 recommended 设备。
  await expect(statusDevice).toContainText("CPU");
});

test("device page refreshes runtime state on entry", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/settings/general");
  await expect(page.getByRole("heading", { name: "General" })).toBeVisible();
  await expect
    .poll(async () => (await readBusinessCalls(page)).includes("getCapabilities"))
    .toBe(true);

  // 启动阶段已加载能力目录；进入设备页必须再次读取，才能拿到最新槽位运行状态。
  await clearBusinessCalls(page);
  await page.goto("/#/settings/devices");
  await expect(page.getByRole("heading", { name: "Compute devices" })).toBeVisible();
  await expect
    .poll(async () => (await readBusinessCalls(page)).includes("getCapabilities"))
    .toBe(true);
});

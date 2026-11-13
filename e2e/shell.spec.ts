import { expect, test, type Page } from "@playwright/test";
import { seedCapabilityStates, seedOnboardedSettings } from "./support";

const NAV_LABELS = [
  "Workbench",
  "Projects",
  "Full Pipeline",
  "Voice Clone",
  "Subtitle Erase",
  "Audio Separation",
  "Subtitle Translation",
  "Subtitle Extraction",
  "Custom Pipeline",
  "Settings",
];

async function sidebarWidth(page: Page): Promise<number> {
  return page.locator("#primary-nav").evaluate((el) => Math.round(el.getBoundingClientRect().width));
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

test("shows the model readiness summary and opens model settings", async ({ page }) => {
  await page.goto("/#/");
  const summary = page.getByTestId("sidebar-model-summary");
  await expect(summary).toBeVisible();
  await expect(summary).toContainText("6/6");
  await summary.click();
  await expect(page).toHaveURL(/#\/settings\/models$/);
});

test("uses the animated brand logo by default and the static one for reduced motion", async ({
  page,
}) => {
  await page.goto("/#/");
  const logo = page.locator(".topbar__logo");
  await expect(logo).toBeVisible();
  await expect(logo).toHaveAttribute("src", /logo-anim\.svg/);

  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(logo).not.toHaveAttribute("src", /logo-anim\.svg/);
});

test("collapses the sidebar to an icon rail and keeps navigation accessible", async ({ page }) => {
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();

  const collapse = page.getByRole("button", { name: "Collapse sidebar" });
  await expect(collapse).toHaveAttribute("aria-expanded", "true");
  await expect(collapse).toHaveAttribute("aria-controls", "primary-nav");
  expect(await sidebarWidth(page)).toBe(240);

  await collapse.click();

  const expand = page.getByRole("button", { name: "Expand sidebar" });
  await expect(expand).toHaveAttribute("aria-expanded", "false");
  await expect.poll(() => sidebarWidth(page)).toBe(64);

  const nav = page.getByRole("navigation", { name: "primary" });
  for (const label of NAV_LABELS) {
    await expect(nav.getByRole("link", { name: label })).toBeVisible();
  }
});

test("shows a tooltip on hover and keyboard focus in collapsed mode", async ({ page }) => {
  await page.goto("/#/");
  await page.getByRole("button", { name: "Collapse sidebar" }).click();

  // 宽度有 160ms 过渡；若在布局稳定前 hover，Playwright 可能取到过渡起点的旧坐标，
  // 指针随后被收缩中的侧栏移开，pointerleave 会取消 tooltip 打开定时器。
  await expect.poll(() => sidebarWidth(page)).toBe(64);

  const workbench = page
    .getByRole("navigation", { name: "primary" })
    .getByRole("link", { name: "Workbench" });

  await workbench.hover();
  await expect(page.getByRole("tooltip")).toHaveText("Workbench");

  await page.mouse.move(640, 420);
  await expect(page.getByRole("tooltip")).toHaveCount(0);

  await workbench.focus();
  await expect(page.getByRole("tooltip")).toHaveText("Workbench");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toHaveCount(0);
});

test("persists the collapsed sidebar across reloads", async ({ page }) => {
  await page.goto("/#/");
  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
  await expect.poll(() => sidebarWidth(page)).toBe(64);

  await page.reload();

  await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
  await expect.poll(() => sidebarWidth(page)).toBe(64);
});

test("does not show tooltips while the sidebar is expanded", async ({ page }) => {
  // 让翻译槽处于“已准入未就绪”，覆盖带 setup 徽标的菜单项。
  await seedCapabilityStates(page, { translate: "NEEDS_SETUP" });
  await page.goto("/#/");
  const nav = page.getByRole("navigation", { name: "primary" });
  const workbench = nav.getByRole("link", { name: "Workbench" });

  await workbench.hover();
  await page.waitForTimeout(250);
  await expect(page.getByRole("tooltip")).toHaveCount(0);

  const setupItem = nav.locator('.sidebar__item[data-access="setup"]').first();
  await expect(setupItem).toBeVisible();
  await setupItem.hover();
  await page.waitForTimeout(250);
  await expect(page.getByRole("tooltip")).toHaveCount(0);
});

test("opens the notification center from the top bar", async ({ page }) => {
  await page.goto("/#/");
  await page.getByRole("button", { name: "Notifications" }).click();

  const panel = page.getByRole("dialog", { name: "Notifications" });
  await expect(panel).toBeVisible();
  // 通知按项目聚合：不再出现“任务”筛选与任务中心文案。
  await expect(panel.getByRole("tab", { name: "Projects", exact: true })).toBeVisible();
  await expect(panel.getByRole("tab", { name: "Tasks", exact: true })).toHaveCount(0);
  await expect(panel.getByText("All caught up")).toBeVisible();
  await expect(
    panel.getByText("Project results, app updates and system messages will appear here."),
  ).toBeVisible();

  await page.getByRole("button", { name: "Notifications" }).click();
  await expect(panel).toHaveCount(0);
});

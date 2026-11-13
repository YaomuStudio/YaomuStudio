import { expect, test } from "@playwright/test";
import { seedOnboardedSettings } from "./support";

const PROJECT_NAME = "E2E smoke project";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

test("renders the app shell without the desktop bridge", async ({ page }) => {
  await page.goto("/");

  // 浏览器模式没有 preload 桥；webServer 显式开启 VITE_E2E_FIXTURES=1 才加载测试夹具。
  expect(
    await page.evaluate(() => typeof (window as Window & { yaomu?: unknown }).yaomu),
  ).toBe("undefined");

  await expect(page.getByRole("navigation", { name: "primary" })).toBeVisible();
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByText("YaomuStudio", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Projects", exact: true })).toBeVisible();
});

test("creates a project and shows it in the project list", async ({ page }) => {
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "primary" })
    .getByRole("link", { name: "Projects" })
    .click();

  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();

  await page.getByTestId("project-create").click();
  await page.getByTestId("project-create-input").fill(PROJECT_NAME);
  await page.getByTestId("project-create-confirm").click();

  // 创建后进入项目详情；返回列表后新项目仍应存在（mock 在同一页面会话内保存）。
  await expect(page).toHaveURL(/#\/projects\/proj-/);
  await expect(page.getByRole("heading", { level: 1, name: PROJECT_NAME })).toBeVisible();

  await page.getByRole("button", { name: "Back to projects" }).click();
  await expect(page).toHaveURL(/#\/projects$/);

  const projectList = page.getByTestId("project-list");
  await expect(projectList.getByRole("button", { name: PROJECT_NAME, exact: true })).toBeVisible();
});

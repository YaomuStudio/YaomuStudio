import { expect, test } from "@playwright/test";
import { createProject, seedOnboardedSettings } from "./support";

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

test("renames a project from the list and detail page", async ({ page }) => {
  await createProject(page, "E2E rename project");
  await page.goto("/#/projects");

  const card = page.locator("article.recent-card").filter({ hasText: "E2E rename project" });
  await card.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByTestId("project-rename-input").fill("Renamed project");
  await page.getByTestId("project-rename-confirm").click();

  const renamedCard = page.locator("article.recent-card").filter({ hasText: "Renamed project" });
  await expect(renamedCard).toBeVisible();

  // 连续第二次重命名必须使用递增后的 edit_version，不能被 409 拒绝。
  await renamedCard.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByTestId("project-rename-input").fill("Renamed twice");
  await page.getByTestId("project-rename-confirm").click();
  await expect(
    page.locator("article.recent-card").filter({ hasText: "Renamed twice" }),
  ).toBeVisible();

  // 空名称时确认按钮禁用，取消不写入。
  await page
    .locator("article.recent-card")
    .filter({ hasText: "Renamed twice" })
    .getByRole("button", { name: "Edit", exact: true })
    .click();
  await page.getByTestId("project-rename-input").fill("   ");
  await expect(page.getByTestId("project-rename-confirm")).toBeDisabled();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByTestId("project-rename-input")).toHaveCount(0);

});

test("renames a project from the home page recent card", async ({ page }) => {
  await createProject(page, "E2E home rename project");
  await page.goto("/#/");

  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "E2E home rename project" });
  await card.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByTestId("workbench-project-rename-input").fill("Home renamed project");
  await page.getByTestId("workbench-project-rename-confirm").click();

  await expect(
    page.locator("article.recent-card").filter({ hasText: "Home renamed project" }),
  ).toBeVisible();
});

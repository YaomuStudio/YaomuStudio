import { expect, test } from "@playwright/test";
import { SAMPLE_MEDIA, seedOnboardedSettings } from "./support";

test("creation page offers project navigation and continuous creation", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/tools/separate");
  await page.getByPlaceholder("Project name").fill("E2E continuous create");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await page.getByRole("button", { name: "Create" }).click();

  // 提交完成后：主按钮切到「查看详情」，旁边提供「再建一个」。
  await expect(page.getByRole("button", { name: "Create another" })).toBeVisible({
    timeout: 20_000,
  });
  // 创建后保持当前页面：右侧进度/结果面板不能一闪而过或自动跳转。
  await expect(page).toHaveURL(/#\/tools\/separate$/);
  await expect(page.locator(".results-stack")).toBeVisible();
  await expect(
    page.locator(".results-stack").getByText("E2E continuous create").first(),
  ).toBeVisible();
  // 运行中只用次级「查看详情」离开；完成后主按钮才是「查看详情」，不出现两个同名按钮。
  await expect(page.getByRole("button", { name: "View details" })).toHaveCount(1);

  // 「再建一个」清空表单，回到可再次创建的状态。
  await page.getByRole("button", { name: "Create another" }).click();
  await expect(page.getByPlaceholder("Project name")).toHaveValue("");
  await expect(page.getByRole("button", { name: "Create another" })).toHaveCount(0);
  await expect(page.locator(".ym-file-row")).toHaveCount(0);

  // 创建出的项目确实进入项目页。
  await page.goto("/#/projects");
  await expect(
    page.locator("article.recent-card").filter({ hasText: "E2E continuous create" }),
  ).toBeVisible();
});

test("projects page creation entry picks a task type", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/projects");
  await page.getByTestId("project-create").click();
  await expect(page.getByTestId("project-create-types")).toBeVisible();

  await page.getByTestId("project-create-type-separation").click();
  await expect(page).toHaveURL(/#\/tools\/separate$/);
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();
});

test("project card shows resource waiting instead of running", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.addInitScript(() => {
    const scope = window as unknown as { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.runStatusOverride = "RUNNING";
    control.projectRunStatusFixtures = { "proj-demo": "RUNNING" };
    control.projectWaitingResourceFixtures = { "proj-demo": true };
  });

  await page.goto("/#/projects");
  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  // 右上角只有项目四态：等待执行器仍属于「制作中」，具体原因在封面提示里。
  await expect(card.getByText("In production", { exact: true })).toBeVisible();
  await expect(card.getByText("Waiting for resources").first()).toBeVisible();
  await expect(card.getByText("Running", { exact: true })).toHaveCount(0);
});

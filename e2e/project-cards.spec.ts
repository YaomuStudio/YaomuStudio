import { expect, test, type Locator, type Page } from "@playwright/test";
import { seedMockControl, seedOnboardedSettings, seedProjectList, seedRunParams } from "./support";

async function chooseFilter(page: Page, filter: Locator, value: string): Promise<void> {
  await filter.locator(".ym-select__trigger").click();
  await page.locator(`.ym-select__option[data-value="${value}"]`).click();
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("renders project cards with type, status, preview and download groups", async ({ page }) => {
  await page.goto("/#/projects");
  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });

  await expect(card.getByText("Smart full-pipeline dubbing")).toBeVisible();
  await expect(card.getByText("Completed", { exact: true })).toBeVisible();
  // 已完成项目的预览播放键常显，不依赖 hover。
  await expect(card.getByRole("button", { name: "Preview video" })).toHaveCSS("opacity", "1");
  // 编号使用本地项目序号，而不是内部 ID 片段。
  await expect(card.getByText("ID:1", { exact: true })).toBeVisible();

  await card.getByRole("button", { name: "Download", exact: true }).click();
  const download = page.getByRole("dialog").filter({ hasText: "Download files" });
  await expect(download).toBeVisible();
  await expect(download.getByText("Original video", { exact: true })).toBeVisible();
  await expect(download.getByText("Voice separated audio", { exact: true })).toBeVisible();
  await download.getByRole("button", { name: "Close", exact: true }).click();

  await card.getByRole("button", { name: "Preview video" }).click();
  await expect(
    page.getByRole("dialog").filter({ hasText: "Preview video" }),
  ).toBeVisible();
});

test("filters projects by name or episode ID and keeps the query in the URL", async ({ page }) => {
  await page.goto("/#/projects");
  const cards = page.locator(".project-grid article.recent-card");
  const input = page.getByTestId("project-search-input");
  await expect(cards).toHaveCount(3);

  await input.fill("星际");
  await expect(cards).toHaveCount(3);
  await input.press("Enter");
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("星际译制 · 试播集");
  await expect(page.getByText(/^1 of 3 projects$/)).toBeVisible();
  await expect(page).toHaveURL(/q=/);

  await input.fill("#ep_demo");
  await input.press("Enter");
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("星际译制 · 试播集");

  await input.press("Escape");
  await expect(cards).toHaveCount(3);
  await expect(page).not.toHaveURL(/q=/);
});

test("shows a clearable empty state when no project matches", async ({ page }) => {
  await page.goto("/#/projects");
  const cards = page.locator(".project-grid article.recent-card");
  const input = page.getByTestId("project-search-input");

  await input.fill("不存在的项目");
  await input.press("Enter");
  await expect(cards).toHaveCount(0);
  await expect(page.getByText("No matching projects")).toBeVisible();
  await expect(page.getByText(/不存在的项目/)).toBeVisible();

  await page.locator(".ym-empty").getByRole("button", { name: "Clear search" }).click();
  await expect(cards).toHaveCount(3);
  await expect(input).toHaveValue("");
});

test("filters by type and project-level status without exposing review", async ({ page }) => {
  await page.goto("/#/projects");
  const cards = page.locator(".project-grid article.recent-card");
  const typeFilter = page.getByTestId("project-type-filter");
  const statusFilter = page.getByTestId("project-status-filter");

  await chooseFilter(page, typeFilter, "clone");
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("产品宣传片配音");
  await expect(page).toHaveURL(/type=clone/);

  await chooseFilter(page, typeFilter, "extract");
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("会议录音转写");

  await chooseFilter(page, typeFilter, "all");
  await chooseFilter(page, statusFilter, "completed");
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("星际译制 · 试播集");
  await expect(cards.first().getByText("Completed", { exact: true })).toBeVisible();
  await expect(page.getByText("Pending review", { exact: true })).toHaveCount(0);

  await chooseFilter(page, statusFilter, "pending");
  await expect(cards).toHaveCount(2);
  await expect(page).toHaveURL(/status=pending/);

  await page.getByTestId("project-filter-clear").click();
  await expect(cards).toHaveCount(3);
  await expect(page).not.toHaveURL(/type=|status=/);
  await expect(typeFilter.locator(".ym-select__value")).toContainText("All types");
  await expect(statusFilter.locator(".ym-select__value")).toContainText("All statuses");
});

test("sorts by recent use or creation time and remembers the choice", async ({ page }) => {
  await seedProjectList(page, [
    {
      id: "proj-alpha",
      name: "Alpha",
      editVersion: 1,
      episodeId: "ep_alpha",
      modeType: "full_pipeline",
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-03T00:00:00Z",
      lastOpenedAt: "2026-01-03T00:00:00Z",
      assetCount: 0,
      runCount: 0,
    },
    {
      id: "proj-beta",
      name: "Beta",
      editVersion: 1,
      episodeId: "ep_beta",
      modeType: "high_sentiment_tts_clone",
      createdAt: "2026-01-02T00:00:00Z",
      updatedAt: "2026-01-01T00:00:00Z",
      lastOpenedAt: "2026-01-01T00:00:00Z",
      assetCount: 0,
      runCount: 0,
    },
    {
      id: "proj-gamma",
      name: "Gamma",
      editVersion: 1,
      episodeId: "ep_gamma",
      modeType: "subtitle_extract_asr",
      createdAt: "2026-01-03T00:00:00Z",
      updatedAt: "2026-01-02T00:00:00Z",
      lastOpenedAt: "2026-01-02T00:00:00Z",
      assetCount: 0,
      runCount: 0,
    },
  ]);
  await page.goto("/#/projects");
  const names = page.locator(".project-grid article.recent-card .recent-card__name");
  const sort = page.getByTestId("project-sort");

  await expect(sort.locator(".ym-select__value")).toContainText("Recently used");
  await expect(names).toHaveText(["Alpha", "Gamma", "Beta"]);

  await chooseFilter(page, sort, "created");
  await expect(names).toHaveText(["Alpha", "Beta", "Gamma"]);

  await page.reload();
  await expect(
    page.getByTestId("project-sort").locator(".ym-select__value"),
  ).toContainText("Creation time");
  await expect(names).toHaveText(["Alpha", "Beta", "Gamma"]);
});

test("shows a frozen duration adjustment as recorded but not applied", async ({ page }) => {
  await seedRunParams(page, { "proj-demo": { adjustDuration: true } });
  await page.goto("/#/projects");
  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  const row = card
    .locator(".recent-card__detail")
    .filter({ hasText: "Dynamic duration adjustment" });
  await expect(row).toContainText("Recorded on · not applied");
});

test("does not guess false for an unrecorded duration adjustment", async ({ page }) => {
  await seedRunParams(page, { "proj-demo": { adjustDuration: null } });
  await page.goto("/#/projects");
  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  const row = card
    .locator(".recent-card__detail")
    .filter({ hasText: "Dynamic duration adjustment" });
  await expect(row).toContainText("Not recorded");
});

test("keeps the task center hidden while cards open project detail", async ({ page }) => {
  await page.goto("/#/projects");
  await expect(
    page.getByRole("navigation", { name: "primary" }).getByRole("link", { name: "Tasks" }),
  ).toHaveCount(0);

  await page.locator("article.recent-card").first().locator("button.recent-card__name").click();
  await expect(page).toHaveURL(/#\/projects\/proj-[^/?]+$/);

  await page.goto("/#/tasks");
  await expect(page).toHaveURL(/#\/projects$/);
});

test("matches workbench and projects ordering", async ({ page }) => {
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  await expect(page.locator(".recent-grid article.recent-card").first()).toBeVisible();
  const workbenchOrder = await page
    .locator(".recent-grid article.recent-card .recent-card__name")
    .allTextContents();
  expect(workbenchOrder.length).toBeGreaterThan(0);

  await page.goto("/#/projects");
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();
  await expect(page.locator(".project-grid article.recent-card").first()).toBeVisible();
  const projectOrder = await page
    .locator(".project-grid article.recent-card .recent-card__name")
    .allTextContents();

  expect(projectOrder.slice(0, workbenchOrder.length)).toEqual(workbenchOrder);
});

test("orders and right-aligns card actions by frequency", async ({ page }) => {
  await page.goto("/#/projects");

  const review = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  const reviewFoot = review.locator(".recent-card__foot");
  await expect(reviewFoot.getByRole("button")).toHaveText([
    "Manual correction",
    "Download",
    "Edit",
    "Delete",
  ]);
  await expect(reviewFoot).toHaveCSS("justify-content", "flex-end");

  const idle = page
    .locator("article.recent-card")
    .filter({ hasText: "产品宣传片配音" });
  const idleFoot = idle.locator(".recent-card__foot");
  await expect(idleFoot.getByRole("button")).toHaveText([
    "Download",
    "Edit",
    "Delete",
  ]);

  const deleteColors = await page.locator(".recent-card__delete").evaluateAll((elements) =>
    elements.map((element) => getComputedStyle(element).color),
  );
  expect(deleteColors.length).toBeGreaterThan(0);
  expect(new Set(deleteColors).size).toBe(1);
});

test("shows simulated type-specific details for clone and extract cards", async ({ page }) => {
  await page.goto("/#/projects");

  const clone = page
    .locator("article.recent-card")
    .filter({ hasText: "产品宣传片配音" });
  await expect(clone.getByText("1 WAV / 2 SRT")).toBeVisible();
  await expect(clone.getByText("Subtitle timestamp check：Off")).toBeVisible();
  await expect(clone.getByText("Dynamic duration adjustment：Off")).toBeVisible();
  await expect(clone.getByText(/语言：/)).toHaveCount(0);

  const extract = page
    .locator("article.recent-card")
    .filter({ hasText: "会议录音转写" });
  await expect(extract.getByText("1 WAV")).toBeVisible();
  await expect(extract.getByText("Extraction method：ASR")).toBeVisible();
  await expect(extract.getByText("Source language：简体中文")).toBeVisible();
  await expect(extract.getByText(/语言：/)).toHaveCount(0);
});

test("renders target languages as complete chips without a generic language row", async ({ page }) => {
  await page.goto("/#/projects");

  const card = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  // 旧的泛化“语言”行已移除；目标语言行只保留一次。
  await expect(card.getByText("Language：", { exact: true })).toHaveCount(0);
  const row = card.locator(".recent-card__detail--languages");
  await expect(row).toBeVisible();
  await expect(row.locator(".recent-card__lang-chip").first()).toHaveText("English");
  await expect(card.getByTestId("recent-card-lang-count")).toContainText(
    "Translation & dubbing languages count",
  );
});

test("target language chips follow the dark theme tokens", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({ onboarded: true, locale: "en", theme: "dark" }),
    );
  });
  await page.goto("/#/projects");

  const chip = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" })
    .locator(".recent-card__lang-chip")
    .first();
  const styles = await chip.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      theme: document.documentElement.getAttribute("data-theme"),
      color: style.color,
      background: style.backgroundColor,
      border: style.borderColor,
    };
  });
  expect(styles.theme).toBe("dark");
  // 深色主题使用 --color-primary-ink 的暗色值，不能落回浅色主题的深蓝。
  expect(styles.color).toBe("rgb(102, 170, 255)");
  expect(styles.background).not.toBe("rgb(255, 255, 255)");
});

test("project detail shows type and latest run status badges without dropping main actions", async ({
  page,
}) => {
  await page.goto("/#/projects/proj-demo");

  const meta = page.locator(".page-header__meta");
  await expect(meta.getByText("Smart full-pipeline dubbing", { exact: true })).toBeVisible();
  await expect(meta.getByText("Completed", { exact: true })).toBeVisible();
  await expect(page.locator(".page-header__desc")).toHaveText(
    /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/,
  );

  // 内部 WAITING_REVIEW 不再显示为“待审核”；项目事实状态已在页头展示。
  await expect(page.getByText("Pending review", { exact: true })).toHaveCount(0);

  // main 独有入口保持不变：人工校正、打开目录；有 Run 时不显示创建任务。
  await expect(page.getByTestId("project-detail-correction")).toBeVisible();
  const openExport = page.getByRole("button", { name: "Open containing folder" });
  await expect(openExport).toBeVisible();
  // 没有已完成导出时不能打开内部项目目录；按钮禁用并给出原因。
  await expect(openExport).toBeDisabled();
  await expect(page.getByTestId("project-detail-create-task")).toHaveCount(0);
  await expect(page.getByTestId("project-detail-not-started")).toHaveCount(0);
});

test("no-run project detail shows the not-started guide and opens configuration without starting", async ({
  page,
}) => {
  await page.goto("/#/projects");
  await page
    .locator("article.recent-card")
    .filter({ hasText: "产品宣传片配音" })
    .locator("button.recent-card__name")
    .click();
  await expect(page).toHaveURL(/#\/projects\/proj-clone$/);

  const meta = page.locator(".page-header__meta");
  await expect(meta.getByText("High-emotion clone dubbing", { exact: true })).toBeVisible();
  // 无 Run 显示真实类型 + 待制作状态，并隐藏恒为 0 的运行统计。
  await expect(meta.locator(".ym-badge")).toHaveCount(2);
  await expect(meta.getByText("Not started", { exact: true })).toBeVisible();
  await expect(page.locator(".ym-stats").getByText("Runs", { exact: true })).toHaveCount(0);

  const guide = page.getByTestId("project-detail-not-started");
  await expect(guide.getByText("Production not started")).toBeVisible();
  await expect(
    guide.getByText("The project exists, but no production task has been created yet."),
  ).toBeVisible();

  await guide.getByTestId("project-detail-create-task").click();
  await expect(page).toHaveURL(/#\/projects\/proj-clone\/pipeline$/);
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();

  const lastRun = await page.evaluate(() => {
    const scope = window as Window & { __yaomuMock?: { lastRunInput?: unknown } };
    return scope.__yaomuMock?.lastRunInput;
  });
  expect(lastRun).toBeUndefined();
});

test("active run shows status without run history or a per-node progress panel", async ({ page }) => {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.runStatusOverride = "RUNNING";
  });
  await page.goto("/#/projects/proj-demo");

  const meta = page.locator(".page-header__meta");
  await expect(meta.getByText("In production", { exact: true })).toBeVisible();
  // 节点百分比无法保证实时同步，制作进度面板已移除；只保留事实状态。
  await expect(page.getByTestId("project-detail-progress")).toHaveCount(0);

  // 运行记录卡片已移除：详情只保留项目事实状态、素材与字幕轨。
  await expect(page.locator(".run")).toHaveCount(0);
  await expect(page.locator(".run-node")).toHaveCount(0);
  await expect(page.getByTestId("project-detail-not-started")).toHaveCount(0);
});

test("downloads an asset directly from project detail", async ({ page }) => {
  await page.goto("/#/projects/proj-demo");

  const row = page.locator(".asset-row").filter({ hasText: "episode01.mp4" });
  const download = row.getByRole("button", { name: "Download", exact: true });
  await expect(download).toBeVisible();
  await download.click();

  await expect
    .poll(() =>
      page.evaluate(() => {
        const scope = window as Window & {
          __yaomuMock?: { lastSaveAssetInput?: unknown };
        };
        return scope.__yaomuMock?.lastSaveAssetInput;
      }),
    )
    .toEqual({
      assetId: "asset-video-1",
      suggestedName: "星际译制 · 试播集_Original video.mp4",
    });
  await expect(
    page
      .getByRole("status")
      .filter({ hasText: "Saved 星际译制 · 试播集_Original video.mp4." }),
  ).toBeVisible();
});

test("failed run offers recreate and shows production failed", async ({ page }) => {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.runStatusOverride = "FAILED";
  });
  await page.goto("/#/projects/proj-demo");

  const meta = page.locator(".page-header__meta");
  await expect(meta.getByText("Production failed", { exact: true })).toBeVisible();
  const recreate = page.getByTestId("project-detail-recreate");
  await expect(recreate).toBeVisible();
  await recreate.click();
  await expect(page).toHaveURL(/#\/pipeline\/new$/);
});

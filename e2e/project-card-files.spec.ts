import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_AUDIO,
  SAMPLE_MEDIA,
  createProject,
  openPipelineForProject,
  seedAssetFixtures,
  seedCapabilityStates,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

interface FixtureAssetInput {
  projectId: string;
  id: string;
  name: string;
  type: "video" | "audio" | "audio_track" | "subtitle";
  role?: string;
  language?: string;
  runId?: string;
}

function asset(input: FixtureAssetInput): Record<string, unknown> {
  return {
    sizeBytes: 1024,
    status: "ready",
    importedAt: "2026-11-06T00:00:00.000Z",
    ...input,
  };
}

function mockDownloadInput(page: Page) {
  return page.evaluate(() => {
    const scope = window as Window & {
      __yaomuMock?: { lastSaveAssetInput?: { suggestedName?: string } };
    };
    return scope.__yaomuMock?.lastSaveAssetInput;
  });
}

function card(page: Page, name: string) {
  return page.locator("article.recent-card").filter({ hasText: name });
}

async function openDownloadDialog(page: Page, name: string) {
  await card(page, name).getByRole("button", { name: "Download", exact: true }).click();
  const dialog = page.getByRole("dialog").filter({ hasText: "Download files" });
  await expect(dialog).toBeVisible();
  return dialog;
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("imported WAV and MP4 keep their real type in the card download list", async ({ page }) => {
  const projectId = await createProject(page, "E2E card import");
  await openPipelineForProject(page, projectId);
  await page
    .locator('input[type="file"]')
    .setInputFiles([SAMPLE_MEDIA, SAMPLE_AUDIO]);
  await expect(
    page.getByRole("status").filter({ hasText: "2 files imported" }),
  ).toBeVisible();

  await page
    .getByRole("navigation", { name: "primary" })
    .getByRole("link", { name: "Projects" })
    .click();
  await expect(page).toHaveURL(/#\/projects$/);

  const dialog = await openDownloadDialog(page, "E2E card import");
  await expect(dialog.getByText("Original video", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Original audio", { exact: true })).toBeVisible();
  await expect(
    dialog.getByText("E2E card import_Original video.mp4", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("E2E card import_Original audio.wav", { exact: true }),
  ).toBeVisible();
});

test("labels imported WAV as original audio and lets the user save it", async ({ page }) => {
  await seedAssetFixtures(page, {
    "proj-demo": [
      asset({
        projectId: "proj-demo",
        id: "asset-video-original",
        name: "clip.mp4",
        type: "video",
        role: "original",
      }),
      asset({
        projectId: "proj-demo",
        id: "asset-audio-original",
        name: "record.wav",
        type: "audio",
        role: "original",
      }),
    ],
  });

  await page.goto("/#/projects");
  const dialog = await openDownloadDialog(page, "星际译制 · 试播集");

  await expect(dialog.getByText("Original video", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Original audio", { exact: true })).toBeVisible();
  await expect(
    dialog.getByText("星际译制 · 试播集_Original video.mp4", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("星际译制 · 试播集_Original audio.wav", { exact: true }),
  ).toBeVisible();

  await dialog
    .locator("button.download-row")
    .filter({ hasText: "Original audio" })
    .click();
  await expect
    .poll(() => mockDownloadInput(page))
    .toEqual({ assetId: "asset-audio-original", suggestedName: "星际译制 · 试播集_Original audio.wav" });
});

test("keeps imported subtitles, same-language files and unknown roles as distinct items", async ({
  page,
}) => {
  await seedAssetFixtures(page, {
    "proj-demo": [
      asset({
        projectId: "proj-demo",
        id: "sub-import-1",
        name: "first.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-import-2",
        name: "second.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-en-1",
        name: "en.srt",
        type: "subtitle",
        role: "translated_subtitle",
        language: "en",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-en-2",
        name: "en-alt.srt",
        type: "subtitle",
        role: "translated_subtitle",
        language: "en",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-ja-1",
        name: "ja.srt",
        type: "subtitle",
        role: "translated_subtitle",
        language: "ja",
      }),
      asset({
        projectId: "proj-demo",
        id: "legacy-video",
        name: "legacy.mp4",
        type: "video",
      }),
      asset({
        projectId: "proj-demo",
        id: "legacy-unknown-role",
        name: "legacy-unknown.wav",
        type: "audio",
        role: "legacy_role",
      }),
    ],
  });

  await page.goto("/#/projects");
  const dialog = await openDownloadDialog(page, "星际译制 · 试播集");

  // 导入 SRT 没有可靠语言：保留原文件名识别，不猜成译文语种。
  await expect(
    dialog.getByText("Imported subtitles · first.srt", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("Imported subtitles · second.srt", { exact: true }),
  ).toBeVisible();
  await expect(dialog.getByText("English subtitles", { exact: true })).toHaveCount(2);
  await expect(dialog.getByText("日本語 subtitles", { exact: true })).toBeVisible();
  // 无 role 与未知 role 的旧资产不能因同项目存在 role 资产而消失。
  await expect(dialog.getByText("Video · legacy.mp4", { exact: true })).toBeVisible();
  await expect(
    dialog.getByText("Audio · legacy-unknown.wav", { exact: true }),
  ).toBeVisible();

  const names = [
    "星际译制 · 试播集_Imported subtitles_first.srt",
    "星际译制 · 试播集_Imported subtitles_second.srt",
    "星际译制 · 试播集_English subtitles.srt",
    "星际译制 · 试播集_English subtitles (2).srt",
    "星际译制 · 试播集_日本語 subtitles.srt",
    "星际译制 · 试播集_Video_legacy.mp4",
    "星际译制 · 试播集_Audio_legacy-unknown.wav",
  ];
  for (const name of names) {
    await expect(dialog.getByText(name, { exact: true })).toBeVisible();
  }
});

test("sanitizes illegal characters and de-duplicates case-insensitive collisions", async ({
  page,
}) => {
  await seedAssetFixtures(page, {
    "proj-demo": [
      asset({
        projectId: "proj-demo",
        id: "sub-colon",
        name: "a:b.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-question",
        name: "a?b.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-upper",
        name: "Clip.SRT",
        type: "subtitle",
        role: "translated_subtitle",
        language: "en",
      }),
      asset({
        projectId: "proj-demo",
        id: "sub-lower",
        name: "clip.srt",
        type: "subtitle",
        role: "translated_subtitle",
        language: "en",
      }),
    ],
  });

  await page.goto("/#/projects");
  const dialog = await openDownloadDialog(page, "星际译制 · 试播集");

  await expect(
    dialog.getByText("星际译制 · 试播集_Imported subtitles_a_b.srt", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("星际译制 · 试播集_Imported subtitles_a_b (2).srt", { exact: true }),
  ).toBeVisible();
  // 扩展名保持资产实际大小写；后出现项在扩展名前追加稳定序号。
  await expect(
    dialog.getByText("星际译制 · 试播集_English subtitles.SRT", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("星际译制 · 试播集_English subtitles (2).srt", { exact: true }),
  ).toBeVisible();

  const names = await dialog.locator(".download-row__name").allTextContents();
  expect(new Set(names.map((name) => name.toLowerCase())).size).toBe(names.length);
});

test("keeps long multibyte suggested names unique within desktop limits", async ({ page }) => {
  const zhCommon = "超长导入字幕公共前缀".repeat(8);
  // 无空格/无连字符的长 ASCII，复现真实文件中 label 横向溢出的情况。
  const enCommon = "x".repeat(140);
  await seedAssetFixtures(page, {
    "proj-trans": [
      asset({
        projectId: "proj-trans",
        id: "long-zh-1",
        name: `${zhCommon}乙.srt`,
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-trans",
        id: "long-zh-2",
        name: `${zhCommon}甲.srt`,
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-trans",
        id: "long-en-1",
        name: `${enCommon}alpha.srt`,
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-trans",
        id: "long-en-2",
        name: `${enCommon}beta.srt`,
        type: "subtitle",
        role: "source_subtitle",
      }),
    ],
  });

  await page.goto("/#/projects");
  const dialog = await openDownloadDialog(page, "会议录音转写");
  const names = await dialog.locator(".download-row__name").allTextContents();
  expect(names).toHaveLength(4);
  for (const name of names) {
    // 与 desktop/src/ipc/asset-content.ts 的 basename+slice(0,120) 上限一致。
    expect(name.length).toBeLessThanOrEqual(120);
    // 常见文件系统单个文件名 255 字节（UTF-8）。
    expect(Buffer.byteLength(name, "utf8")).toBeLessThanOrEqual(255);
    expect(name).toMatch(/\.srt$/);
    expect(name).not.toContain("�");
  }
  expect(new Set(names.map((name) => name.toLowerCase())).size).toBe(names.length);
  expect(
    new Set(names.map((name) => name.split(/[\\/]/).pop()!.slice(0, 120).toLowerCase())).size,
  ).toBe(names.length);
  // 两个长共同前缀各自发生碰撞时，(2) 后缀不能被裁剪掉。
  expect(names.filter((name) => name.includes(" (2).srt"))).toHaveLength(2);

  // 长无空格文件名的 label 必须换行/约束在名称列内，不能横向盖住扩展名列。
  const rows = dialog.locator("button.download-row");
  await expect(rows).toHaveCount(4);
  for (let index = 0; index < 4; index += 1) {
    const row = rows.nth(index);
    const label = row.locator(".download-row__label");
    const ext = row.locator(".download-row__ext");
    const name = row.locator(".download-row__name");
    const extBox = await ext.boundingBox();
    expect(extBox).not.toBeNull();
    const textRight = await label.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      return range.getBoundingClientRect().right;
    });
    expect(textRight).toBeLessThanOrEqual(extBox!.x + 0.5);
    const overflow = await label.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth + 1);
    await expect(ext).toBeVisible();
    // 被 ellipsis 截断的建议名保留完整 title 便于查看。
    expect(await name.getAttribute("title")).toBe(await name.textContent());
  }
});

test("keeps a natural (2) name and resolves later collisions deterministically", async ({
  page,
}) => {
  await seedAssetFixtures(page, {
    "proj-trans": [
      asset({
        projectId: "proj-trans",
        id: "dup-a",
        name: "a-b (2).srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-trans",
        id: "dup-b",
        name: "a-b.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
      asset({
        projectId: "proj-trans",
        id: "dup-c",
        name: "a-b.srt",
        type: "subtitle",
        role: "source_subtitle",
      }),
    ],
  });

  await page.goto("/#/projects");
  const dialog = await openDownloadDialog(page, "会议录音转写");
  await expect(
    dialog.getByText("会议录音转写_Imported subtitles_a-b (2).srt", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("会议录音转写_Imported subtitles_a-b.srt", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText("会议录音转写_Imported subtitles_a-b (3).srt", { exact: true }),
  ).toBeVisible();
});

test("keeps a legacy audio item for a completed extraction run", async ({ page }) => {
  await page.goto("/#/tools/extract");
  await expect(
    page.getByRole("heading", { level: 1, name: "Subtitle extraction" }),
  ).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E legacy extract");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_AUDIO);
  await expect(page.getByText(SAMPLE_AUDIO.name)).toBeVisible();
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Extracted line one.")).toBeVisible();

  const nav = page.getByRole("navigation", { name: "primary" });
  await nav.getByRole("link", { name: "Projects" }).click();
  await expect(page).toHaveURL(/#\/projects$/);

  // 通过详情 URL 取得 mock 项目 ID，再在 SPA 会话内注入混合旧资产。
  const target = card(page, "E2E legacy extract");
  await target.locator("button.recent-card__name").click();
  await page.waitForURL(/#\/projects\/proj-[^/?]+$/);
  const projectId = /#\/projects\/(proj-[^/?]+)/.exec(page.url())?.[1];
  if (!projectId) throw new Error(`Unexpected project URL: ${page.url()}`);
  await page.evaluate(
    ({ id, assets }) => {
      const scope = window as Window & {
        __yaomuMock?: { assetFixtures?: Record<string, unknown[]> };
      };
      const control = scope.__yaomuMock ?? (scope.__yaomuMock = {});
      control.assetFixtures = { ...(control.assetFixtures ?? {}), [id]: assets };
    },
    {
      id: projectId,
      assets: [
        asset({
          projectId,
          id: "legacy-original-audio",
          name: "mix-source.wav",
          type: "audio",
          role: "original",
        }),
        asset({
          projectId,
          id: "legacy-unknown-audio",
          name: "legacy-mix.wav",
          type: "audio",
        }),
      ],
    },
  );

  await nav.getByRole("link", { name: "Projects" }).click();
  await expect(page).toHaveURL(/#\/projects$/);
  const dialog = await openDownloadDialog(page, "E2E legacy extract");
  // 未知 role 的文件级条目不能被音频分组门控丢弃，也不能被猜成业务语义。
  await expect(dialog.getByText("Original audio", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Audio · legacy-mix.wav", { exact: true })).toBeVisible();
});

test("single visible item is saved directly with the unique suggested name", async ({ page }) => {
  await seedAssetFixtures(page, {
    "proj-demo": [
      asset({
        projectId: "proj-demo",
        id: "sub-en-only",
        name: "en.srt",
        type: "subtitle",
        role: "translated_subtitle",
        language: "en",
      }),
    ],
  });

  await page.goto("/#/projects");
  const demo = card(page, "星际译制 · 试播集");
  await demo.getByRole("button", { name: "Download", exact: true }).click();

  await expect
    .poll(() => mockDownloadInput(page))
    .toEqual({
      assetId: "sub-en-only",
      suggestedName: "星际译制 · 试播集_English subtitles.srt",
    });
});

test("keeps an imported audio original downloadable after an extraction run", async ({ page }) => {
  await page.goto("/#/tools/extract");
  await expect(
    page.getByRole("heading", { level: 1, name: "Subtitle extraction" }),
  ).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E extract audio");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_AUDIO);
  await expect(page.getByText(SAMPLE_AUDIO.name)).toBeVisible();
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Extracted line one.")).toBeVisible();

  // 字幕提取单点类型的文档包含「原件」；导入的音频原件不能因音频分组门控消失。
  await page.goto("/#/projects");
  const target = card(page, "E2E extract audio");
  const download = target.getByRole("button", { name: "Download", exact: true });
  await expect(download).toBeEnabled();
  await download.click();
  await expect
    .poll(async () => (await mockDownloadInput(page))?.suggestedName)
    .toBe("E2E extract audio_Original audio.wav");
});

test("no-run project creates a task from detail with the project id and creates no run", async ({
  page,
}) => {
  await page.goto("/#/projects");
  const target = card(page, "产品宣传片配音");
  await target.locator("button.recent-card__name").click();
  await expect(page).toHaveURL(/#\/projects\/proj-clone$/);

  await page.getByTestId("project-detail-create-task").click();

  await expect(page).toHaveURL(/#\/projects\/proj-clone\/pipeline$/);
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();
  const lastRun = await page.evaluate(() => {
    const scope = window as Window & { __yaomuMock?: { lastRunInput?: unknown } };
    return scope.__yaomuMock?.lastRunInput;
  });
  expect(lastRun).toBeUndefined();
});

test("no-run and waiting-review cards keep distinct actions, while detail keeps edit access", async ({
  page,
}) => {
  await page.goto("/#/projects");

  const review = card(page, "星际译制 · 试播集");
  await expect(review.getByRole("button", { name: "Manual correction" })).toBeVisible();
  await expect(review.getByRole("button", { name: "Continue configuration" })).toHaveCount(0);

  const empty = card(page, "产品宣传片配音");
  await expect(empty.getByRole("button", { name: "Continue configuration" })).toHaveCount(0);
  await expect(empty.getByRole("button", { name: "Manual correction" })).toHaveCount(0);

  // 详情页对存在译文轨的项目保留独立编辑入口；不等同于审核放行。
  await page.goto("/#/projects/proj-demo");
  await expect(page.getByTestId("project-detail-correction")).toBeVisible();
  await expect(page.getByTestId("project-detail-create-task")).toHaveCount(0);

  await page.goto("/#/projects/proj-clone");
  await expect(page.getByTestId("project-detail-correction")).toHaveCount(0);
  await expect(page.getByTestId("project-detail-create-task")).toBeVisible();
});

test("hidden pipeline entry keeps the project context and points to the workbench", async ({
  page,
}) => {
  await seedCapabilityStates(page, { ffmpeg: "UNSUPPORTED" });

  await page.goto("/#/projects");
  const target = card(page, "产品宣传片配音");
  await target.locator("button.recent-card__name").click();
  await expect(page).toHaveURL(/#\/projects\/proj-clone$/);

  await page.getByTestId("project-detail-create-task").click();

  await expect(page).toHaveURL(/#\/\?project=proj-clone$/);
  await expect(page).not.toHaveURL(/#\/pipeline/);
});

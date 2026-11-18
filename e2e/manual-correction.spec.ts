import { expect, test, type Locator, type Page } from "@playwright/test";
import {
  readApproveCalls,
  readApproveInputs,
  readExportCalls,
  readExportInputs,
  seedExportFailure,
  seedMockControl,
  seedOnboardedSettings,
  seedReviewFixtures,
  seedReviewScenario,
  seedSaveSubtitleFailure,
  type ReviewGateFixture,
} from "./support";

// 浏览器 E2E 运行在 mock API 上（无 window.yaomu 桥），只证明渲染/交互结构；
// 真实 Core 持久化、舞台媒体播放与候选试听由 .design-build 下的隔离 Electron 脚本验证。
const saveActions = (page: Page) =>
  page.getByRole("button", { name: /^Save( corrections)?$/ });

async function openCorrection(page: Page): Promise<void> {
  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByText("星际译制 · 试播集")).toBeVisible();
  await expect(page.getByTestId("segment-row").first()).toBeVisible();
}

async function readLastSavedRevision(page: Page): Promise<unknown> {
  return page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: { lastSavedRevision?: unknown } })
      .__yaomuMock;
    return control?.lastSavedRevision ?? null;
  });
}

async function readSaveSubtitleCalls(page: Page): Promise<number> {
  return page.evaluate(() => {
    const control = (
      window as Window & { __yaomuMock?: { saveSubtitleCalls?: number } }
    ).__yaomuMock;
    return control?.saveSubtitleCalls ?? 0;
  });
}

async function clearTranslationInput(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    scope.__yaomuMock = { ...(scope.__yaomuMock ?? {}), clearTranslationInput: true };
  });
}

async function readSavedTracks(
  page: Page,
): Promise<{ trackId: string; text: string; startMs: number; endMs: number }[]> {
  return page.evaluate(() => {
    const control = (
      window as Window & {
        __yaomuMock?: {
          lastSavedTracks?: { trackId: string; text: string; startMs: number; endMs: number }[];
        };
      }
    ).__yaomuMock;
    return control?.lastSavedTracks ?? [];
  });
}

/**
 * 显式打开 mock 的重新合成推进：译文放行后模拟 07/08 完成并停在交付审核；
 * 不自动放行 delivery（仍由人工在流水线页处理）。
 */
async function enableRemixAdvance(page: Page): Promise<void> {
  await page.evaluate(() => {
    const scope = window as Window & { __yaomuMock?: { remixOnApprove?: boolean } };
    scope.__yaomuMock ??= {};
    scope.__yaomuMock.remixOnApprove = true;
  });
}

/** mock 的当前交付绑定：mix + 当前字幕 revision；已放行后三种导出都应可用。 */
const approvedMixDeliveryFixture: ReviewGateFixture = {
  id: "fixture-delivery-mix",
  runId: "run-demo-1",
  type: "delivery",
  language: "en",
  revisionDigest: "mix:run-demo-1:en",
  status: "approved",
  currentBindings: [
    {
      language: "en",
      sourceKind: "delivery_mix",
      sourceId: "task-mix:run-demo-1:en",
    },
  ],
};

const approvedSubtitleDeliveryFixture: ReviewGateFixture = {
  id: "fixture-delivery-subtitle",
  runId: "run-demo-1",
  type: "delivery",
  language: "en",
  revisionDigest: "delivery:rev-demo-en-1",
  status: "approved",
  currentBindings: [
    {
      language: "en",
      sourceKind: "delivery_revision",
      sourceId: "rev-demo-en-1",
    },
  ],
};

const approvedDeliveryFixtures: ReviewGateFixture[] = [
  approvedMixDeliveryFixture,
  approvedSubtitleDeliveryFixture,
];

async function openExport(page: Page): Promise<Locator> {
  await page.getByTestId("mcs-export-open").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Export" })).toBeVisible();
  return dialog;
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("shows one save action and one dirty status on the manual correction page", async ({
  page,
}) => {
  await openCorrection(page);

  // 人工校正页顶栏是唯一保存权威：面板内没有第二个保存按钮或 dirty 徽标。
  await expect(saveActions(page)).toHaveCount(1);
  await expect(
    page.getByRole("button", { name: "Save corrections", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Content correction" })).toBeVisible();
  await expect(page.getByText("8 sentences")).toBeVisible();
  // 三栏骨架固定三个页签：内容 / 样式 / 擦除。
  await expect(page.getByRole("tab")).toHaveCount(3);

  const save = saveActions(page).first();
  await expect(save).toBeDisabled();

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Edited translation");

  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(1);
  await expect(save).toBeEnabled();

  await save.click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(0);
  await expect(save).toBeDisabled();
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Edited translation");
});

test("autosaves after idle and coalesces rapid edits", async ({ page }) => {
  await openCorrection(page);
  const textarea = page.getByTestId("segment-row").first().getByTestId("segment-translation");
  await textarea.fill("Autosave first");
  await textarea.fill("Autosave final");

  // 1.5s 防抖后自动保存；连续输入只形成一个 revision。
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
  const saved = (await readLastSavedRevision(page)) as {
    segments?: { text?: string }[];
  } | null;
  expect(saved?.segments?.[0]?.text).toBe("Autosave final");
  expect(await readSaveSubtitleCalls(page)).toBe(1);
});

test("edits and autosaves the source transcription independently", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  const source = firstRow.getByTestId("segment-source");
  const translation = firstRow.getByTestId("segment-translation");
  const translationBefore = await translation.inputValue();

  await source.fill("Edited source line");
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);

  const saved = (await readLastSavedRevision(page)) as {
    trackId?: string;
    segments?: { text?: string }[];
  } | null;
  expect(saved?.trackId).toBe("track-demo-zh");
  expect(saved?.segments?.[0]?.text).toBe("Edited source line");
  await expect(translation).toHaveValue(translationBefore);
  await expect(page.getByTestId("source-changed-notice")).toBeVisible();
});

test("saves source and translation as separate track revisions", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-source").fill("Source track edit");
  await firstRow.getByTestId("segment-translation").fill("Translation track edit");

  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
  const tracks = await readSavedTracks(page);
  expect(
    tracks.some((item) => item.trackId === "track-demo-zh" && item.text === "Source track edit"),
  ).toBe(true);
  expect(
    tracks.some((item) => item.trackId === "track-demo-en" && item.text === "Translation track edit"),
  ).toBe(true);
  // 两轨一起保存时译文 input 会记录新的源 revision，不残留过期提示。
  await expect(page.getByTestId("source-changed-notice")).toHaveCount(0);
});

test("treats missing input revision as unknown, not fresh", async ({ page }) => {
  await clearTranslationInput(page);
  await openCorrection(page);
  await expect(page.getByTestId("source-unknown-notice")).toBeVisible();

  await page.getByRole("button", { name: "Confirm translation and remix" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/source transcription version.*unknown/i)).toBeVisible();
});

test("applies time edits to both paired source and translation tracks", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.locator("input").first().fill("0.1");

  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
  const tracks = await readSavedTracks(page);
  expect(tracks.find((item) => item.trackId === "track-demo-zh")?.startMs).toBe(100);
  expect(tracks.find((item) => item.trackId === "track-demo-en")?.startMs).toBe(100);
});

test("warns before remixing after the source transcription changes", async ({ page }) => {
  await openCorrection(page);
  await page
    .getByTestId("segment-row")
    .first()
    .getByTestId("segment-source")
    .fill("Source changed before remix");
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);

  await page.getByRole("button", { name: "Confirm translation and remix" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/source transcription changed/i)).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
});

test("keeps source staleness after leaving and returning to the correction page", async ({
  page,
}) => {
  await openCorrection(page);
  await page
    .getByTestId("segment-row")
    .first()
    .getByTestId("segment-source")
    .fill("Persistent source change");
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);
  await expect(page.getByTestId("source-changed-notice")).toBeVisible();

  await page.getByRole("button", { name: "Back to project" }).click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo$/);
  await page.goBack();
  await expect(page).toHaveURL(/#\/manual-correction\/proj-demo$/);
  await expect(page.getByTestId("source-changed-notice")).toBeVisible();
});

test("clears source staleness after saving the translation", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-source").fill("Source changed");
  await firstRow.getByTestId("segment-translation").fill("Translation updated after source");
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);

  await expect(page.getByTestId("source-changed-notice")).toHaveCount(0);
});

test("keeps the source draft when source autosave conflicts", async ({ page }) => {
  await seedSaveSubtitleFailure(page, "conflict");
  await openCorrection(page);
  const source = page.getByTestId("segment-row").first().getByTestId("segment-source");
  await source.fill("Source conflict draft");

  await expect(page.locator(".mcs-status")).toHaveText(/Content changed/);
  await expect(source).toHaveValue("Source conflict draft");
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("saves unsaved corrections before leaving without prompting", async ({ page }) => {
  await openCorrection(page);
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Saved before leaving");

  await page.getByRole("button", { name: "Back to project" }).click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo$/);
  const saved = (await readLastSavedRevision(page)) as {
    segments?: { text?: string }[];
  } | null;
  expect(saved?.segments?.[0]?.text).toBe("Saved before leaving");
});

test("prompts when leaving cannot save and keeps the draft", async ({ page }) => {
  await seedSaveSubtitleFailure(page, "conflict");
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Conflict draft");

  await page.getByRole("button", { name: "Back to project" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Unsaved changes", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/#\/manual-correction\/proj-demo$/);

  // 留在本页：草稿不丢，冲突状态仍可见。
  await dialog.getByRole("button", { name: "Stay on this page" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Conflict draft");
  await expect(page.locator(".mcs-status")).toHaveText(/Content changed/);

  // 放弃：不保存，直接离开。
  await page.getByRole("button", { name: "Back to project" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Discard and leave" }).click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo$/);
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("renders an explicit empty stage without a desktop bridge", async ({ page }) => {
  await openCorrection(page);

  // 浏览器没有 yaomu bridge：不拼接资产 URL、不伪造播放元素。
  const stage = page.locator(".stage");
  await expect(stage).toBeVisible();
  await expect(stage.getByText("Source preview", { exact: true })).toBeVisible();
  await expect(
    stage.getByText("Video preview is available in the desktop app."),
  ).toBeVisible();
  await expect(page.locator(".stage__viewport video, .stage__viewport audio")).toHaveCount(0);
  // 没有成品资产时成品页签禁用，不把源素材伪装成成品。
  await expect(stage.getByRole("button", { name: "Final video" })).toBeDisabled();
});

test("preserves revision style and erase blocks when saving corrections", async ({ page }) => {
  await openCorrection(page);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Edited with style");
  await saveActions(page).first().click();
  await expect(page.locator(".mcs-status")).toHaveText(/Saved/);

  const saved = (await readLastSavedRevision(page)) as {
    style?: { fontSize?: number; align?: string };
    eraseBlocks?: { id?: string; bbox?: number[] }[];
  } | null;
  expect(saved?.style?.fontSize).toBe(44);
  expect(saved?.style?.align).toBe("center");
  expect(saved?.eraseBlocks?.[0]?.id).toBe("eblock-demo-1");
  expect(saved?.eraseBlocks?.[0]?.bbox).toEqual([10, 20, 110, 120]);
});

test("keeps the draft when saving hits a revision conflict", async ({ page }) => {
  await seedSaveSubtitleFailure(page, "conflict");
  await openCorrection(page);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Conflict draft");
  await saveActions(page).first().click();

  await expect(
    page.getByRole("status").filter({
      hasText: "The content changed; reload before saving.",
    }),
  ).toBeVisible();
  // 409 不覆盖草稿、不假同步：文本保留，冲突状态与保存入口仍可用。
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue("Conflict draft");
  await expect(page.locator(".mcs-status")).toHaveText(/Content changed/);
  await expect(saveActions(page).first()).toBeEnabled();
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("disables the single save action when a timestamp draft is invalid", async ({ page }) => {
  await openCorrection(page);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.locator("input").first().fill("not-a-timestamp");
  await expect(firstRow.getByText("Invalid timestamp format")).toBeVisible();

  await expect(saveActions(page)).toHaveCount(1);
  await expect(saveActions(page)).toBeDisabled();
  // 非法草稿没有提交入口，mock 侧也不会收到 revision。
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("keeps the header save available on the style tab", async ({ page }) => {
  await openCorrection(page);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Saved from style tab");
  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(1);

  await page.getByRole("tab", { name: "Subtitle style" }).click();
  await expect(page.locator(".style-panel")).toBeVisible();

  // 切到样式页签后顶栏保存仍常驻，未提交草稿可直接保存。
  await expect(saveActions(page)).toHaveCount(1);
  const save = saveActions(page).first();
  await expect(save).toBeEnabled();
  await save.click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();

  await page.getByRole("tab", { name: "Content correction" }).click();
  await expect(page.getByTestId("segment-row").first().getByTestId("segment-translation")).toHaveValue(
    "Saved from style tab",
  );
});

test("generates, selects and audits inline candidates through the Core API", async ({
  page,
}) => {
  await openCorrection(page);

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Edited translation");
  await saveActions(page).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  // save 后页面会重新读取项目；文本仍在说明 revision 已落库（mock 内存模型）。
  await expect(page.getByTestId("segment-row").first().getByTestId("segment-translation")).toHaveValue(
    "Edited translation",
  );

  // 行内候选：只读回已有候选，生成由显式按钮触发。
  await page.getByTestId("segment-row").first().getByTestId("candidate-toggle").click();
  const inline = page.getByTestId("inline-candidates");
  await expect(inline).toBeVisible();
  await expect(inline.getByText("Dubbing candidates")).toBeVisible();
  // 无可编造参数：不出现参考句/生成数量/恢复自动配音控件。
  await expect(inline.getByText("Reference sentence parameters: pending")).toHaveCount(0);
  await expect(inline.getByText("Use auto dubbing")).toHaveCount(0);
  await expect(inline.locator("select")).toHaveCount(0);

  await inline
    .getByRole("button", { name: /Generate candidates|Regenerate this sentence/ })
    .click();
  const firstSelect = inline.getByRole("button", { name: "Select", exact: true }).first();
  await expect(firstSelect).toBeVisible({ timeout: 10_000 });
  await firstSelect.click();
  await expect(
    page.getByRole("status").filter({ hasText: "Candidate selected" }),
  ).toBeVisible();
  await expect(inline.locator(".cand.is-selected")).toHaveCount(1);

  // 浏览器无桌面桥：试听给出明确提示，不伪造播放状态。
  await inline.getByRole("button", { name: "Audition" }).first().click();
  await expect(
    page.getByRole("status").filter({ hasText: "Audition requires the desktop app" }),
  ).toBeVisible();

  // 折叠后重新展开仍读回同一选择。
  await page.getByTestId("segment-row").first().getByTestId("candidate-toggle").click();
  await expect(inline).toHaveCount(0);
  await page.getByTestId("segment-row").first().getByTestId("candidate-toggle").click();
  await expect(page.getByTestId("inline-candidates").locator(".cand.is-selected")).toHaveCount(
    1,
  );
});

test("filters subtitle cards with the inline search", async ({ page }) => {
  await openCorrection(page);
  const search = page.getByRole("searchbox", { name: "Search subtitles" });

  await search.fill("no-such-subtitle");
  await expect(page.getByText("No subtitles match your search.")).toBeVisible();
  await expect(page.getByTestId("segment-row")).toHaveCount(0);

  await search.fill("");
  await expect(page.getByTestId("segment-row")).toHaveCount(8);
});

test("undoes and redoes local editor changes", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  const textarea = firstRow.getByTestId("segment-translation");
  const original = await textarea.inputValue();

  await textarea.fill("Undo draft");
  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(1);

  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await expect(textarea).toHaveValue(original);
  await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(0);

  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await expect(textarea).toHaveValue("Undo draft");
  // 非输入控件上的 Ctrl/Cmd+Z 走同一本地撤销栈；最终状态由自动保存落库。
  await page.keyboard.press("ControlOrMeta+z");
  await expect(textarea).toHaveValue(original);
  // 回到基准内容后不再 dirty，不需要自动保存。
  await expect(page.locator(".mcs-status")).toHaveText(/saved/i);
  expect(await readLastSavedRevision(page)).toBeNull();
});

test("switches tabs and opens shortcut help from the keyboard", async ({ page }) => {
  await openCorrection(page);

  await page.keyboard.press("2");
  await expect(page.getByRole("tab", { name: "Subtitle style" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.keyboard.press("1");
  await expect(page.getByRole("tab", { name: "Content correction" })).toHaveAttribute(
    "aria-selected",
    "true",
  );

  await page.keyboard.press("?");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Shortcuts");
  await expect(dialog).toContainText("Switch correction tab");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);

  // Ctrl/Cmd+Enter 打开重新合成弹窗；Escape 关闭，不提交任何审核。
  await page.keyboard.press("ControlOrMeta+Enter");
  const remixDialog = page.getByRole("dialog");
  await expect(remixDialog).toContainText("Changed segments");
  await page.keyboard.press("Escape");
  await expect(remixDialog).toHaveCount(0);
  expect(await readApproveCalls(page)).toBe(0);
});

test("saves a custom subtitle style with the revision and keeps erase read-only", async ({
  page,
}) => {
  await openCorrection(page);
  await page.getByRole("tab", { name: "Subtitle style" }).click();

  // demo revision 自带样式：默认显式覆盖，保存时按整轨回传。
  const override = page.getByRole("checkbox", {
    name: "Use a custom style for the current version",
  });
  await expect(override).toBeChecked();
  const fontSize = page.getByLabel("Font size", { exact: true });
  await expect(fontSize).toHaveValue("44");

  await fontSize.fill("48");
  await expect(page.locator(".mcs-status")).toHaveText("Unsaved changes");
  await saveActions(page).first().click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  await expect(override).toBeChecked();
  await expect(fontSize).toHaveValue("48");

  const styled = (await readLastSavedRevision(page)) as {
    id?: string;
    style?: { fontSize?: number };
    eraseBlocks?: { id?: string; bbox?: number[] }[];
  } | null;
  expect(styled?.style?.fontSize).toBe(48);
  // 编辑样式不得丢擦除块。
  expect(styled?.eraseBlocks?.[0]?.id).toBe("eblock-demo-1");

  // 取消显式覆盖：保存省略 style（回退运行样式），擦除块仍原样保留。
  await override.uncheck();
  await expect(page.locator(".mcs-status")).toHaveText("Unsaved changes");
  await saveActions(page).first().click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  const fallback = (await readLastSavedRevision(page)) as {
    style?: unknown;
    eraseBlocks?: { id?: string }[];
  } | null;
  expect(fallback?.style).toBeUndefined();
  expect(fallback?.eraseBlocks?.[0]?.id).toBe("eblock-demo-1");

  await page.getByRole("tab", { name: "Erase correction" }).click();
  const erasePanel = page.locator(".erase-panel");
  await expect(erasePanel).toContainText("Read-only; editing and running are not available yet.");
  // 只读占位没有任何执行/编辑控件，也不派发擦除任务。
  await expect(erasePanel.getByRole("button")).toHaveCount(0);
  await expect(erasePanel.getByRole("checkbox")).toHaveCount(0);
});

test("confirms the translation and remixes, leaving delivery review manual", async ({ page }) => {
  await openCorrection(page);
  await enableRemixAdvance(page);

  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill(
    "Edited translation for remix",
  );
  await page.getByRole("button", { name: "Confirm translation and remix" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Language: English");
  await expect(dialog).toContainText("Changed segments: 1");
  // 只确认当前语种：没有多语批量、双语开关或“模拟合成”提示。
  await expect(dialog.getByRole("checkbox")).toHaveCount(0);
  await expect(dialog.getByText(/simulated/i)).toHaveCount(0);
  await dialog.getByRole("button", { name: "Confirm and remix" }).click();

  // mock 后端在放行译文审核后推进 07/08；08 完成后停在交付审核，不自动放行。
  await expect(
    dialog.getByText("New mix is ready and waiting for delivery review."),
  ).toBeVisible({ timeout: 10_000 });

  const saved = (await readLastSavedRevision(page)) as { id?: string } | null;
  const calls = await readApproveInputs(page);
  expect(calls).toHaveLength(1);
  expect(calls[0]?.type).toBe("translation");
  // 批准必须携带当前绑定守卫：当前语种 + translation_revision + 刚保存的 revision。
  expect(calls[0]?.currentBinding).toEqual({
    language: "en",
    sourceKind: "translation_revision",
    sourceId: saved?.id,
  });
  expect(calls.some((call) => call.type === "delivery")).toBe(false);

  await dialog.getByRole("button", { name: "Go to delivery review" }).click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo\/pipeline$/);
});

test("fails closed when the review service does not report current bindings", async ({
  page,
}) => {
  // 旧 Core 缺省 current_bindings：不得按摘要裸批，也不得发批准请求。
  await seedReviewFixtures(page, [
    {
      id: "legacy-translation",
      runId: "run-demo-1",
      type: "translation",
      language: "en",
      revisionDigest: "legacy-digest",
      status: "pending",
    },
  ]);
  await openCorrection(page);
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Legacy binding");
  await page.getByRole("button", { name: "Confirm translation and remix" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Confirm and remix" }).click();

  await expect(dialog.getByText(/cannot read the review information/)).toBeVisible();
  expect(await readApproveCalls(page)).toBe(0);
});

test("shows a readable paused reason without approving the review", async ({ page }) => {
  await seedReviewScenario(page, { runStatus: "PAUSED" });
  await openCorrection(page);
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Paused remix");
  await page.getByRole("button", { name: "Confirm translation and remix" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Confirm and remix" }).click();

  await expect(
    dialog.getByText("The task is paused. Resume it before remixing."),
  ).toBeVisible();
  expect(await readApproveCalls(page)).toBe(0);
});

test("keeps remix blocked by invalid timestamp drafts", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.locator("input").first().fill("not-a-timestamp");
  await page.getByRole("button", { name: "Confirm translation and remix" }).click();

  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Confirm and remix" }).click();
  await expect(
    dialog.getByText("Fix the highlighted segment errors before remixing."),
  ).toBeVisible();
  expect(await readApproveCalls(page)).toBe(0);
});

test("disables remix for a terminal run with a readable reason", async ({ page }) => {
  await seedReviewScenario(page, { runStatus: "SUCCEEDED" });
  await openCorrection(page);

  const remix = page.getByRole("button", { name: "Confirm translation and remix" });
  await expect(remix).toBeDisabled();
  await expect(remix).toHaveAttribute(
    "title",
    "The task is already finished; remix is unavailable.",
  );
});

test("opens the export panel with the default outputs folder and Core naming tokens", async ({
  page,
}) => {
  await openCorrection(page);
  const dialog = await openExport(page);
  const panel = dialog.getByTestId("mcs-export-panel");

  // 目录由桌面主进程授权，浏览器 mock 展示相对路径；没有手输或浏览入口。
  await expect(panel.getByTestId("mcs-export-dir")).toHaveValue("outputs");
  await expect(panel.getByTestId("mcs-export-dir")).toBeDisabled();
  await expect(panel.getByRole("button", { name: "Browse" })).toHaveCount(0);
  await expect(panel.getByTestId("mcs-export-preview")).toHaveText(
    /^星际译制 · 试播集_en\.srt$/,
  );

  const subtitle = panel.getByRole("checkbox", { name: "Subtitles", exact: true });
  const video = panel.getByRole("checkbox", { name: "Composed video", exact: true });
  const audio = panel.getByRole("checkbox", { name: "Audio track", exact: true });
  await expect(subtitle).toBeChecked();
  await expect(video).not.toBeChecked();
  await expect(audio).not.toBeChecked();

  for (const token of ["{project}", "{language}"]) {
    await expect(panel.getByRole("button", { name: token, exact: true })).toBeVisible();
  }
  await expect(panel.getByRole("button", { name: "{lang}", exact: true })).toHaveCount(0);

  // 只接受 Core 的三个 token；其他占位符保持可读错误。
  const naming = panel.getByTestId("mcs-export-naming");
  await naming.fill("{project}_{lang}");
  await expect(
    panel.getByText("The naming rule may only use {project} and {language}.", {
      exact: true,
    }),
  ).toBeVisible();
  await naming.fill("{project}_{language}");
  await expect(panel.getByTestId("mcs-export-preview")).toHaveText(
    /^星际译制 · 试播集_en\.srt$/,
  );

  // demo 的交付门槛仍待放行：默认字幕被阻断；已选项仍可取消，未准入项不可勾选。
  await expect(panel.getByTestId("mcs-export-blocked")).toHaveText(
    "Some current delivery reviews are still pending.",
  );
  await expect(panel.getByRole("button", { name: "Start export" })).toBeDisabled();
  await expect(subtitle).toBeEnabled();
  await panel.locator("label.ym-checkbox").filter({ hasText: "Subtitles" }).click();
  await expect(panel.getByTestId("mcs-export-blocked")).toHaveText(
    "Select at least one export kind.",
  );
  await expect(video).toBeDisabled();
});

test("exports multiple kinds with per-item status after delivery approval", async ({ page }) => {
  await seedReviewFixtures(page, approvedDeliveryFixtures);
  await openCorrection(page);
  const dialog = await openExport(page);
  const panel = dialog.getByTestId("mcs-export-panel");
  const start = panel.getByRole("button", { name: "Start export" });

  await expect(panel.getByTestId("mcs-export-blocked")).toHaveCount(0);
  await expect(panel.getByRole("checkbox", { name: "Subtitles", exact: true })).toBeChecked();
  await panel.locator("label.ym-checkbox").filter({ hasText: "Composed video" }).click();
  await panel.locator("label.ym-checkbox").filter({ hasText: "Audio track" }).click();
  await panel.getByText("Include original + translation lines", { exact: true }).click();
  await expect(start).toBeEnabled();

  await start.click();
  await expect.poll(async () => readExportCalls(page), { timeout: 10_000 }).toBe(3);

  const inputs = await readExportInputs(page);
  const byKind = Object.fromEntries(inputs.map((item) => [item.kind, item]));
  // 正文：视频/音频绑定当前 mix，不带字幕 revision；字幕冻结当前 revision 且双语只作用于 SRT。
  expect(byKind.video).toMatchObject({
    kind: "video",
    format: "mp4_h264",
    outputDir: "outputs",
    namingTemplate: "{project}_{language}",
    bilingual: false,
  });
  expect(byKind.video?.inputRevisionId).toBeUndefined();
  expect(byKind.subtitle).toMatchObject({
    kind: "subtitle",
    format: "srt",
    inputRevisionId: "rev-demo-en-1",
    bilingual: true,
  });
  expect(byKind.audio).toMatchObject({ kind: "audio", format: "wav", bilingual: false });
  expect(byKind.audio?.inputRevisionId).toBeUndefined();

  for (const kind of ["subtitle", "video", "audio"]) {
    const record = panel.getByTestId(`mcs-export-record-${kind}`);
    await expect(record.getByText("Succeeded")).toBeVisible({ timeout: 5_000 });
    await expect(record.getByText(/outputs\//)).toBeVisible();
  }
});

test("gates subtitle export on the current revision while video only needs the mix", async ({
  page,
}) => {
  // mix 已放行、当前字幕 revision 仍待放行：视频可用，字幕继续阻断。
  await seedReviewFixtures(page, [
    approvedMixDeliveryFixture,
    { ...approvedSubtitleDeliveryFixture, status: "pending" },
  ]);
  await openCorrection(page);
  const dialog = await openExport(page);
  const panel = dialog.getByTestId("mcs-export-panel");
  const start = panel.getByRole("button", { name: "Start export" });
  const subtitle = panel.getByRole("checkbox", { name: "Subtitles", exact: true });
  const video = panel.getByRole("checkbox", { name: "Composed video", exact: true });

  await expect(start).toBeDisabled();
  await expect(subtitle).toBeChecked();
  await expect(panel.getByTestId("mcs-export-blocked")).toHaveText(
    "Some current delivery reviews are still pending.",
  );
  await panel.locator("label.ym-checkbox").filter({ hasText: "Subtitles" }).click();
  await expect(video).toBeEnabled();
  await panel.locator("label.ym-checkbox").filter({ hasText: "Composed video" }).click();
  await expect(panel.getByTestId("mcs-export-blocked")).toHaveCount(0);
  await expect(start).toBeEnabled();
  expect(await readExportCalls(page)).toBe(0);
});

test("blocks export when delivery bindings are unknown", async ({ page }) => {
  // 旧 Core 缺省 current_bindings：不得猜测当前交付门槛，也不得发出导出请求。
  await seedReviewFixtures(page, [
    {
      id: "legacy-delivery",
      runId: "run-demo-1",
      type: "delivery",
      language: "en",
      revisionDigest: "legacy-digest",
      status: "pending",
    },
  ]);
  await openCorrection(page);
  const dialog = await openExport(page);
  const panel = dialog.getByTestId("mcs-export-panel");

  await expect(panel.getByTestId("mcs-export-blocked")).toContainText(
    "cannot read the review information",
  );
  await expect(panel.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readExportCalls(page)).toBe(0);
});

test("does not fabricate a file when an export fails", async ({ page }) => {
  await seedReviewFixtures(page, approvedDeliveryFixtures);
  await seedExportFailure(page);
  await openCorrection(page);
  const dialog = await openExport(page);
  const panel = dialog.getByTestId("mcs-export-panel");

  await expect(panel.getByRole("button", { name: "Start export" })).toBeEnabled();
  await panel.getByRole("button", { name: "Start export" }).click();

  const record = panel.getByTestId("mcs-export-record-subtitle");
  await expect(record.getByText("Failed", { exact: true })).toBeVisible({ timeout: 5_000 });
  // 失败记录没有输出路径；页面不生成下载链接或本地假文件。
  await expect(record.getByText("—")).toBeVisible();
  await expect(dialog.locator("a[download]")).toHaveCount(0);
  await expect(dialog.getByRole("status").filter({ hasText: "Export failed" })).toBeVisible();
});

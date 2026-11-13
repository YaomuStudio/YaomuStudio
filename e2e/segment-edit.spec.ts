import { expect, test, type Page } from "@playwright/test";
import { seedMockControl, seedOnboardedSettings } from "./support";

interface SavedSegmentProbe {
  id: string;
  startMs: number;
  endMs: number;
  text: string;
  contentRevision?: number;
  lineage?: string[];
}

interface SavedRevisionProbe {
  id: string;
  parentRevisionId?: string;
  segments: SavedSegmentProbe[];
  style?: { fontSize?: number; align?: string };
  eraseBlocks?: { id?: string; bbox?: number[] }[];
}

interface ReviewProbe {
  type: string;
  language?: string;
  revisionDigest: string;
  status: string;
}

interface SegmentProbeResult {
  revision: SavedRevisionProbe | null;
  reviews: ReviewProbe[];
}

/** demo 项目已含 zh transcription + en translation 与 06 审核门禁，直接进入校正器。 */
async function openDemoEditor(page: Page): Promise<void> {
  await page.goto("/#/projects/proj-demo/pipeline");
  await expect(
    page.getByRole("heading", { level: 2, name: "Subtitle correction" }),
  ).toBeVisible();
  await expect(page.getByTestId("segment-row")).toHaveCount(8);
}

async function readSavedProbe(page: Page): Promise<SegmentProbeResult> {
  return page.evaluate(() => {
    const control = (
      window as Window & {
        __yaomuMock?: {
          lastSavedRevision?: SavedRevisionProbe;
          lastSavedReviews?: ReviewProbe[];
        };
      }
    ).__yaomuMock;
    return {
      revision: control?.lastSavedRevision ?? null,
      reviews: control?.lastSavedReviews ?? [],
    };
  });
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("merges two translation rows, saves lineage and rehydrates", async ({ page }) => {
  await openDemoEditor(page);
  const rows = page.getByTestId("segment-row");
  const firstRow = rows.nth(0);

  // 原文列按时间重叠匹配，不再按数组下标。
  await expect(firstRow.getByText("欢迎来到YaomuStudio。")).toBeVisible();
  await firstRow.getByRole("button", { name: "Merge next" }).click();

  await expect(rows).toHaveCount(7);
  const mergedRow = rows.nth(0);
  // 译文在 textarea 中，用 value 断言（getByText 不匹配表单值）。
  await expect(mergedRow.locator("textarea")).toHaveValue(
    "Welcome to YaoMu. This is a local dubbing demo.",
  );
  await expect(
    mergedRow.getByText("欢迎来到YaomuStudio。 这是一段本地译制的演示。"),
  ).toBeVisible();

  await page.getByRole("button", { name: "Save corrections" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toBeVisible();
  // 保存后重新 hydrate：新句段使用 Core 分配的 ID，行数保持合并后的结果。
  await expect(rows).toHaveCount(7);
  await expect(mergedRow.locator("textarea")).toHaveValue(
    "Welcome to YaoMu. This is a local dubbing demo.",
  );

  const { revision, reviews } = await readSavedProbe(page);
  expect(revision).not.toBeNull();
  expect(revision?.parentRevisionId).toBeTruthy();
  expect(revision?.segments).toHaveLength(7);
  const merged = revision?.segments[0];
  expect(merged?.id).toMatch(/^seg-/);
  expect(merged?.id).not.toBe("seg-101");
  expect(merged?.startMs).toBe(1000);
  expect(merged?.endMs).toBe(7200);
  expect(merged?.text).toBe("Welcome to YaoMu. This is a local dubbing demo.");
  expect(merged?.lineage).toEqual(["seg-101", "seg-102"]);
  // 未参与合并的已有句段内容未变：content_revision 保持 1（与 Core 同口径）。
  expect(
    revision?.segments.find((segment) => segment.id === "seg-103")?.contentRevision,
  ).toBe(1);
  // 整轨替换语义：改字幕不能丢 revision 已有的样式/擦除块。
  expect(revision?.style?.fontSize).toBe(44);
  expect(revision?.style?.align).toBe("center");
  expect(revision?.eraseBlocks?.[0]?.id).toBe("eblock-demo-1");
  expect(revision?.eraseBlocks?.[0]?.bbox).toEqual([10, 20, 110, 120]);

  // 保存后旧审核门槛不再匹配：出现绑定新摘要的 pending translation/delivery。
  expect(
    reviews.some(
      (gate) =>
        gate.type === "translation" &&
        gate.language === "en" &&
        gate.status === "pending" &&
        gate.revisionDigest !== "zh-en-r2",
    ),
  ).toBe(true);
  expect(
    reviews.some((gate) => gate.type === "delivery" && gate.status === "pending"),
  ).toBe(true);
});

test("splits a translation row at the nearest whitespace and saves both halves", async ({
  page,
}) => {
  await openDemoEditor(page);
  const rows = page.getByTestId("segment-row");
  const secondRow = rows.nth(1);

  await secondRow.getByRole("button", { name: "Split" }).click();
  await expect(rows).toHaveCount(9);

  await page.getByRole("button", { name: "Save corrections" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toBeVisible();
  await expect(rows).toHaveCount(9);

  const { revision } = await readSavedProbe(page);
  const halves = (revision?.segments ?? []).filter((segment) =>
    segment.lineage?.includes("seg-102"),
  );
  expect(halves).toHaveLength(2);
  expect(halves[0].startMs).toBe(3500);
  expect(halves[0].endMs).toBe(5350);
  expect(halves[0].text).toBe("This is a local");
  expect(halves[1].startMs).toBe(5350);
  expect(halves[1].endMs).toBe(7200);
  expect(halves[1].text).toBe("dubbing demo.");
  expect(halves[0].id).not.toBe(halves[1].id);
  expect(halves[0].id).toMatch(/^seg-/);
});

test("disables merge on the last row and split on a window shorter than 2 ms", async ({
  page,
}) => {
  await openDemoEditor(page);
  const rows = page.getByTestId("segment-row");

  const lastRow = rows.nth(7);
  const lastMerge = lastRow.getByRole("button", { name: "Merge next" });
  await expect(lastMerge).toBeDisabled();
  await expect(lastMerge).toHaveAttribute(
    "title",
    "The last sentence has no next sentence to merge",
  );

  const firstRow = rows.nth(0);
  const inputs = firstRow.locator("input");
  await inputs.nth(0).fill("00:00:01,000");
  await inputs.nth(1).fill("00:00:01,001");

  const split = firstRow.getByRole("button", { name: "Split" });
  await expect(split).toBeDisabled();
  await expect(split).toHaveAttribute(
    "title",
    "This sentence is shorter than 2 ms and cannot be split",
  );
});

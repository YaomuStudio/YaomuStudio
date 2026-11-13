import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_AUDIO,
  SAMPLE_MEDIA,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

// 各新建页统一使用 CreationNotice：同一位置、同一配色、同一标题，且不包含计费/积分文案。
// Extract 的音频提示只在音频源时出现；CustomPipeline 只在选中擦除/克隆时出现。

async function expectNotice(page: Page): Promise<void> {
  const notice = page.locator(".creation-notice");
  await expect(notice).toHaveCount(1);
  await expect(notice).toBeVisible();
  await expect(notice.getByRole("listitem").first()).toBeVisible();
  await expect(notice).not.toContainText(/积分|计费|points|billing/i);
}

async function noticeStyle(page: Page): Promise<string> {
  return page.locator(".creation-notice").evaluate((element) => {
    const style = getComputedStyle(element);
    return [
      style.backgroundColor,
      style.color,
      style.borderRadius,
      style.paddingTop,
      style.paddingRight,
      style.paddingBottom,
      style.paddingLeft,
    ].join("|");
  });
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("creation notices share one visual treatment and contain no billing copy", async ({ page }) => {
  // 强制项目列表为空，使 /#/pipeline 显示无项目引导空态（默认夹具数据始终有项目）。
  await page.addInitScript(() => {
    const scope = window as unknown as { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.projectListOverride = [];
  });

  const styles: string[] = [];

  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();
  await expectNotice(page);
  styles.push(await noticeStyle(page));

  await page.goto("/#/tools/erase");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle erase" })).toBeVisible();
  await expectNotice(page);
  await expect(page.locator(".creation-notice")).toContainText(
    "Large subtitle areas can reduce repair quality.",
  );
  styles.push(await noticeStyle(page));

  await page.goto("/#/tools/translate");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle translation" })).toBeVisible();
  await expectNotice(page);
  styles.push(await noticeStyle(page));

  await page.goto("/#/voice-clone");
  await expect(
    page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" }),
  ).toBeVisible();
  await expectNotice(page);
  styles.push(await noticeStyle(page));

  await page.goto("/#/pipeline/new");
  await expect(page.getByRole("heading", { level: 1, name: "New full pipeline" })).toBeVisible();
  await expectNotice(page);
  await expect(page.locator(".creation-notice").getByRole("listitem")).toHaveCount(4);
  styles.push(await noticeStyle(page));

  await page.goto("/#/tools/custom");
  await expect(page.getByRole("heading", { level: 1, name: "Custom pipeline" })).toBeVisible();
  await expect(page.locator(".creation-notice")).toHaveCount(0);
  await page.locator("label.ym-checkbox").filter({ hasText: "Subtitle erase" }).click();
  await expectNotice(page);
  styles.push(await noticeStyle(page));

  expect(new Set(styles).size).toBe(1);
});

test("extract always shows output guidance and the audio note only for audio sources", async ({ page }) => {
  await page.goto("/#/tools/extract");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle extraction" })).toBeVisible();

  // 输出说明常显；音频误用提示只在音频源时追加。
  const notice = page.locator(".creation-notice");
  await expect(notice).toHaveCount(1);
  await expect(notice).toContainText("Outputs an SRT with timestamps.");
  await expect(notice).not.toContainText("Audio sources support ASR extraction only.");

  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await expect(page.getByText(SAMPLE_MEDIA.name)).toBeVisible();
  await expect(notice).toHaveCount(1);
  await expect(notice).not.toContainText("Audio sources support ASR extraction only.");

  await page.getByRole("button", { name: "Remove" }).click();
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_AUDIO);
  await expect(page.getByText(SAMPLE_AUDIO.name)).toBeVisible();

  await expect(notice).toHaveCount(1);
  await expect(notice).toContainText("Outputs an SRT with timestamps.");
  await expect(notice).toContainText("Audio sources support ASR extraction only.");
});

test("voiceClone shows the source warning only once", async ({ page }) => {
  await page.goto("/#/voice-clone");
  await expect(
    page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" }),
  ).toBeVisible();

  const warning =
    "Background music or noise mixed into the source will affect clone quality.";
  const notice = page.locator(".creation-notice");
  await expect(notice).toHaveCount(1);
  await expect(notice).toContainText(warning);
  await expect(page.getByText(warning)).toHaveCount(1);
});

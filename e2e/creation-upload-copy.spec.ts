import { expect, test } from "@playwright/test";
import { seedMockControl, seedOnboardedSettings } from "./support";

// 上传框内说明统一为完整句子：标题不加句号，说明文字以句号收尾。
test("creation upload boxes use consistent punctuation", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);

  const pages = [
    { path: "/#/tools/separate", heading: "Audio separation" },
    { path: "/#/tools/extract", heading: "Subtitle extraction" },
    { path: "/#/tools/erase", heading: "Subtitle erase" },
    { path: "/#/tools/translate", heading: "Subtitle translation" },
    { path: "/#/voice-clone", heading: "High-emotion clone dubbing" },
    { path: "/#/pipeline/new", heading: "New full pipeline" },
    { path: "/#/tools/custom", heading: "Custom pipeline" },
  ];

  for (const item of pages) {
    await page.goto(item.path);
    await expect(page.getByRole("heading", { level: 1, name: item.heading })).toBeVisible();

    const dropzones = page.locator(".ym-dropzone");
    expect(await dropzones.count(), `${item.path} should show an upload box`).toBeGreaterThan(0);

    for (let index = 0; index < (await dropzones.count()); index += 1) {
      const dropzone = dropzones.nth(index);
      const title = (await dropzone.locator(".ym-dropzone__title").innerText()).trim();
      expect(title, `${item.path} upload title must not end with punctuation`).not.toMatch(/[。.]$/);

      const hint = dropzone.locator(".ym-dropzone__hint");
      if (await hint.count()) {
        const text = (await hint.innerText()).trim();
        expect(text, `${item.path} upload hint must end with a full stop`).toMatch(/[。.]$/);
      }
    }
  }
});

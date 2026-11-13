import { expect, test, type Page } from "@playwright/test";
import { seedCapabilityStates, seedOnboardedSettings } from "./support";

const ALL_SLOTS = [
  "asr",
  "translate",
  "tts",
  "voice-clone",
  "subtitle-erase",
  "separation",
  "ocr",
  "ffmpeg",
] as const;

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

function nav(page: Page) {
  return page.getByRole("navigation", { name: "primary" });
}

test("hides an unadmitted tool and redirects its deep link to the workbench", async ({ page }) => {
  await seedCapabilityStates(page, { "subtitle-erase": "UNSUPPORTED" });
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();

  await expect(nav(page).getByRole("link", { name: "Subtitle Erase" })).toHaveCount(0);

  await page.goto("/#/tools/erase");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  await expect(page).toHaveURL(/#\/$/);
});

test("hides the full pipeline unless every required slot is admitted", async ({ page }) => {
  await seedCapabilityStates(page, { separation: "UNSUPPORTED" });
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();

  await expect(nav(page).getByRole("link", { name: "Full Pipeline" })).toHaveCount(0);
  // 专属链路仍可用：它只要求至少一个功能路径已准入。
  await expect(nav(page).getByRole("link", { name: "Custom Pipeline" })).toBeVisible();

  await page.goto("/#/pipeline");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
});

test("keeps a setup entry visible with an indicator and a readable notice", async ({ page }) => {
  await seedCapabilityStates(page, { "subtitle-erase": "NEEDS_SETUP" });
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();

  const link = nav(page).getByRole("link", { name: "Subtitle Erase" });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("data-access", "setup");

  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle erase" })).toBeVisible();
  await expect(page.getByText(/some required engines are not ready/)).toBeVisible();
});

test("shows admitted entries normally when every slot is ready", async ({ page }) => {
  await page.goto("/#/");
  const link = nav(page).getByRole("link", { name: "Subtitle Erase" });
  await expect(link).toHaveAttribute("data-access", "ready");

  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle erase" })).toBeVisible();
  await expect(page.getByText(/some required engines are not ready/)).toHaveCount(0);
});

test("voice clone stays available without the speech synthesis slot", async ({ page }) => {
  // 克隆模型自身包含合成，TTS 槽未准入不应隐藏克隆入口或提示它未就绪。
  await seedCapabilityStates(page, { tts: "UNSUPPORTED" });
  await page.goto("/#/");
  const link = nav(page).getByRole("link", { name: "Voice Clone" });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("data-access", "ready");
  // 全流程同样包含 07 克隆节点，也不应被 TTS 槽阻断。
  await expect(nav(page).getByRole("link", { name: "Full Pipeline" })).toHaveAttribute(
    "data-access",
    "ready",
  );

  await link.click();
  await expect(
    page.getByRole("heading", { level: 1, name: "High-emotion clone dubbing" }),
  ).toBeVisible();
  await expect(page.getByText(/Speech synthesis/)).toHaveCount(0);

  // 专属链路里的克隆功能同样只依赖 voice-clone。
  await page.goto("/#/tools/custom");
  await expect(page.getByRole("checkbox", { name: "High-emotion cloned dubbing" })).toBeEnabled();
});

test("treats degraded slots as usable", async ({ page }) => {
  await seedCapabilityStates(page, { "subtitle-erase": "DEGRADED" });
  await page.goto("/#/");
  const link = nav(page).getByRole("link", { name: "Subtitle Erase" });
  await expect(link).toHaveAttribute("data-access", "ready");

  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle erase" })).toBeVisible();
  await expect(page.getByText(/some required engines are not ready/)).toHaveCount(0);
});

test("filters extraction modes by admitted ASR and OCR slots", async ({ page }) => {
  await seedCapabilityStates(page, { ocr: "UNSUPPORTED" });
  await page.goto("/#/tools/extract");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle extraction" })).toBeVisible();

  // 三种方式始终展示；未准入的 OCR/联合模式禁用，不从界面移除。
  const radios = page.locator("label.ym-radio__item");
  await expect(radios).toHaveCount(3);
  await expect(radios.nth(0)).toContainText("ASR speech recognition");
  await expect(radios.nth(0)).not.toHaveClass(/ym-radio__item--disabled/);
  await expect(radios.nth(1)).toContainText("OCR on-screen text");
  await expect(radios.nth(1)).toHaveClass(/ym-radio__item--disabled/);
  await expect(radios.nth(2)).toContainText("ASR + OCR smart correction");
  await expect(radios.nth(2)).toHaveClass(/ym-radio__item--disabled/);
});

test("defaults extraction to OCR when ASR is not admitted", async ({ page }) => {
  await seedCapabilityStates(page, { asr: "UNSUPPORTED" });
  await page.goto("/#/tools/extract");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle extraction" })).toBeVisible();

  const radios = page.locator("label.ym-radio__item");
  await expect(radios).toHaveCount(3);
  await expect(radios.nth(0)).toHaveClass(/ym-radio__item--disabled/);
  await expect(radios.nth(1)).toContainText("OCR on-screen text");
  await expect(radios.nth(1)).not.toHaveClass(/ym-radio__item--disabled/);
  await expect(radios.nth(2)).toHaveClass(/ym-radio__item--disabled/);
  await expect(page.getByText("Submit code: chinese")).toBeVisible();
});

test("disables unadmitted custom functions and keeps setup functions selectable", async ({
  page,
}) => {
  await seedCapabilityStates(page, {
    separation: "UNSUPPORTED",
    translate: "NEEDS_SETUP",
  });
  await page.goto("/#/tools/custom");
  await expect(page.getByRole("heading", { level: 1, name: "Custom pipeline" })).toBeVisible();

  const separation = page.getByRole("checkbox", { name: "Audio separation" });
  await expect(separation).toBeDisabled();
  await expect(page.getByRole("checkbox", { name: "Subtitle translation" })).toBeEnabled();
  await expect(page.getByText("Not available in this release")).toBeVisible();
  // 已准入但未就绪的功能保留可勾选，并提示未就绪槽位。
  await expect(page.getByText(/Not ready: Subtitle translation/)).toBeVisible();
});

test("hides the custom pipeline entry when no function can run", async ({ page }) => {
  // 只保留 ASR：任何专属链路功能都缺少 ffmpeg/翻译/克隆等必需槽位。
  await seedCapabilityStates(page, {
    ffmpeg: "UNSUPPORTED",
    separation: "UNSUPPORTED",
    ocr: "UNSUPPORTED",
    "subtitle-erase": "UNSUPPORTED",
    translate: "UNSUPPORTED",
    "voice-clone": "UNSUPPORTED",
    tts: "UNSUPPORTED",
  });
  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();

  await expect(nav(page).getByRole("link", { name: "Custom Pipeline" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Custom Pipeline" })).toHaveCount(0);

  await page.goto("/#/tools/custom");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
});

test("keeps projects and settings accessible when everything is unsupported", async ({ page }) => {
  await seedCapabilityStates(
    page,
    Object.fromEntries(ALL_SLOTS.map((slot) => [slot, "UNSUPPORTED"])),
  );

  await page.goto("/#/");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  await expect(nav(page).getByRole("link", { name: "Full Pipeline" })).toHaveCount(0);
  await expect(nav(page).getByRole("link", { name: "Subtitle Extraction" })).toHaveCount(0);
  // 工作台快捷入口同样隐藏，避免给出会绕回工作台的死入口。
  await expect(page.getByRole("heading", { level: 2, name: "Start something" })).toHaveCount(0);
  await expect(page.locator(".quick-card")).toHaveCount(0);

  await page.goto("/#/projects/proj-demo");
  await expect(page.getByRole("heading", { level: 1, name: /星际译制/ })).toBeVisible();

  await page.goto("/#/tasks");
  await expect(page).toHaveURL(/#\/projects$/);
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();
  await expect(
    nav(page).getByRole("link", { name: "Tasks" }),
  ).toHaveCount(0);

  await page.goto("/#/settings/general");
  await expect(page.getByRole("heading", { level: 1, name: "General" })).toBeVisible();
});

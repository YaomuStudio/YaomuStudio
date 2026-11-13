import { expect, test, type Locator, type Page } from "@playwright/test";
import {
  createProject,
  seedCapabilityFixtures,
  seedCapabilityStates,
  seedMockControl,
  seedOnboardedSettings,
} from "./support";

/** 首屏第一行的卡片：排序受最近打开时间影响，不能假设某张固定卡片在第一行。 */
async function firstRowCards(page: Page): Promise<Locator[]> {
  const cards = page.locator("article.recent-card");
  await expect(cards.first()).toBeVisible();
  const count = await cards.count();
  const boxes: { index: number; top: number }[] = [];
  for (let index = 0; index < count; index += 1) {
    const box = await cards.nth(index).boundingBox();
    if (box) boxes.push({ index, top: box.y });
  }
  const firstRowTop = Math.min(...boxes.map((box) => box.top));
  return boxes
    .filter((box) => box.top - firstRowTop < 2)
    .map((box) => cards.nth(box.index));
}

async function scrollAreaBottom(page: Page): Promise<number> {
  const main = await page.locator("main.app-shell__main").boundingBox();
  expect(main, "the app shell scroll container must have a layout box").not.toBeNull();
  return main!.y + main!.height;
}

function modelCard(page: Page, heading: string): Locator {
  return page
    .locator("article.slot")
    .filter({ has: page.getByRole("heading", { level: 3, name: heading }) });
}

function occurrenceCount(text: string, needle: string): number {
  return text.split(needle).length - 1;
}

function seedThemeAndScale(page: Page, theme: string, uiScale: number): Promise<void> {
  return page.addInitScript(
    ([nextTheme, nextScale]) => {
      localStorage.setItem(
        "yaomu.settings",
        JSON.stringify({ onboarded: true, locale: "en", theme: nextTheme, uiScale: nextScale }),
      );
    },
    [theme, uiScale] as const,
  );
}

test("first-row project card actions stay above the status bar at 1280x800", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/#/projects");

  const mainBottom = await scrollAreaBottom(page);
  const cards = await firstRowCards(page);
  expect(cards.length).toBeGreaterThan(0);
  for (const card of cards) {
    const download = card.getByRole("button", { name: "Download", exact: true });
    await expect(download).toBeVisible();
    const foot = card.locator(".recent-card__foot");
    await expect(foot).toBeVisible();
    const footOverflow = await foot.evaluate(
      (element) => element.scrollWidth - element.clientWidth,
    );
    expect(footOverflow, "all card actions must stay inside the card").toBeLessThanOrEqual(1);
    const cardOverflow = await card.evaluate(
      (element) => element.scrollWidth - element.clientWidth,
    );
    expect(cardOverflow, "card must not overflow horizontally").toBeLessThanOrEqual(1);
    const box = await foot.boundingBox();
    expect(box, "card action footer must have a layout box").not.toBeNull();
    expect(
      box!.y + box!.height,
      `action footer bottom (${box!.y + box!.height}) must stay above the scroll area bottom (${mainBottom})`,
    ).toBeLessThanOrEqual(mainBottom);
  }
});

test("card actions remain reachable without clipping at 1024x700", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.setViewportSize({ width: 1024, height: 700 });
  await page.goto("/#/projects");

  const mainBottom = await scrollAreaBottom(page);
  const cards = await firstRowCards(page);
  expect(cards.length).toBeGreaterThan(0);
  for (const card of cards) {
    const download = card.getByRole("button", { name: "Download", exact: true });
    await expect(download).toBeVisible();
    const foot = card.locator(".recent-card__foot");
    await foot.scrollIntoViewIfNeeded();
    const footOverflow = await foot.evaluate(
      (element) => element.scrollWidth - element.clientWidth,
    );
    expect(footOverflow, "all card actions must stay inside the card").toBeLessThanOrEqual(1);
    const cardOverflow = await card.evaluate(
      (element) => element.scrollWidth - element.clientWidth,
    );
    expect(cardOverflow, "card must not overflow horizontally").toBeLessThanOrEqual(1);
    const box = await foot.boundingBox();
    expect(box, "card action footer must have a layout box").not.toBeNull();
    // 测试夹具模式没有额外产品提示条；小视口仍按实际 main 边界检查，滚动定位与卡片布局存在
    // 亚像素舍入（0.0625px），与同文件水平溢出的 1px 容差一致，不隐藏提示条。
    expect(box!.y + box!.height).toBeLessThanOrEqual(mainBottom + 1);
  }
});

test("setup notice offers a keyboard-reachable models entry without installing", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await seedCapabilityStates(page, { ffmpeg: "NEEDS_SETUP" });
  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();

  const notice = page
    .getByRole("status")
    .filter({ hasText: "some required engines are not ready" });
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("Not ready: FFmpeg");
  // FFmpeg 来自运行环境而非模型包；不承诺模型下载能修复它。
  await expect(notice).toContainText("FFmpeg is provided by the runtime environment");
  await expect(notice).toContainText("downloading a model cannot repair it");

  const entry = notice.getByRole("button", { name: "View models and environment" });
  await expect(entry).toBeVisible();
  await entry.focus();
  await expect(entry).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/#\/settings\/models\?slots=ffmpeg$/);
  await expect(page.getByRole("heading", { level: 1, name: "Models & capabilities" })).toBeVisible();

  // 显式上下文只突出相关槽位；纯导航不请求安装计划，也不出现下载进度。
  const highlighted = page.locator("article.slot--needed");
  await expect(highlighted).toHaveCount(1);
  await expect(highlighted.getByRole("heading", { level: 3, name: "FFmpeg" })).toBeVisible();
  await expect(highlighted).toContainText("Needed by the feature you came from.");
  await expect(page.getByText("Download progress")).toHaveCount(0);
  expect(
    await page.evaluate(
      () => (window as Window & { __yaomuMock?: { lastPlanSlot?: string } }).__yaomuMock?.lastPlanSlot,
    ),
  ).toBeUndefined();
});

test("an unsupported model slot is shown as not configured without install actions", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { asr: "UNSUPPORTED" });
  await page.goto("/#/settings/models");

  const card = modelCard(page, "Speech recognition");
  await expect(card.getByText("Not configured", { exact: true })).toBeVisible();
  await expect(
    card.getByText("This capability is not provided in the current version."),
  ).toBeVisible();
  await expect(card.getByRole("button", { name: "Download model" })).toHaveCount(0);
});

test("a ready entry shows no setup notice and no recovery action", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Audio separation" })).toBeVisible();
  await expect(page.getByText("some required engines are not ready")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "View models and environment" })).toHaveCount(0);
});

test("an unverified slot remains hidden by the existing admission guard", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { ffmpeg: "UNVERIFIED" });
  await page.goto("/#/tools/separate");
  await expect(page.getByRole("heading", { level: 1, name: "Workbench" })).toBeVisible();
  await expect(page).toHaveURL(/#\/$/);
  await expect(page.getByRole("button", { name: "View models and environment" })).toHaveCount(0);
});

test("models page shows known engine facts once and aggregates missing details", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedCapabilityFixtures(page, {
    "voice-clone": { engineName: "", version: "", languages: [], engines: [] },
  });
  await page.goto("/#/settings/models");
  await expect(page.getByRole("heading", { level: 1, name: "Models & capabilities" })).toBeVisible();

  // 已知字段只显示一次，不再在标题与 meta 中重复。
  const asr = modelCard(page, "Speech recognition");
  await expect(asr.locator(".slot__engine")).toHaveCount(0);
  const asrText = await asr.innerText();
  expect(occurrenceCount(asrText, "flash-asr")).toBe(1);
  expect(occurrenceCount(asrText, "v1.2.0")).toBe(1);
  await expect(asr.getByText("Supported languages")).toHaveCount(0);
  await expect(asr.getByText("Not reported")).toHaveCount(0);

  // 未提供的字段聚合成一条说明，不出现假数字或逐字段占位。
  const clone = modelCard(page, "Voice clone");
  const cloneText = await clone.innerText();
  expect(occurrenceCount(cloneText, "Some version or resource details are not available yet.")).toBe(1);
  expect(occurrenceCount(cloneText, "Not reported")).toBe(0);
  expect(occurrenceCount(cloneText, "Not declared by the bundle.")).toBe(0);
  expect(occurrenceCount(cloneText, "This engine exposes no adjustable parameters.")).toBe(0);
  expect(cloneText).not.toContain("—");
  // 语音合成与语音克隆共包，不再单独出现。
  await expect(modelCard(page, "Speech synthesis")).toHaveCount(0);
  // 已知状态与既有动作保持。
  await expect(clone.locator(".slot__head").getByText("Ready", { exact: true })).toBeVisible();
  await expect(clone.getByRole("button", { name: "Delete package" })).toBeVisible();
  await expect(clone.getByRole("button", { name: "Roll back" })).toBeVisible();

  // 没有入口上下文时不标“推荐”槽位。
  await expect(page.locator("article.slot--needed")).toHaveCount(0);
});

test("a long project name keeps card actions reachable without overlap", async ({ page }) => {
  await seedOnboardedSettings(page);
  const name =
    "第十四季第三集超长标题用于验证卡片换行与操作区ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
  await createProject(page, name);
  await page
    .getByRole("navigation", { name: "primary" })
    .getByRole("link", { name: "Projects" })
    .click();
  await expect(page).toHaveURL(/#\/projects$/);

  const card = page.locator("article.recent-card").filter({ hasText: name });
  await expect(card).toBeVisible();

  for (const viewport of [
    { width: 1280, height: 800 },
    { width: 1024, height: 700 },
  ]) {
    await page.setViewportSize(viewport);
    await expect(card).toBeVisible();
    const mainBottom = await scrollAreaBottom(page);
    // 无产物的项目不显示下载占位；以始终存在的编辑操作验证操作区可达。
    const action = card.getByRole("button", { name: "Edit", exact: true });
    await action.scrollIntoViewIfNeeded();
    const box = await action.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.y + box!.height).toBeLessThanOrEqual(mainBottom);

    const cardOverflow = await card.evaluate(
      (element) => element.scrollWidth - element.clientWidth,
    );
    expect(cardOverflow, "card must not overflow horizontally").toBeLessThanOrEqual(1);

    const nameBox = await card.locator(".recent-card__name").boundingBox();
    const footBox = await card.locator(".recent-card__foot").boundingBox();
    expect(nameBox).not.toBeNull();
    expect(footBox).not.toBeNull();
    expect(footBox!.y).toBeGreaterThanOrEqual(nameBox!.y + nameBox!.height - 1);
  }
});

test("dark theme at 150% keeps card content clipped to its container", async ({ page }) => {
  await seedThemeAndScale(page, "dark", 150);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/#/projects");
  await expect(page.getByRole("heading", { level: 1, name: "Projects" })).toBeVisible();

  const state = await page.evaluate(() => ({
    theme: document.documentElement.getAttribute("data-theme"),
    zoom: document.documentElement.style.zoom,
    mainOverflow:
      document.querySelector("main.app-shell__main")!.scrollWidth -
      document.querySelector("main.app-shell__main")!.clientWidth,
  }));
  expect(state.theme).toBe("dark");
  expect(state.zoom).toBe("1.5");
  expect(state.mainOverflow, "main scroll area must not overflow horizontally").toBeLessThanOrEqual(1);

  const cards = page.locator("article.recent-card");
  const count = await cards.count();
  expect(count).toBeGreaterThan(0);
  for (let index = 0; index < count; index += 1) {
    const card = cards.nth(index);
    const overflow = await card.evaluate((element) => element.scrollWidth - element.clientWidth);
    expect(overflow, `card ${index} must not overflow horizontally`).toBeLessThanOrEqual(1);
    const footOverflow = await card
      .locator(".recent-card__foot")
      .evaluate((element) => element.scrollWidth - element.clientWidth);
    expect(footOverflow, `card ${index} actions must not be clipped`).toBeLessThanOrEqual(1);
  }

  // 缩放后操作区可滚动到达，且不被状态栏覆盖。
  const demo = page.locator("article.recent-card").filter({ hasText: "星际译制 · 试播集" });
  const download = demo.getByRole("button", { name: "Download", exact: true });
  await download.scrollIntoViewIfNeeded();
  await expect(download).toBeInViewport();
  const mainBottom = await scrollAreaBottom(page);
  const box = await download.boundingBox();
  expect(box!.y + box!.height).toBeLessThanOrEqual(mainBottom);
});

test("media sliders share a larger operable thumb", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByTestId("segment-row").first()).toBeVisible();
  // 全局 range 规则同时作用于舞台时间轴与擦除滑杆；只断言 CSS 事实，
  // 不依赖某个页面是否恰好渲染滑块。
  const thumb = await page.evaluate(() => {
    for (const sheet of document.styleSheets) {
      let rules: CSSRuleList;
      try {
        rules = sheet.cssRules;
      } catch {
        continue;
      }
      for (const rule of Array.from(rules)) {
        const selector = (rule as CSSStyleRule).selectorText ?? "";
        if (selector.includes('input[type="range"]') && selector.includes("thumb")) {
          return {
            width: (rule as CSSStyleRule).style.width,
            marginTop: (rule as CSSStyleRule).style.marginTop,
          };
        }
      }
    }
    return null;
  });
  expect(thumb?.width).toBe("18px");
  // 固定负 margin 会把圆点顶离轨道中心，并在 80%–150% UI 缩放下放大偏移。
  expect(thumb?.marginTop === "" || thumb?.marginTop === "0px").toBe(true);
});

test("sidebar setup dot stays vertically centered across UI scale", async ({ page }) => {
  await seedThemeAndScale(page, "light", 100);
  await seedCapabilityStates(page, { asr: "NEEDS_SETUP" });
  await page.goto("/#/");

  const badge = page.locator(".sidebar__item-badge").first();
  await expect(badge).toBeVisible();
  const item = badge.locator("xpath=..");

  for (const zoom of ["0.8", "1", "1.25"]) {
    await page.evaluate((value) => {
      document.documentElement.style.zoom = value;
    }, zoom);
    const badgeBox = await badge.boundingBox();
    const itemBox = await item.boundingBox();
    expect(badgeBox).not.toBeNull();
    expect(itemBox).not.toBeNull();
    const badgeCenter = badgeBox!.y + badgeBox!.height / 2;
    const itemCenter = itemBox!.y + itemBox!.height / 2;
    expect(Math.abs(badgeCenter - itemCenter)).toBeLessThanOrEqual(1);
  }
});

test("keeps run-level configuration out of model capability cards", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/models");
  const erase = page.locator("article.slot").filter({ hasText: "Subtitle erase" });
  await expect(erase).toBeVisible();
  await expect(erase.getByText("Erase mode", { exact: false })).toHaveCount(0);
  await expect(erase.getByText("Save parameters", { exact: false })).toHaveCount(0);
});

test("selects an installed local engine for a capability", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/models");
  const asr = modelCard(page, "Speech recognition");
  await expect(asr.getByRole("radio")).toHaveCount(2);
  await asr.getByRole("radio", { name: /Faster-Whisper large-v3/ }).check({ force: true });
  await expect(asr.getByRole("radio", { name: /Faster-Whisper large-v3/ })).toBeChecked();
  await expect(asr.locator(".engine-option.is-selected")).toContainText("Faster-Whisper large-v3");
});

test("recommended tool badges keep the refined pill treatment", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/");
  const badge = page.locator(".recommend-badge--hot").first();
  await expect(badge).toBeVisible();
  const style = await badge.evaluate((element) => {
    const pill = getComputedStyle(element);
    const dot = getComputedStyle(element, "::before");
    return {
      height: pill.height,
      borderWidth: pill.borderTopWidth,
      radius: pill.borderRadius,
      background: pill.backgroundColor,
      dotWidth: dot.width,
      dotHeight: dot.height,
      dotRadius: dot.borderRadius,
    };
  });
  expect(style.height).toBe("22px");
  expect(style.borderWidth).toBe("1px");
  expect(style.radius).toBe("999px");
  expect(style.background).not.toBe("rgba(0, 0, 0, 0)");
  expect(style.dotWidth).toBe("4px");
  expect(style.dotHeight).toBe("4px");
  expect(style.dotRadius).toBe("50%");
});

test("composite search fields draw a single focus ring", async ({ page }) => {
  await seedOnboardedSettings(page);

  // 项目筛选：焦点环只由外层容器绘制，内层输入不再叠加同心边框。
  await page.goto("/#/projects");
  const search = page.getByTestId("project-search-input");
  await search.focus();
  await expect(search).toBeFocused();
  const searchRing = await search.evaluate((element) => ({
    input: getComputedStyle(element).boxShadow,
    wrapper: getComputedStyle(element.closest(".project-search")!).boxShadow,
  }));
  expect(searchRing.input).toBe("none");
  expect(searchRing.wrapper).not.toBe("none");

  // 清除按钮同样只保留外层焦点环。
  await search.fill("demo");
  const clear = page.getByTestId("project-search-clear");
  await clear.focus();
  await expect(clear).toBeFocused();
  expect(await clear.evaluate((element) => getComputedStyle(element).boxShadow)).toBe("none");

  // 手动校对的字幕搜索：内层输入不叠加焦点环，焦点由外层边框表达。
  await page.goto("/#/projects/proj-demo/correction");
  const subSearch = page.locator(".subpanel__search input").first();
  await subSearch.focus();
  await expect(subSearch).toBeFocused();
  expect(await subSearch.evaluate((element) => getComputedStyle(element).boxShadow)).toBe("none");
});

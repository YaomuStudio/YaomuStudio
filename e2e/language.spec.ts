import { expect, test, type Page } from "@playwright/test";
import { seedOnboardedSettings } from "./support";

// 覆盖 26 语言可选、24 个语言包按实际键数计算完成度、缺包/缺键回退英文与 RTL 方向。
// 仅 zh-CN/en 完整；24 个包是高覆盖子集（约 90%，zh-TW 约 96%），不得断言 100%。
// 所有可选语言都有包；缺包回退路径改用不可选的假 locale 做 API 级验证。

const BUNDLED_PACKS = [
  "zh-TW",
  "ar",
  "de",
  "es",
  "fa",
  "fr",
  "he",
  "hi",
  "id",
  "it",
  "ja",
  "ko",
  "pl",
  "pt",
  "ru",
  "tr",
  "cs",
  "da",
  "fi",
  "ms",
  "no",
  "th",
  "uk",
  "vi",
];

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

async function chooseLanguage(page: Page, value: string): Promise<void> {
  await page.locator(".ym-select__trigger").click();
  await page.locator(`.ym-select__option[data-value="${value}"]`).click();
}

async function openLanguageSettings(page: Page): Promise<void> {
  await page.goto("/#/settings/language");
  await expect(page.getByRole("heading", { level: 1, name: "Language" })).toBeVisible();
}

async function overrideSystemLanguage(page: Page, code: string): Promise<void> {
  await page.addInitScript((value: string) => {
    Object.defineProperty(Navigator.prototype, "language", {
      configurable: true,
      get: () => value,
    });
    Object.defineProperty(Navigator.prototype, "languages", {
      configurable: true,
      get: () => [value],
    });
  }, code);
}

test("lists all 26 languages and applies the zh-TW high-coverage pack", async ({ page }) => {
  await openLanguageSettings(page);

  const trigger = page.locator(".ym-select__trigger");
  await trigger.click();
  const values = await page
    .locator(".ym-select__option")
    .evaluateAll((options) => options.map((option) => (option as HTMLElement).dataset.value));

  // 26 种全部可选，不再只允许 zh-CN/en。
  expect(values).toHaveLength(26);
  expect(values).toContain("zh-TW");
  expect(values).toContain("ja");
  expect(values).toContain("ar");

  await page.locator('.ym-select__option[data-value="zh-TW"]').click();
  await expect(trigger.locator(".ym-select__value")).toContainText("繁體中文");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");

  // 语言包内的键显示繁体中文。
  await expect(page.getByRole("heading", { level: 1, name: "語言" })).toBeVisible();
  await expect(page.getByText("當前介面語言")).toBeVisible();

  // 完成度按实际键数计算：高覆盖但非完整，不伪造 100%。
  const completionBadge = page.getByText(/^已翻譯 \d+%$/);
  await expect(completionBadge).toBeVisible();
  const percent = Number(/(\d+)%/.exec((await completionBadge.textContent()) ?? "")?.[1]);
  expect(percent).toBeGreaterThanOrEqual(95);
  expect(percent).toBeLessThan(100);

  await expect(page.getByRole("alert")).toHaveText(
    "該語言僅完成部分翻譯，缺失文案回退英文。",
  );

  // 缺键仍回退英文；旧授权文案不得随包带回。
  const details = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    await i18n.ensureLocalePack("zh-TW");
    i18n.setLocale("zh-TW");
    const en = i18n.getEnglishMessages();
    const staleKeys = [
      "settings.privacy.consentTitle",
      "settings.privacy.consentBody",
      // 逐文件选语言已由“目标语言多选 + 文件名识别”替代，旧译文不得带入。
      "voiceClone.sub.translatedAuto",
    ];
    return {
      completion: i18n.getLocaleCompletion("zh-TW"),
      translatedNav: i18n.t("nav.projects"),
      fallback: i18n.t("pipeline.editor.saving"),
      englishFallback: en["pipeline.editor.saving"],
      stale: staleKeys.map((key) => ({ key, text: i18n.t(key), english: en[key] })),
    };
  });
  expect(details.completion).toBeGreaterThanOrEqual(95);
  expect(details.completion).toBeLessThan(100);
  expect(details.translatedNav).toBe("專案");
  expect(details.fallback).toBe(details.englishFallback);
  expect(details.fallback).not.toBe("pipeline.editor.saving");
  for (const item of details.stale) {
    // 该键在包内被有意省略：回退当前英文，且不含已移除步骤的旧文案。
    expect(item.text, item.key).toBe(item.english);
    expect(item.text, item.key).not.toMatch(/授權|授权/);
  }
});

test("applies a high-coverage Japanese pack without a missing-pack notice", async ({ page }) => {
  await openLanguageSettings(page);

  await chooseLanguage(page, "ja");
  await expect(page.locator(".ym-select__value")).toContainText("日本語");
  await expect(page.locator("html")).toHaveAttribute("lang", "ja");
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");
  await expect(page.getByRole("heading", { level: 1, name: "言語" })).toBeVisible();

  const completion = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    await i18n.ensureLocalePack("ja");
    return i18n.getLocaleCompletion("ja");
  });
  expect(completion).toBeGreaterThan(0);
  expect(completion).toBeLessThan(100);

  // 有包但非完整：显示“部分翻译”，不是“尚无语言包”。
  await expect(page.getByRole("alert")).toHaveText(
    "この言語は一部のみ翻訳されています。未翻訳部分は英語にフォールバックします。",
  );
  await expect(page.getByRole("main")).toBeVisible();
});

test("falls back to English for a locale without a pack (API level)", async ({ page }) => {
  await openLanguageSettings(page);

  // 26 个可选语言现在都有包；缺包回退仍必须保留，用不可选的假 locale 验证机制本身。
  const result = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    await i18n.ensureLocalePack("xx");
    i18n.setLocale("xx");
    const en = i18n.getEnglishMessages();
    return {
      hasPack: i18n.hasLocalePack("xx"),
      completion: i18n.getLocaleCompletion("xx"),
      text: i18n.t("settings.language.title"),
      english: en["settings.language.title"],
      lang: document.documentElement.lang,
      dir: document.documentElement.dir,
    };
  });
  expect(result.hasPack).toBe(false);
  expect(result.completion).toBe(0);
  expect(result.text).toBe(result.english);
  expect(result.text).not.toBe("settings.language.title");
  expect(result.lang).toBe("xx");
  expect(result.dir).toBe("ltr");

  // 回退后界面仍完整渲染，不白屏。
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Language" })).toBeVisible();
});

test("hides completion badge and progress for a fully translated locale", async ({ page }) => {
  // seed 的当前语言是 en（compiled，100%）：语言页不显示完成度 badge 或进度条。
  await openLanguageSettings(page);
  await expect(page.locator(".current-card .ym-progress")).toHaveCount(0);
  await expect(page.locator(".current-card .ym-badge")).toHaveCount(0);

  // 同一页面切到 zh-TW（95%）：部分翻译的 badge、进度条与提示仍然显示。
  await chooseLanguage(page, "zh-TW");
  await expect(page.locator(".current-card .ym-progress")).toHaveCount(1);
  await expect(page.locator(".current-card .ym-badge")).toHaveCount(1);
  await expect(page.getByText(/^已翻譯 \d+%$/)).toBeVisible();
  await expect(page.getByRole("alert")).toHaveText(
    "該語言僅完成部分翻譯，缺失文案回退英文。",
  );

  // 切回 compiled 100% 语言后 badge/进度条再次隐藏。
  await chooseLanguage(page, "en");
  await expect(page.locator(".current-card .ym-progress")).toHaveCount(0);
  await expect(page.locator(".current-card .ym-badge")).toHaveCount(0);
});

test("every bundled pack is a valid English-subset translation with measured completion", async ({
  page,
}) => {
  await openLanguageSettings(page);

  const result = await page.evaluate(async (codes: string[]) => {
    const i18n = await import("/src/i18n/index.ts");
    const pack = await import("/src/i18n/pack.ts");
    const en = i18n.getEnglishMessages();
    const placeholders = (value: string) =>
      [...value.matchAll(/\{([A-Za-z0-9_]+)\}/g)]
        .map((match) => match[1])
        .sort()
        .join(",");
    const bad: string[] = [];
    const completion: Record<string, number> = {};
    const hasPack: Record<string, boolean> = {};
    for (const code of codes) {
      hasPack[code] = i18n.hasLocalePack(code);
      const mod = await import(/* @vite-ignore */ `/src/i18n/locales/${code}.json`);
      const raw = mod.default as Record<string, unknown>;
      const keys = Object.keys(raw);
      for (const key of keys) {
        if (!(key in en)) bad.push(`${code}:extra:${key}`);
        if (/voiceClone\.auth|voiceAuthorization|voice_authorization/i.test(key)) {
          bad.push(`${code}:auth:${key}`);
        }
        const value = raw[key];
        if (typeof value !== "string" || value.trim() === "") {
          bad.push(`${code}:invalid:${key}`);
        } else if (key in en && placeholders(value) !== placeholders(en[key])) {
          bad.push(`${code}:placeholder:${key}`);
        }
      }
      const sanitized = pack.sanitizeLocalePack(raw, en);
      // 落盘内容必须全部有效：不可靠运行时过滤来抬完成度。
      if (Object.keys(sanitized).length !== keys.length) bad.push(`${code}:not-all-valid`);
      completion[code] = pack.completionPercent(sanitized, en);
    }
    return { bad, completion, hasPack, englishKeys: Object.keys(en).length };
  }, BUNDLED_PACKS);

  expect(result.bad).toEqual([]);
  expect(result.englishKeys).toBeGreaterThan(1500);
  for (const code of BUNDLED_PACKS) {
    expect(result.hasPack[code], `${code} should have a pack`).toBe(true);
    expect(result.completion[code], `${code} completion`).toBeGreaterThanOrEqual(90);
    expect(result.completion[code], `${code} completion`).toBeLessThan(100);
  }
  expect(result.completion["zh-TW"]).toBeGreaterThanOrEqual(95);
});

test("keeps the app layout LTR while switching to RTL-script languages", async ({ page }) => {
  await openLanguageSettings(page);

  await chooseLanguage(page, "ar");
  await expect(page.locator("html")).toHaveAttribute("lang", "ar");
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");
  // ar 现在有语言包：完成度必须是实测值，不是缺包 0%。
  const completion = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    await i18n.ensureLocalePack("ar");
    return i18n.getLocaleCompletion("ar");
  });
  expect(completion).toBeGreaterThan(0);

  await chooseLanguage(page, "en");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");
  // en 是 compiled 100%：完成度 badge 与进度条隐藏。
  await expect(page.locator(".current-card .ym-progress")).toHaveCount(0);
  await expect(page.locator(".current-card .ym-badge")).toHaveCount(0);
});

test("follows a matching system language", async ({ page }) => {
  await overrideSystemLanguage(page, "zh-TW");
  await page.goto("/#/settings/language");

  // 精确匹配系统语言并加载对应语言包；跟随系统默认开启。
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");
  await expect(page.getByRole("heading", { level: 1, name: "語言" })).toBeVisible();
});

test("falls back to English when the system language has no match", async ({ page }) => {
  await overrideSystemLanguage(page, "xx-YY");
  await page.goto("/#/settings/language");

  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("heading", { level: 1, name: "Language" })).toBeVisible();
});

test("applies the system language on first launch outside settings", async ({ page }) => {
  await overrideSystemLanguage(page, "ja-JP");
  await page.addInitScript(() => {
    // 首次启动：没有已保存 locale，也没有关闭跟随系统。
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: true }));
    localStorage.removeItem("yaomu.settings.followSystem");
  });
  await page.goto("/#/projects");

  // 不先打开设置页，应用启动时就应按系统语言匹配到 ja；页面按 ja 包或英文回退渲染。
  await expect(page.locator("html")).toHaveAttribute("lang", "ja");
  await expect(page.getByRole("main")).toBeVisible();
});

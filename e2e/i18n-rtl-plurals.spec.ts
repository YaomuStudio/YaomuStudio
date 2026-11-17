import { expect, test, type Page } from "@playwright/test";
import { seedMockFault, seedOnboardedSettings } from "./support";

// 复数、语言包有效条目与 RTL 值级方向。
// 浏览器 demo 只证明 renderer 行为与 mock 渲染；真实 Core 保存/持久化仍由隔离 Electron 验证。

async function openProjects(page: Page): Promise<void> {
  await page.goto("/#/projects");
  await expect(page.getByTestId("project-list")).toBeVisible();
}

test("tCount follows the displayed fallback language and rejects invalid counts", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await openProjects(page);

  const result = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    const out: Record<string, string> = {};
    i18n.setLocale("en");
    out.en0 = i18n.tCount("projects.list.count", 0);
    out.en1 = i18n.tCount("projects.list.count", 1);
    out.en2 = i18n.tCount("projects.list.count", 2);
    out.enMany = i18n.tCount("projects.list.count", 11);
    out.override = i18n.tCount("projects.list.count", 2, { count: 99 });
    out.negative = i18n.tCount("projects.list.count", -1);
    out.fraction = i18n.tCount("projects.list.count", 1.5);
    out.nan = i18n.tCount("projects.list.count", Number.NaN);
    out.infinity = i18n.tCount("projects.list.count", Number.POSITIVE_INFINITY);
    out.unsafe = i18n.tCount("projects.list.count", Number.MAX_SAFE_INTEGER + 1);
    i18n.setLocale("zh-CN");
    out.zh0 = i18n.tCount("projects.list.count", 0);
    out.zh1 = i18n.tCount("projects.list.count", 1);
    out.zh2 = i18n.tCount("projects.list.count", 2);
    // 不可选的假 locale 没有语言包：界面回退英文，数量也必须按英文规则重算。
    await i18n.ensureLocalePack("xx");
    i18n.setLocale("xx");
    out.xxHasPack = i18n.hasLocalePack("xx");
    out.xxCompletion = i18n.getLocaleCompletion("xx");
    out.xx1 = i18n.tCount("projects.list.count", 1);
    out.xx2 = i18n.tCount("projects.list.count", 2);
    // ar 有语言包：按 ar 的 family 渲染（只有 one/other 时 two 回退 other）。
    await i18n.ensureLocalePack("ar");
    i18n.setLocale("ar");
    out.ar1 = i18n.tCount("projects.list.count", 1);
    out.ar2 = i18n.tCount("projects.list.count", 2);
    await i18n.ensureLocalePack("zh-TW");
    i18n.setLocale("zh-TW");
    out.tw2 = i18n.tCount("projects.list.count", 2);
    // 无 family 时回退 key（开发期暴露；实际调用键由 i18n guard 拒绝）。
    out.missingFamily = i18n.tCount("settings.storageUsed", 1);
    return out;
  });

  expect(result.en0).toBe("0 projects");
  expect(result.en1).toBe("1 project");
  expect(result.en2).toBe("2 projects");
  expect(result.enMany).toBe("11 projects");
  // count 参数不可被 params.count 覆盖。
  expect(result.override).toBe("2 projects");
  for (const value of [
    result.negative,
    result.fraction,
    result.nan,
    result.infinity,
    result.unsafe,
  ]) {
    expect(value).toBe("—");
  }
  expect(result.zh0).toBe("0 个项目");
  expect(result.zh1).toBe("1 个项目");
  expect(result.zh2).toBe("2 个项目");
  // 无包语言按英文规则回退。
  expect(result.xxHasPack).toBe(false);
  expect(result.xxCompletion).toBe(0);
  expect(result.xx1).toBe("1 project");
  expect(result.xx2).toBe("2 projects");
  // 有包的 ar 使用包内译文，不再显示英文模板。
  expect(result.ar1).toContain("1");
  expect(result.ar1).not.toBe("1 project");
  expect(result.ar2).toContain("2");
  expect(result.ar2).not.toBe("2 projects");
  expect(result.tw2).toBe("共 2 個專案");
  expect(result.missingFamily).toBe("settings.storageUsed");
});

test("language pack validity and completion share one measured rule", async ({ page }) => {
  await seedOnboardedSettings(page);
  await openProjects(page);

  const result = await page.evaluate(async () => {
    const pack = await import("/src/i18n/pack.ts");
    const english = {
      "page.count.one": "{count} item",
      "page.count.other": "{count} items",
      "page.label": "Label",
      "page.placeholder": "Hi {name}",
    };
    const sanitized = pack.sanitizeLocalePack(
      {
        "page.count.one": "{count} 項",
        "page.count.other": "{count} 項",
        "page.label": "標籤",
        "page.placeholder": "嗨 {wrong}",
        "page.unknown": "額外鍵",
        "page.empty": "   ",
        "page.number": 5,
      },
      english,
    );
    return {
      keys: Object.keys(sanitized).sort(),
      completion: pack.completionPercent(sanitized, english),
    };
  });

  // 未知键、空白值、非字符串、占位错误都不进入有效条目。
  expect(result.keys).toEqual(["page.count.one", "page.count.other", "page.label"]);
  expect(result.completion).toBe(75);
});

test("extra CLDR plural categories are valid for known families but do not inflate completion", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await openProjects(page);

  const result = await page.evaluate(async () => {
    const pack = await import("/src/i18n/pack.ts");
    const plural = await import("/src/i18n/plural.ts");
    const english = {
      "page.count.one": "{count} item",
      "page.count.other": "{count} items",
      "page.label": "Label",
      "page.extra.one": "{count} extra",
      "page.extra.other": "{count} extras",
    };
    const sanitized = pack.sanitizeLocalePack(
      {
        "page.count.other": "{count} عناصر",
        "page.count.two": "عنصران ({count})",
        "page.count.few": "{count} عناصر قليلة",
        "page.count.many": "{count} عنصراً",
        "page.count.zero": "{count} صفر",
        "page.count.lots": "{count} bad suffix",
        "page.unknown.two": "{count} unknown family",
        "page.extra.few": "占位不符",
        "page.label": "تسمية",
      },
      english,
    );
    return {
      keys: Object.keys(sanitized).sort(),
      completion: pack.completionPercent(sanitized, english),
      two: plural.resolvePluralTemplate(sanitized, english, "ar", "page.count", 2),
      few: plural.resolvePluralTemplate(sanitized, english, "ar", "page.count", 3),
      zero: plural.resolvePluralTemplate(sanitized, english, "ar", "page.count", 0),
      unknown: plural.resolvePluralTemplate(sanitized, english, "ar", "page.unknown", 2),
      categoryTwo: plural.pluralCategory("ar", 2),
      categoryFew: plural.pluralCategory("ar", 3),
    };
  });

  // 已知 family 下的 CLDR 额外类别被保留；乱后缀、未知 family、占位不符被过滤。
  expect(result.keys).toEqual([
    "page.count.few",
    "page.count.many",
    "page.count.other",
    "page.count.two",
    "page.count.zero",
    "page.label",
  ]);
  // 额外类别可渲染，但不占英文覆盖数：交集 2 / 英文 5 = 40%。
  expect(result.completion).toBe(40);
  expect(result.categoryTwo).toBe("two");
  expect(result.categoryFew).toBe("few");
  expect(result.two).toBe("عنصران ({count})");
  expect(result.few).toBe("{count} عناصر قليلة");
  expect(result.zero).toBe("{count} صفر");
  // 未知 family 不获得复数 fallback，由调用方回退 key。
  expect(result.unknown).toBeUndefined();
});

test("projects and correction use cardinal plurals", async ({ page }) => {
  await seedOnboardedSettings(page);
  await openProjects(page);

  // demo 有 3 个项目；目标语言以数量行 + chip 展示，不再出现 language(s)。
  await expect(page.getByText("3 projects", { exact: false })).toBeVisible();
  const demoCard = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  const langRow = demoCard.locator(".recent-card__detail--languages");
  await expect(langRow).toBeVisible();
  await expect(langRow.locator(".recent-card__lang-chip").first()).toHaveText("English");
  await expect(
    demoCard.getByTestId("recent-card-lang-count").locator(".recent-card__detail-value"),
  ).toHaveText("1");
  await expect(page.getByText(/\d+ language\(s\)/)).toHaveCount(0);

  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByText("8 sentences")).toBeVisible();
});

test("zh-TW keeps its measured high-coverage copy and English fallback", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/language");
  await page.locator(".ym-select__trigger").click();
  await page.locator('.ym-select__option[data-value="zh-TW"]').click();

  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");
  await expect(page.getByRole("heading", { level: 1, name: "語言" })).toBeVisible();
  // 关键导航显示繁体中文。
  await expect(
    page.getByRole("navigation", { name: "primary" }).getByRole("link", { name: "專案" }),
  ).toBeVisible();

  const completionBadge = page.getByText(/^已翻譯 \d+%$/);
  const percent = Number(/(\d+)%/.exec((await completionBadge.textContent()) ?? "")?.[1]);
  expect(percent).toBeGreaterThanOrEqual(95);
  expect(percent).toBeLessThan(100);
  await expect(page.getByRole("alert")).toHaveText(
    "該語言僅完成部分翻譯，缺失文案回退英文。",
  );

  // 已知缺失键回退英文；被省略的旧授权键也不得出现授权文案。
  const fallback = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    await i18n.ensureLocalePack("zh-TW");
    i18n.setLocale("zh-TW");
    return {
      text: i18n.t("pipeline.editor.saving"),
      english: i18n.getEnglishMessages()["pipeline.editor.saving"],
      emptyDesc: i18n.t("voiceClone.result.emptyDesc"),
    };
  });
  expect(fallback.text).toBe(fallback.english);
  expect(fallback.text).not.toBe("pipeline.editor.saving");
  expect(fallback.emptyDesc).not.toMatch(/授權|授权/);

  await page.goto("/#/projects");
  await expect(page.getByText("共 3 個專案", { exact: false })).toBeVisible();
  const demoCard = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  const langRow = demoCard.locator(".recent-card__detail--languages");
  await expect(langRow).toBeVisible();
  await expect(langRow.locator(".recent-card__lang-chip").first()).toHaveText("英语");
  await expect(
    demoCard.getByTestId("recent-card-lang-count").locator(".recent-card__detail-value"),
  ).toHaveText("1");
});

test("RTL keeps values isolated and timecode inputs LTR", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/language");
  await page.locator(".ym-select__trigger").click();
  await page.locator('.ym-select__option[data-value="ar"]').click();
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");

  await page.goto("/#/projects");
  await expect(page.getByTestId("project-list")).toBeVisible();
  // 设备名用 auto，磁盘数值+单位用 LTR 隔离，不在 RTL 中被重排。
  const status = page.locator("footer.statusbar");
  await expect(status.locator("bdi[dir='ltr']").first()).toBeVisible();
  await expect(status.locator("bdi[dir='auto']").first()).toBeVisible();

  // 列表头部数量短语单独隔离；排序已改为下拉选择，不再有第二个文本短语。
  // ar 已有语言包，不能再用英文文案定位；按结构与数字内容断言。
  const listHead = page.locator(".section-head .muted");
  await expect(listHead.locator("bdi")).toHaveCount(1);
  await expect(listHead.locator("bdi").first()).toContainText(/\d/);

  // 卡片日期/时间码是协议式固定格式，按值级 LTR 隔离。
  const demoCard = page
    .locator("article.recent-card")
    .filter({ hasText: "星际译制 · 试播集" });
  await expect(demoCard.locator("bdi[dir='ltr']").first()).toHaveText(
    /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/,
  );

  await demoCard.locator(".recent-card__download").click();
  const download = page.locator(".ym-dialog").filter({ has: page.locator(".download-list") });
  await expect(download.locator(".download-row__name bdi[dir='auto']").first()).toBeVisible();
  // 主 label 可能包含原文件名（如 “Imported subtitles · mixed-…srt”），按值级 auto 隔离。
  await expect(download.locator(".download-row__label bdi[dir='auto']").first()).toBeVisible();
  await download.locator(".ym-dialog__footer button").click();

  await demoCard.locator(".recent-card__play").click();
  const preview = page.locator(".ym-dialog").filter({ has: page.locator(".product-preview") });
  await expect(preview.locator(".product-preview__tab bdi[dir='auto']").first()).toBeVisible();
  await preview.locator(".ym-dialog__footer button").click();

  // 时间码输入在 RTL 页面仍按 LTR 编辑，媒体时间轴不镜像。
  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByTestId("segment-row").first()).toBeVisible();
  // 计数短语按值级 auto 隔离，RTL 页面不拆散数量短语（ar 包缺该 family 时回退英文）。
  await expect(page.locator(".mcs-meta bdi").nth(2)).toContainText(/\d/);
  await expect(page.locator(".subpanel__count bdi").first()).toContainText(/\d/);
  const firstRow = page.getByTestId("segment-row").first();
  await expect(firstRow.locator("input").first()).toHaveAttribute("dir", "ltr");
  await expect(firstRow.locator("input").nth(1)).toHaveAttribute("dir", "ltr");
});

test("project detail counts are value-isolated under RTL", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/language");
  await page.locator(".ym-select__trigger").click();
  await page.locator('.ym-select__option[data-value="ar"]').click();
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");

  await page.goto("/#/projects/proj-demo");
  await expect(page.locator(".card-head .muted bdi").first()).toContainText(/\d/);
  // 字幕轨行内：句数与版本数分别由 bdi 隔离。
  await expect(page.locator(".track-row bdi").nth(0)).toContainText(/\d/);
  await expect(page.locator(".track-row bdi").nth(1)).toContainText(/\d/);
});

test("a displayed safe error re-renders on locale change", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockFault(page, "listProjects");
  await page.goto("/#/projects");

  const error = page.getByRole("alert");
  await expect(error).toContainText("The local service is not ready.");
  await expect(error).not.toContainText("Error:");
  await expect(error).not.toContainText("stack");

  await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    i18n.setLocale("zh-CN");
  });
  await expect(error).toContainText("本地服务未就绪");

  // 未知错误只显示已注册回退；原始 Error.message 不进入提示。
  const fallback = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    const notice = await import("/src/i18n/notice.ts");
    i18n.setLocale("en");
    return {
      fallback: notice.renderNotice(
        notice.fallbackErrorNotice(new Error("SECRET raw message"), "projects.error.generic"),
      ),
      generic: notice.renderNotice(notice.errorNotice(new Error("SECRET raw message"))),
    };
  });
  expect(fallback.fallback).toBe("Something went wrong. Try again.");
  expect(fallback.generic).not.toContain("SECRET");
});

test("model slot notice params translate at render time and keep the original slot", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await openProjects(page);

  const result = await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    const notice = await import("/src/i18n/notice.ts");
    i18n.setLocale("en");
    const asr = notice.messageNotice("models.paramsSaved", {
      name: notice.noticeKey("slot.asr"),
    });
    const tts = notice.messageNotice("models.paramsSaved", {
      name: notice.noticeKey("slot.tts"),
    });
    const before = [notice.renderNotice(asr), notice.renderNotice(tts)];
    i18n.setLocale("zh-CN");
    const after = [notice.renderNotice(asr), notice.renderNotice(tts)];
    const asrParam = asr.params?.name;
    return {
      before,
      after,
      asrKey:
        asrParam && typeof asrParam === "object" ? asrParam.messageKey : "",
    };
  });

  expect(result.before).toEqual([
    "Parameters saved for Speech recognition.",
    "Parameters saved for Speech synthesis.",
  ]);
  // 冻结的是槽位身份，不是已翻译字符串：切语言后同一条提示重新翻译。
  expect(result.after).toEqual(["已保存 语音识别 的参数。", "已保存 语音合成 的参数。"]);
  expect(result.asrKey).toBe("slot.asr");
});

test("an already displayed notice re-renders on locale change without resending the save", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/projects/proj-demo/correction");

  const firstRow = page.getByTestId("segment-row").first();
  await firstRow.getByTestId("segment-translation").fill("Locale switch keeps this draft");
  const save = page.locator(".mcs-top__actions button").first();
  await save.click();
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  await expect(save).toBeDisabled();

  // 只切换语言：已显示提示按新语言重渲染，不重新提交、不丢正文。
  await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    i18n.setLocale("zh-CN");
  });
  await expect(
    page.getByRole("status").filter({ hasText: "校正已保存。" }),
  ).toBeVisible();
  await expect(save).toBeDisabled();
  await expect(firstRow.getByTestId("segment-translation")).toHaveValue(
    "Locale switch keeps this draft",
  );

  await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    i18n.setLocale("en");
  });
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
});

import { expect, test, type Page } from "@playwright/test";
import {
  SAMPLE_AUDIO,
  SAMPLE_MEDIA,
  SAMPLE_SRT,
  SAMPLE_TRANSLATED_SRT,
  seedCapabilityStates,
  seedMockControl,
  seedMockFault,
  seedOnboardedSettings,
} from "./support";

// 第二批本地化提示：工具/克隆/引导/任务中心的已显示提示在切换语言后必须重渲染，
// 且不重发安装/运行/保存/导出请求、不重置调用方状态。
// 浏览器 demo 只证明 renderer 行为；真实 Core 持久化不在本文件范围。

async function switchLocale(page: Page, locale: string): Promise<void> {
  await page.evaluate(async (value) => {
    const i18n = await import("/src/i18n/index.ts");
    i18n.setLocale(value);
  }, locale);
}

async function setMockValue(page: Page, key: string, value: unknown): Promise<void> {
  await page.evaluate(
    ({ name, val }) => {
      const control = (window as Window & { __yaomuMock?: Record<string, unknown> })
        .__yaomuMock;
      if (control) control[name] = val;
    },
    { name: key, val: value },
  );
}

async function readMockNumber(page: Page, key: string): Promise<number> {
  return page.evaluate((name) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> })
      .__yaomuMock;
    return Number(control?.[name]) || 0;
  }, key);
}

async function seedProjectRunStatuses(
  page: Page,
  fixtures: Record<string, string>,
): Promise<void> {
  await page.addInitScript((value) => {
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.projectRunStatusFixtures = value;
  }, fixtures);
}

// ---------------------------------------------------------------------------
// Task center
// ---------------------------------------------------------------------------

test("onboarding environment error re-renders without exposing raw text or refetching", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({ onboarded: false, locale: "en" }),
    );
    localStorage.setItem("yaomu.onboarding.step", "environment");
    const scope = window as Window & { __yaomuMock?: Record<string, unknown> };
    const control = (scope.__yaomuMock ??= {});
    control.environmentFailure = true;
  });

  await page.goto("/");
  // 提示容器本身不随语言改变，避免用旧语言文本过滤后切换语言时定位失败。
  const alert = page.locator(".ym-notice.is-error").first();
  await expect(alert).toContainText(
    "Could not check the environment: Something went wrong. Check the local logs and retry.",
  );
  await expect(alert).not.toContainText("mock environment secret");

  const before = await readMockNumber(page, "environmentCalls");
  await switchLocale(page, "zh-CN");
  await expect(alert).toContainText(
    "环境检查失败：操作失败，请查看本地日志后重试。",
  );
  expect(await readMockNumber(page, "environmentCalls")).toBe(before);
});

test("onboarding install and cancel notices follow the locale without resending", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({ onboarded: false, locale: "en" }),
    );
    localStorage.setItem("yaomu.onboarding.step", "models");
  });
  await seedMockControl(page);
  await seedCapabilityStates(page, { asr: "NEEDS_SETUP" });

  await page.goto("/");
  const asrRow = page.locator(".model").filter({ hasText: "Speech recognition" });
  await asrRow.getByRole("button", { name: "Download", exact: true }).click();
  await page.getByRole("button", { name: "Download and install" }).click();

  await expect(
    page.locator(".ym-notice[role='status']").filter({ hasText: "Download requested" }),
  ).toHaveCount(0);
  await expect(asrRow.getByRole("button", { name: "Installing", exact: true })).toBeVisible();
  await expect(asrRow.getByRole("progressbar")).toBeVisible();
  const planBefore = await readMockNumber(page, "planRequests");
  const installBefore = await readMockNumber(page, "installRequests");

  await switchLocale(page, "zh-CN");
  const zhRow = page.locator(".model").filter({ hasText: "语音识别" });
  await expect(zhRow.getByRole("button", { name: "安装中", exact: true })).toBeVisible();
  await expect(
    page.locator(".ym-notice[role='status']").filter({ hasText: "已请求下载" }),
  ).toHaveCount(0);
  // 仍停留在模型步骤，未因切换语言跳转或重置步骤。
  await expect(page.getByRole("heading", { name: "下载模型包" })).toBeVisible();
  expect(await readMockNumber(page, "planRequests")).toBe(planBefore);
  expect(await readMockNumber(page, "installRequests")).toBe(installBefore);

  await zhRow.getByRole("button", { name: "取消", exact: true }).click();
  await expect(
    page
      .locator(".ym-notice[role='status']")
      .filter({ hasText: "已请求取消，稍后可继续。" })
      .first(),
  ).toBeVisible();
  const cancelIds = await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> })
      .__yaomuMock;
    return Array.isArray(control?.cancelInstallationIds)
      ? [...(control?.cancelInstallationIds as string[])]
      : [];
  });
  expect(cancelIds.length).toBe(1);

  await switchLocale(page, "en");
  await expect(
    page
      .locator(".ym-notice[role='status']")
      .filter({ hasText: "Cancellation requested. You can resume later." })
      .first(),
  ).toBeVisible();
  const cancelIdsAfter = await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> })
      .__yaomuMock;
    return Array.isArray(control?.cancelInstallationIds)
      ? [...(control?.cancelInstallationIds as string[])]
      : [];
  });
  expect(cancelIdsAfter).toEqual(cancelIds);
});

// ---------------------------------------------------------------------------
// Voice clone
// ---------------------------------------------------------------------------

test("voice clone save notices re-render on locale switch without resaving", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/voice-clone");
  await page.locator('input[accept^=".mp3"]').setInputFiles(SAMPLE_AUDIO);
  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles(SAMPLE_SRT);
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "简体中文", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
  // 选中标签显示在输入框内，不再单独占一行。
  await expect(
    page.getByTestId("language-picker-trigger").locator(".language-chip"),
  ).toHaveCount(1);
  await expect(page.locator(".language-select__chips")).toHaveCount(0);
  await page.locator('input[accept=".srt"][multiple]').setInputFiles(SAMPLE_TRANSLATED_SRT);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("full-zh-CN.wav")).toBeVisible();

  await page.getByRole("button", { name: "Save audio" }).first().click();
  await expect(page.getByText(/Saved to ~\/Downloads\/clone-.*\.wav/)).toBeVisible();
  const saved = await readMockNumber(page, "saveAssetCalls");

  await switchLocale(page, "zh-CN");
  await expect(page.getByText(/已保存到 ~\/Downloads\/clone-.*\.wav/)).toBeVisible();
  // 目标语言与已上传文件不因切换语言变化；VoiceClone 已无项目名称字段。
  await expect(page.locator(".language-chip").filter({ hasText: "简体中文" })).toBeVisible();
  await expect(page.locator('input[accept=".srt"][multiple]')).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(saved);

  await setMockValue(page, "saveAssetFailure", true);
  await page.getByRole("button", { name: "保存音频" }).first().click();
  await expect(page.getByText("保存音频失败。")).toBeVisible();
  const failed = await readMockNumber(page, "saveAssetCalls");
  await switchLocale(page, "en");
  await expect(page.getByText("Saving the audio failed.")).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(failed);
  expect(failed).toBe(saved + 1);
});

// ---------------------------------------------------------------------------
// Tools: separate / erase
// ---------------------------------------------------------------------------

test("separate save notices re-render on locale switch without resaving", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/tools/separate");
  await page.getByPlaceholder("Project name").fill("E2E separate locale project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("voice.wav")).toBeVisible();

  await page.getByRole("button", { name: "Save" }).first().click();
  await expect(page.getByText(/Saved to ~\/Downloads\/voice\.wav/)).toBeVisible();
  const saved = await readMockNumber(page, "saveAssetCalls");

  await switchLocale(page, "zh-CN");
  await expect(page.getByText(/已保存到 ~\/Downloads\/voice\.wav/)).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(saved);

  await setMockValue(page, "saveAssetFailure", true);
  await page.getByRole("button", { name: "保存" }).first().click();
  await expect(page.getByText("保存音轨失败。")).toBeVisible();
  const failed = await readMockNumber(page, "saveAssetCalls");
  await switchLocale(page, "en");
  await expect(page.getByText("Saving the track failed.")).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(failed);
  expect(failed).toBe(saved + 1);
  // 保存失败不丢已生成结果。
  await expect(page.getByText("voice.wav")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(2);
});

test("erase save notices re-render on locale switch without resaving", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/tools/erase");
  await page.getByPlaceholder("Project name").fill("E2E erase locale project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_MEDIA);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("clean.mp4")).toBeVisible();

  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(/Saved to ~\/Downloads\/clean\.mp4/)).toBeVisible();
  const saved = await readMockNumber(page, "saveAssetCalls");

  await switchLocale(page, "zh-CN");
  await expect(page.getByText(/已保存到 ~\/Downloads\/clean\.mp4/)).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(saved);

  await setMockValue(page, "saveAssetFailure", true);
  await page.getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("保存纯净视频失败。")).toBeVisible();
  const failed = await readMockNumber(page, "saveAssetCalls");
  await switchLocale(page, "en");
  await expect(page.getByText("Saving the cleaned video failed.")).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(failed);
  expect(failed).toBe(saved + 1);
  await expect(page.getByText("clean.mp4")).toBeVisible();
});

// ---------------------------------------------------------------------------
// Tools: custom pipeline
// ---------------------------------------------------------------------------

async function openCustomWithMix(page: Page): Promise<void> {
  await page.goto("/#/tools/custom");
  await page.getByPlaceholder("Project name").fill("E2E custom locale project");
  await page.locator('input[accept^="video/*"]').setInputFiles(SAMPLE_MEDIA);
  await page.locator("label.ym-checkbox").filter({ hasText: "Subtitle translation" }).click();
  await page.locator("label.ym-checkbox").filter({ hasText: "Mixed delivery" }).click();
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "简体中文", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
}

test("custom pipeline save notices re-render on locale switch without resaving", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await openCustomWithMix(page);
  await setMockValue(page, "customSkipReview", true);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("zh-CN-mix.mp4")).toBeVisible();

  const mixRow = page.locator(".asset-row").filter({ hasText: "zh-CN-mix.mp4" });
  await mixRow.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(/Saved to ~\/Downloads\/zh-CN-mix\.mp4/)).toBeVisible();
  const saved = await readMockNumber(page, "saveAssetCalls");

  await switchLocale(page, "zh-CN");
  await expect(page.getByText(/已保存到 ~\/Downloads\/zh-CN-mix\.mp4/)).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(saved);

  await setMockValue(page, "saveAssetFailure", true);
  await mixRow.getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("保存失败。")).toBeVisible();
  const failed = await readMockNumber(page, "saveAssetCalls");
  await switchLocale(page, "en");
  await expect(page.getByText("Saving failed.")).toBeVisible();
  expect(await readMockNumber(page, "saveAssetCalls")).toBe(failed);
  expect(failed).toBe(saved + 1);
  await expect(page.getByText("zh-CN-mix.mp4")).toBeVisible();
});

test("custom pipeline export failure re-renders on locale switch without re-exporting", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/tools/custom");
  await page.getByPlaceholder("Project name").fill("E2E custom export locale");
  await page.locator('input[accept^="video/*"]').setInputFiles(SAMPLE_MEDIA);
  await page.locator("label.ym-checkbox").filter({ hasText: "Subtitle translation" }).click();
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "简体中文", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
  await setMockValue(page, "customSkipReview", true);
  await setMockValue(page, "exportFailure", true);
  await page.getByRole("button", { name: "Create" }).click();

  const exportButton = page.getByRole("button", { name: "Export SRT" });
  await expect(exportButton).toBeVisible();
  await exportButton.click();
  await expect(page.getByText("Export failed.")).toBeVisible();
  const exported = await readMockNumber(page, "exportCalls");

  await switchLocale(page, "zh-CN");
  await expect(page.getByText("导出失败。")).toBeVisible();
  expect(await readMockNumber(page, "exportCalls")).toBe(exported);
  await expect(page.getByRole("button", { name: "导出 SRT" })).toBeEnabled();
});

// ---------------------------------------------------------------------------
// Settings: devices (already migrated in the first batch; regression guard)
// ---------------------------------------------------------------------------

test("settings device notice re-renders on locale switch without reswitching", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
  await page.goto("/#/settings/devices");
  // 不同宿主的 CPU 设备名不同（Windows "CPU" / macOS "Apple Silicon (CPU)"）。
  await page.locator("label.ym-radio__item").filter({ hasText: /CPU\)?$/ }).click();
  await page.getByRole("button", { name: "Switch device" }).click();

  await expect(
    page.getByRole("status").filter({ hasText: "Compute device updated for models." }),
  ).toBeVisible();
  const switched = await readMockNumber(page, "engineConfigCalls");
  expect(switched).toBeGreaterThan(0);

  await switchLocale(page, "zh-CN");
  await expect(
    page.getByRole("status").filter({ hasText: "已更新模型的计算设备。" }),
  ).toBeVisible();
  expect(await readMockNumber(page, "engineConfigCalls")).toBe(switched);
});

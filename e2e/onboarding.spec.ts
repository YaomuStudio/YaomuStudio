import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: false, locale: "en" }));
    localStorage.removeItem("yaomu.onboarding.step");
  });
});

test("shows local environment checks before model setup", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Welcome to YaomuStudio" })).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();

  await expect(page.getByRole("heading", { name: "Check this machine" })).toBeVisible();
  await expect(page.getByText("Compute devices")).toBeVisible();
  // 设备行展示真实探测口径的 used / total；mock 覆盖当前宿主平台。
  await expect(page.locator(".env-device .mono").first()).toContainText(/ \/ /);
  await expect(page.getByText("FFmpeg is available.")).toBeVisible();
  await expect(page.getByText("Platform")).toBeVisible();
  // mock 按宿主 UA 推断平台，不再写死 macOS。
  await expect(page.getByText(/^(Windows|macOS|Linux)$/)).toBeVisible();

  await page.getByRole("button", { name: "Check again" }).click();
  await expect(page.getByText("FFmpeg is available.")).toBeVisible();
});

test("keeps the environment list scrollable without a page-level scrollbar", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 480 });
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.onboarding.step", "environment");
  });

  await page.goto("/");
  await expect(page.getByTestId("onboarding-environment-checks")).toBeVisible();

  const next = page.getByRole("button", { name: "Continue", exact: true });
  await expect(next).toBeInViewport({ ratio: 1 });

  const layout = await page.evaluate(() => {
    const onboarding = document.querySelector(".onboarding")!;
    const list = document.querySelector(".env-scroll")!;
    return {
      pageOverflow: onboarding.scrollHeight - onboarding.clientHeight,
      pageOverflowY: getComputedStyle(onboarding).overflowY,
      listOverflow: list.scrollHeight - list.clientHeight,
      listOverflowY: getComputedStyle(list).overflowY,
    };
  });
  expect(layout.pageOverflow).toBeLessThanOrEqual(1);
  expect(layout.pageOverflowY).toBe("hidden");
  expect(layout.listOverflow).toBeGreaterThan(0);
  expect(layout.listOverflowY).toBe("auto");
  // 有更多内容时底部渐隐；到列表底部后渐隐消失。
  await expect
    .poll(() =>
      page
        .locator(".env-scroll")
        .evaluate((el) => getComputedStyle(el).getPropertyValue("--ym-fade-bottom").trim()),
    )
    .not.toBe("0px");

  await page.locator(".env-scroll").hover();
  await page.mouse.wheel(0, 600);
  await expect
    .poll(() => page.locator(".env-scroll").evaluate((el) => el.scrollTop))
    .toBeGreaterThan(0);
  await page.locator(".env-scroll").evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await expect
    .poll(() =>
      page
        .locator(".env-scroll")
        .evaluate((el) => getComputedStyle(el).getPropertyValue("--ym-fade-bottom").trim()),
    )
    .toBe("0px");
  await expect
    .poll(() => page.locator(".onboarding").evaluate((el) => el.scrollTop))
    .toBe(0);
  await expect(next).toBeInViewport({ ratio: 1 });
});

test("keeps the model list scrollable without a page-level scrollbar", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 480 });
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.onboarding.step", "models");
  });

  await page.goto("/");
  const list = page.locator(".model-list__scroll");
  await expect(list).toBeVisible();
  const next = page.getByRole("button", { name: "Continue", exact: true });
  await expect(next).toBeInViewport({ ratio: 1 });

  await expect
    .poll(() => list.evaluate((el) => el.scrollHeight - el.clientHeight))
    .toBeGreaterThan(0);
  await expect
    .poll(() =>
      list.evaluate((el) => getComputedStyle(el).getPropertyValue("--ym-fade-bottom").trim()),
    )
    .not.toBe("0px");
  const pageOverflow = await page
    .locator(".onboarding")
    .evaluate((el) => ({
      overflow: el.scrollHeight - el.clientHeight,
      overflowY: getComputedStyle(el).overflowY,
    }));
  expect(pageOverflow.overflow).toBeLessThanOrEqual(1);
  expect(pageOverflow.overflowY).toBe("hidden");

  await list.hover();
  await page.mouse.wheel(0, 600);
  await expect.poll(() => list.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
  await list.evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await expect
    .poll(() =>
      list.evaluate((el) => getComputedStyle(el).getPropertyValue("--ym-fade-bottom").trim()),
    )
    .toBe("0px");
  await expect
    .poll(() => page.locator(".onboarding").evaluate((el) => el.scrollTop))
    .toBe(0);
  await expect(next).toBeInViewport({ ratio: 1 });
});

test("keeps the model list scrollable at 150% UI scale", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.addInitScript(() => {
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({ onboarded: false, locale: "en", uiScale: 150 }),
    );
    localStorage.setItem("yaomu.onboarding.step", "models");
  });

  await page.goto("/");
  const main = page.locator(".onboarding__main");
  await expect(main).toHaveClass(/onboarding__main--compact/);
  const list = page.locator(".model-list__scroll");
  await expect(list).toBeVisible();
  await expect
    .poll(() => list.evaluate((el) => el.scrollHeight - el.clientHeight))
    .toBeGreaterThan(0);
  await expect
    .poll(() =>
      list.evaluate((el) => getComputedStyle(el).getPropertyValue("--ym-fade-bottom").trim()),
    )
    .not.toBe("0px");
  await expect(
    page.getByRole("button", { name: "Continue", exact: true }),
  ).toBeInViewport({ ratio: 1 });
  const pageOverflow = await page
    .locator(".onboarding")
    .evaluate((el) => el.scrollHeight - el.clientHeight);
  expect(pageOverflow).toBeLessThanOrEqual(1);
});

test("shows manual WSL and driver guidance when checks request it", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: false, locale: "en" }));
    localStorage.setItem("yaomu.onboarding.step", "environment");
    (
      window as Window & {
        __yaomuMock?: {
          environment?: {
            platform: string;
            wsl: { available: boolean };
            devices: Array<{
              id: string;
              name: string;
              kind: string;
              recommended: boolean;
              selected: boolean;
              inUse: boolean;
            }>;
            checks: Array<{
              code: string;
              severity: string;
              messageKey: string;
              actionIds: string[];
            }>;
          };
        };
      }
    ).__yaomuMock = {
      environment: {
        platform: "windows",
        wsl: { available: false },
        devices: [
          {
            id: "cpu",
            name: "CPU",
            kind: "cpu",
            recommended: true,
            selected: true,
            inUse: false,
          },
        ],
        checks: [
          {
            code: "WSL2_AVAILABLE",
            severity: "blocker",
            messageKey: "environment.wsl_unavailable",
            actionIds: ["install_wsl"],
          },
          {
            code: "WSL_CUDA_AVAILABLE",
            severity: "warning",
            messageKey: "environment.wsl_cuda_unavailable",
            actionIds: ["install_gpu_driver"],
          },
        ],
      },
    };
  });

  await page.goto("/");
  await expect(page.getByTestId("env-guide-wsl")).toBeVisible();
  await expect(page.getByTestId("env-guide-driver")).toBeVisible();
  await expect(page.getByText("wsl --install")).toBeVisible();
  await expect(
    page.getByText(/does not run it or change system settings automatically/i),
  ).toBeVisible();
  await expect(
    page.getByText(/does not download or install drivers automatically/i),
  ).toBeVisible();
});

test("downloads all missing models in one confirmation and dedupes shared packages", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: false, locale: "en" }));
    localStorage.setItem("yaomu.onboarding.step", "models");
    const control = ((window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock ??= {});
    control.capabilityStates = {
      asr: "NEEDS_SETUP",
      translate: "NEEDS_SETUP",
      ocr: "NEEDS_SETUP",
      "voice-clone": "NEEDS_SETUP",
      "subtitle-erase": "NEEDS_SETUP",
      separation: "NEEDS_SETUP",
    };
  });

  await page.goto("/");
  // OCR 没有可下载的准入发布：不参与批量下载，单模型下载按钮禁用并说明原因。
  const ocrRow = page.locator(".model").filter({ hasText: "Subtitle extraction" });
  await expect(ocrRow.getByRole("button", { name: "Download", exact: true })).toBeDisabled();
  await expect(
    ocrRow.getByText("No downloadable version is available for this model yet."),
  ).toBeVisible();

  await page.getByRole("button", { name: /Download all missing models \(5\)/ }).click();
  const dialog = page.locator(".ym-dialog").filter({ hasText: "Download all models" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Total download size");
  await expect(dialog).toContainText("Voice clone");
  await dialog.getByRole("button", { name: "Start download" }).click();

  await expect(page.getByText("Started downloading 5 model packages.")).toBeVisible();
  const counts = await page.evaluate(() => {
    const control = (window as Window & { __yaomuMock?: Record<string, number> }).__yaomuMock;
    return { plans: control?.planRequests ?? 0, installs: control?.installRequests ?? 0 };
  });
  expect(counts.plans).toBe(5);
  expect(counts.installs).toBe(5);
});

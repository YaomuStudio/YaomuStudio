import { expect, test, type Page } from "@playwright/test";
import { SAMPLE_SRT, seedMockControl, seedOnboardedSettings } from "./support";

interface PreflightIssueProbe {
  severity: "pass" | "warning" | "blocker";
  code: string;
  messageKey: string;
  actionIds: string[];
}

const MODEL_INSTALLED = "A required model is not installed. Open Model setup, install it, then retry.";
const EXECUTION_UNVERIFIED =
  "The execution configuration could not be verified. Open Model setup, repair the required engines, then retry.";
const CAPABILITY_UNVERIFIED =
  "A required capability is not ready. Check Model setup and the environment, then retry.";
const UNKNOWN_ERROR = "Something went wrong. Check the local logs and retry.";

async function openTranslate(page: Page): Promise<void> {
  await page.goto("/#/tools/translate");
  await expect(page.getByRole("heading", { level: 1, name: "Subtitle translation" })).toBeVisible();
  await page.getByPlaceholder("Project name").fill("E2E preflight project");
  await page.locator('input[type="file"]').setInputFiles(SAMPLE_SRT);
  await expect(page.getByText(SAMPLE_SRT.name)).toBeVisible();
  await page.getByTestId("language-picker-trigger").click();
  await page.getByRole("option", { name: "简体中文", exact: true }).click();
  await page.getByTestId("language-picker-trigger").click();
}

async function setPreflightIssues(page: Page, issues: PreflightIssueProbe[]): Promise<void> {
  await page.evaluate((value) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control.preflightIssues = value;
  }, issues);
}

async function setPreflightError(page: Page, message: string): Promise<void> {
  await page.evaluate((value) => {
    const control = (window as Window & { __yaomuMock?: Record<string, unknown> }).__yaomuMock;
    if (control) control.preflightError = value;
  }, message);
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

test("localizes preflight keys, dedupes identical issues and hides unknown payloads", async ({
  page,
}) => {
  await openTranslate(page);
  await setPreflightIssues(page, [
    // 完全同义：severity/code/key/规范化 actions 相同，只保留一条。
    {
      severity: "blocker",
      code: "MODEL_NOT_INSTALLED",
      messageKey: "preflight.model_not_installed",
      actionIds: ["open_model_setup"],
    },
    {
      severity: "blocker",
      code: "MODEL_NOT_INSTALLED",
      messageKey: "preflight.model_not_installed",
      actionIds: ["open_model_setup", "open_model_setup"],
    },
    // 恢复动作不同：不合并。
    {
      severity: "blocker",
      code: "MODEL_NOT_INSTALLED",
      messageKey: "preflight.model_not_installed",
      actionIds: ["open_model_setup", "retry_later"],
    },
    // 等级不同：warning 与 blocker 不合并。
    {
      severity: "warning",
      code: "MODEL_NOT_INSTALLED",
      messageKey: "preflight.model_not_installed",
      actionIds: ["open_model_setup"],
    },
    // 原先缺失、直接显示原始键的执行配置阻断。
    {
      severity: "blocker",
      code: "EXECUTION_CONFIG_UNVERIFIED",
      messageKey: "preflight.execution_config_unverified",
      actionIds: ["open_model_setup"],
    },
    // 缺 messageKey 但错误码在契约内：按已知错误码回退。
    {
      severity: "blocker",
      code: "CAPABILITY_UNVERIFIED",
      messageKey: "backend.raw_candidate_failure",
      actionIds: [],
    },
    // 未知键与未知码：固定通用回退，绝不展示原始内容。
    {
      severity: "blocker",
      code: "UNKNOWN_SECRET_CODE",
      messageKey: "SECRET_TOKEN_DO_NOT_SHOW",
      actionIds: [],
    },
  ]);

  await page.getByRole("button", { name: "Create" }).click();

  await expect(page.getByText(MODEL_INSTALLED)).toHaveCount(3);
  await expect(page.getByText(EXECUTION_UNVERIFIED)).toHaveCount(1);
  await expect(page.getByText(CAPABILITY_UNVERIFIED)).toHaveCount(1);
  await expect(page.getByText(UNKNOWN_ERROR)).toHaveCount(1);
  await expect(page.locator(".flow-issues li")).toHaveCount(6);
  await expect(page.getByText("preflight.model_not_installed")).toHaveCount(0);
  await expect(page.getByText("backend.raw_candidate_failure")).toHaveCount(0);
  await expect(page.getByText("SECRET_TOKEN_DO_NOT_SHOW")).toHaveCount(0);
  await expect(page.getByText("UNKNOWN_SECRET_CODE")).toHaveCount(0);
});

test("never renders an arbitrary Error.message from a failed request", async ({ page }) => {
  await openTranslate(page);
  await setPreflightError(page, "SECRET raw backend output: token=abc123");
  await page.getByRole("button", { name: "Create" }).click();

  await expect(page.getByText(UNKNOWN_ERROR)).toBeVisible();
  await expect(page.getByText("SECRET raw backend output: token=abc123")).toHaveCount(0);
});

test("re-renders resolved error copy when the locale changes", async ({ page }) => {
  await openTranslate(page);
  await setPreflightIssues(page, [
    {
      severity: "blocker",
      code: "EXECUTION_CONFIG_UNVERIFIED",
      messageKey: "preflight.execution_config_unverified",
      actionIds: ["open_model_setup"],
    },
    {
      severity: "blocker",
      code: "UNKNOWN_SECRET_CODE",
      messageKey: "SECRET_TOKEN_DO_NOT_SHOW",
      actionIds: [],
    },
  ]);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(EXECUTION_UNVERIFIED)).toBeVisible();

  await page.evaluate(async () => {
    const i18n = (await import("/src/i18n/index.ts")) as {
      setLocale: (locale: string) => void;
    };
    i18n.setLocale("zh-CN");
  });

  await expect(
    page.getByText("执行配置无法验证。请打开模型设置，修复所需引擎后重试。"),
  ).toBeVisible();
  await expect(page.getByText("操作失败，请查看本地日志后重试。")).toBeVisible();
  await expect(page.getByText(EXECUTION_UNVERIFIED)).toHaveCount(0);
  await expect(page.getByText("preflight.execution_config_unverified")).toHaveCount(0);
  await expect(page.getByText("SECRET_TOKEN_DO_NOT_SHOW")).toHaveCount(0);
});

test("preflightIssueKey does not collide on separator characters", async ({ page }) => {
  await page.goto("/");

  const keys = await page.evaluate(async () => {
    const mod = (await import("/src/i18n/errorDisplay.ts")) as {
      preflightIssueKey: (issue: {
        severity: "blocker";
        code: string;
        messageKey: string;
        actionIds: string[];
      }) => string;
    };
    const base = { severity: "blocker" as const, actionIds: [] as string[] };
    return {
      actions: [
        mod.preflightIssueKey({ ...base, code: "C", messageKey: "k", actionIds: ["a,b"] }),
        mod.preflightIssueKey({ ...base, code: "C", messageKey: "k", actionIds: ["a", "b"] }),
      ],
      fields: [
        mod.preflightIssueKey({ ...base, code: "A|B", messageKey: "C" }),
        mod.preflightIssueKey({ ...base, code: "A", messageKey: "B|C" }),
      ],
    };
  });

  expect(keys.actions[0]).not.toBe(keys.actions[1]);
  expect(keys.fields[0]).not.toBe(keys.fields[1]);
});

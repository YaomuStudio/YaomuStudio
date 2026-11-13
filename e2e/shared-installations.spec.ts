import { expect, test, type Page } from "@playwright/test";
import {
  completeMockInstallation,
  failMockInstallation,
  readInstallationProbe,
  readMockInstallations,
  seedCapabilityStates,
  seedInstallationControl,
  seedOnboardedSettings,
  setMockInstallationProgress,
} from "./support";

// 只证明 UI 消费 Core 投影：完整覆盖同意、一个物理 ID/进度、取消的共享影响、
// 单槽解绑与旧 Core 兼容。真实字节/事务/并发由 Core + 受控 loopback 用例证明。

function modelCard(page: Page, name: string) {
  return page.locator("article.slot").filter({
    has: page.getByRole("heading", { level: 3, name }),
  });
}

function onboardingRow(page: Page, name: string) {
  return page.locator(".model").filter({ has: page.getByText(name, { exact: true }) });
}

function invalidRangeText(): string {
  return "The download information is incomplete. Open the download confirmation again.";
}

test("one consented plan starts one shared installation and cancel affects both slots", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { tts: "NEEDS_SETUP", "voice-clone": "NEEDS_SETUP" });
  await page.goto("/#/settings/models");

  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  const dialog = page.getByRole("dialog");
  // 语音合成与语音克隆共用同一模型包，UI 只展示语音克隆一个入口。
  await expect(dialog.locator(".plan__covered")).toHaveText("Voice clone");
  await expect(modelCard(page, "Speech synthesis")).toHaveCount(0);
  // 一份物理发布的体积只计一次（不按覆盖槽位翻倍）。
  const downloadRow = dialog.locator(".plan__row").filter({ hasText: "Download size" });
  await expect(downloadRow.locator("dd")).toHaveText("1.9 KB");
  const installRow = dialog.locator(".plan__row").filter({ hasText: "Installed size" });
  await expect(installRow.locator("dd")).toHaveText("1.9 KB");
  await dialog.getByRole("button", { name: "Download and install" }).click();
  await expect(dialog).toHaveCount(0);

  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  const installations = await readMockInstallations(page);
  expect(installations).toHaveLength(1);
  expect(installations[0].coveredSlots).toEqual(["tts", "voice-clone"]);
  expect(installations[0].boundSlots).toEqual(["tts", "voice-clone"]);
  expect(installations[0].currentSlots).toEqual([]);
  const id = String(installations[0].id);

  // 单个可见入口显示物理安装的进度。
  await expect(page.getByText("Download progress")).toHaveCount(1);
  await setMockInstallationProgress(page, id, 42);
  await expect(modelCard(page, "Voice clone").getByText("42%")).toBeVisible();

  // 取消命中同一物理安装 ID。
  await modelCard(page, "Voice clone").getByRole("button", { name: "Cancel download" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).cancelIds).toEqual([id]);
  await expect(page.getByText("Download progress")).toHaveCount(0);
  await expect(
    page.getByText("Cancellation requested; all of these models will be affected."),
  ).toBeVisible();
});

test("completion activates both slots; deleting the initiating slot keeps the other and reuses bytes", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { tts: "NEEDS_SETUP", "voice-clone": "NEEDS_SETUP" });
  await page.goto("/#/settings/models");

  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  const [created] = await readMockInstallations(page);
  const id = String(created.id);

  // Core 完成同一事务激活全部覆盖槽；UI 只按重读的 caps 显示 READY。
  await completeMockInstallation(page, id);
  await expect(modelCard(page, "Voice clone").locator(".slot__head").getByText("Ready", { exact: true })).toBeVisible();
  await expect(modelCard(page, "Speech synthesis")).toHaveCount(0);
  await expect(page.getByText("Download progress")).toHaveCount(0);
  let records = await readMockInstallations(page);
  expect(records).toHaveLength(1);
  expect(records[0].currentSlots).toEqual(["tts", "voice-clone"]);

  // 删除只解绑请求槽；隐藏的共享槽仍保留物理记录/文件。
  await modelCard(page, "Voice clone").getByRole("button", { name: "Delete package" }).click();
  const deleteDialog = page.getByRole("dialog");
  await expect(deleteDialog.getByText("Also used by: Voice clone. Their files are kept.")).toHaveCount(0);
  await deleteDialog.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(
    modelCard(page, "Voice clone").getByText("Needs setup", { exact: true }),
  ).toBeVisible();
  records = await readMockInstallations(page);
  expect(records).toHaveLength(1);
  expect(records[0].boundSlots).toEqual(["tts"]);
  expect(records[0].currentSlots).toEqual(["tts"]);
  await expect(modelCard(page, "Voice clone").getByText("Download progress")).toHaveCount(0);

  // 从解绑槽重新计划：复用已验证实体，不启动第二次物理安装/下载。
  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  const planDialog = page.getByRole("dialog");
  await expect(planDialog.getByText("Existing installation")).toBeVisible();
  await expect(
    planDialog.getByText("Verified files are reused; no new download is needed."),
  ).toBeVisible();
  await expect(
    planDialog.locator(".plan__row").filter({ hasText: "Download size" }).locator("dd"),
  ).toHaveText("0 B");
  await planDialog.getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  await expect(modelCard(page, "Voice clone").locator(".slot__head").getByText("Ready", { exact: true })).toBeVisible();
  records = await readMockInstallations(page);
  expect(records).toHaveLength(1);
  expect([...(records[0].boundSlots ?? [])].sort()).toEqual(["tts", "voice-clone"]);
  expect([...(records[0].currentSlots ?? [])].sort()).toEqual(["tts", "voice-clone"]);
});

test("onboarding shows the full shared scope, one install and shared progress/cancel", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("yaomu.settings", JSON.stringify({ onboarded: false, locale: "en" }));
    localStorage.setItem("yaomu.onboarding.step", "models");
  });
  await seedCapabilityStates(page, { tts: "NEEDS_SETUP", "voice-clone": "NEEDS_SETUP" });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Download model packages" })).toBeVisible();

  await onboardingRow(page, "Voice clone").getByRole("button", { name: "Download" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator(".plan__covered")).toHaveText("Voice clone");
  await dialog.getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  const [inst] = await readMockInstallations(page);
  const id = String(inst.id);

  await expect(page.getByText("This one download covers:")).toHaveCount(0);
  await setMockInstallationProgress(page, id, 37);
  await expect(onboardingRow(page, "Voice clone").getByText("37%")).toBeVisible();
  await expect(onboardingRow(page, "Voice clone").getByText("37%")).toBeVisible();
  await expect(onboardingRow(page, "Voice clone").getByText("Downloading")).toBeVisible();

  await onboardingRow(page, "Voice clone").getByRole("button", { name: "Cancel" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).cancelIds).toEqual([id]);
  // 引导页在步骤面板与页脚各有一个 status 提示，文本相同。
  await expect(
    page
      .getByText(
        "Cancellation requested; all of these models will be affected.",
      )
      .first(),
  ).toBeVisible();
  await expect(page.getByText("This one download covers:")).toHaveCount(0);
});

test("install notice keeps locale-reactive copy without resending the install", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { tts: "NEEDS_SETUP", "voice-clone": "NEEDS_SETUP" });
  await page.goto("/#/settings/models");

  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).installRequests).toBe(1);

  // 共享安装提示只冻结键/身份，不缓存已翻译槽名。
  await expect(
    page
      .getByRole("status")
      .filter({ hasText: "Download started; these models will become available together." }),
  ).toBeVisible();

  await page.evaluate(async () => {
    const i18n = await import("/src/i18n/index.ts");
    i18n.setLocale("zh-CN");
  });
  await expect(
    page
      .getByRole("status")
      .filter({ hasText: "已开始下载，这些模型会一起准备好。" }),
  ).toBeVisible();
  // 切语言只重渲染提示，不重发 install 请求。
  expect((await readInstallationProbe(page)).installRequests).toBe(1);
});

test("bridge rejects malformed coverage and compares against the requested slot", async ({
  page,
}) => {
  await page.goto("/#/");

  const result = await page.evaluate(async () => {
    const { createBridgeApi } = await import("/src/api/bridge.ts");
    const planWire = (overrides: Record<string, unknown>) => ({
      plan_id: "plan-1",
      plan_hash: "hash-1",
      slot: "tts",
      release: { release_id: "r", version: "1.0.0", digest: "sha256:abc" },
      download_bytes: 10,
      cache_reuse_bytes: 0,
      install_bytes: 10,
      permissions: [],
      license: { license_id: "l", license_name: "L" },
      ...overrides,
    });
    const plan = async (overrides: Record<string, unknown>) => {
      const api = createBridgeApi({
        planInstallation: async () => ({ ok: true, data: planWire(overrides) }),
      } as never);
      try {
        const value = await api.planInstallation("tts");
        return {
          ok: true as const,
          slot: value.slot,
          covered: value.coveredSlots ?? null,
          code: "",
        };
      } catch (error) {
        const typed = error as { code?: string; messageKey?: string };
        return {
          ok: false as const,
          slot: "",
          covered: null,
          code: typed.code ?? "",
        };
      }
    };

    const legacy = await plan({});
    const empty = await plan({ covered_slots: [] });
    const nullCoverage = await plan({ covered_slots: null });
    const duplicate = await plan({ covered_slots: ["tts", "tts"] });
    const unknown = await plan({ covered_slots: ["tts", "nope"] });
    const mismatch = await plan({
      slot: "voice-clone",
      covered_slots: ["voice-clone"],
    });
    const valid = await plan({ covered_slots: ["tts", "voice-clone"] });

    const listApi = createBridgeApi({
      getInstallations: async () => ({
        ok: true,
        data: [
          {
            id: "inst-1",
            slot: "tts",
            phase: "downloading",
            progress: 5,
            covered_slots: ["tts"],
            bound_slots: [],
            current_slots: [],
          },
        ],
      }),
    } as never);
    const list = await listApi.getInstallations();
    return {
      legacy,
      empty,
      nullCoverage,
      duplicate,
      unknown,
      mismatch,
      valid,
      bound: list[0]?.boundSlots,
      current: list[0]?.currentSlots,
      covered: list[0]?.coveredSlots,
    };
  });

  // 字段缺失=旧 Core 单槽；显式空/null/重复/未知/请求槽不匹配一律拒绝。
  expect(result.legacy).toMatchObject({ ok: true, slot: "tts", covered: null });
  for (const rejected of [
    result.empty,
    result.nullCoverage,
    result.duplicate,
    result.unknown,
    result.mismatch,
  ]) {
    expect(rejected.ok).toBe(false);
    expect(rejected.code).toBe("INVALID_RESPONSE");
  }
  expect(result.valid).toMatchObject({
    ok: true,
    covered: ["tts", "voice-clone"],
  });
  // 显式空 bound/current 保持空数组，不洗成旧 Core 单槽回退。
  expect(result.bound).toEqual([]);
  expect(result.current).toEqual([]);
  expect(result.covered).toEqual(["tts"]);
});

test("a shared installation failure activates neither slot", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { tts: "NEEDS_SETUP", "voice-clone": "NEEDS_SETUP" });
  await page.goto("/#/settings/models");

  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  const [created] = await readMockInstallations(page);
  const id = String(created.id);

  await failMockInstallation(page, id);
  await expect(page.getByText("Download progress")).toHaveCount(0);
  await expect(modelCard(page, "Voice clone").locator(".slot__head").getByText("Ready", { exact: true })).toHaveCount(0);
  await expect(modelCard(page, "Voice clone").getByText("Needs setup", { exact: true })).toBeVisible();
  await expect(modelCard(page, "Speech synthesis")).toHaveCount(0);
  const records = await readMockInstallations(page);
  expect(records).toHaveLength(1);
  expect(records[0].phase).toBe("failed");
  expect(records[0].currentSlots).toEqual([]);
});

test("old Core without slot projections keeps single-slot consent and progress", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedInstallationControl(page, {
    planCoverage: { "voice-clone": null },
    installationFixtures: [
      {
        id: "inst-legacy",
        slot: "voice-clone",
        phase: "downloading",
        progress: 15,
        planHash: "legacy-plan",
      },
    ],
    capabilityStates: { "voice-clone": "INSTALLING" },
  });
  await page.goto("/#/settings/models");

  // 缺 boundSlots 的旧记录只能按发起槽显示单槽进度。
  await expect(modelCard(page, "Voice clone").getByText("15%")).toBeVisible();

  await modelCard(page, "Voice clone").getByRole("button", { name: "Cancel download" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).cancelIds).toEqual([
    "inst-legacy",
  ]);

  // 计划缺 covered_slots：只按请求槽展示并允许确认，不猜第二槽。
  await modelCard(page, "Voice clone").getByRole("button", { name: "Download model" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator(".plan__covered")).toHaveText("Voice clone");
  await expect(
    dialog.getByText("One download serves all the models listed above; its size is counted once."),
  ).toHaveCount(0);
  await dialog.getByRole("button", { name: "Download and install" }).click();
  await expect.poll(async () => (await readInstallationProbe(page)).starts).toBe(1);
  // 取消历史保留；新记录是新的单槽实体，不与旧记录共用引用。
  const records = await readMockInstallations(page);
  const created = records.find((record) => record.id !== "inst-legacy");
  expect(created?.coveredSlots).toEqual(["voice-clone"]);
  expect(created?.boundSlots).toEqual(["voice-clone"]);
});

test("rejects explicit empty or non-covering ranges before consent", async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedCapabilityStates(page, { "voice-clone": "NEEDS_SETUP" });
  await seedInstallationControl(page, { planCoverage: { "voice-clone": [] } });
  await page.goto("/#/settings/models");

  const card = modelCard(page, "Voice clone");
  await card.getByRole("button", { name: "Download model" }).click();
  let dialog = page.getByRole("dialog");
  await expect(dialog.getByText(invalidRangeText())).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Download and install" })).toBeDisabled();
  expect((await readInstallationProbe(page)).starts).toBe(0);
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();

  // 覆盖范围不包含请求槽同样不能隐藏后让用户同意。
  await page.evaluate(() => {
    const control = (
      window as unknown as {
        __yaomuMock?: { planCoverage?: Record<string, string[]> };
      }
    ).__yaomuMock;
    if (control) control.planCoverage = { "voice-clone": ["tts"] };
  });
  await card.getByRole("button", { name: "Download model" }).click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByText(invalidRangeText())).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Download and install" })).toBeDisabled();
  expect((await readInstallationProbe(page)).starts).toBe(0);
});

test("rollback without a retained version reports inside the triggering card", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/settings/models");

  const card = modelCard(page, "Voice clone");
  await card.getByRole("button", { name: "Roll back" }).click();

  // 反馈必须贴在触发动作的卡片里，而不是页面顶部的全局横幅。
  await expect(card.locator(".slot__feedback--error")).toContainText(
    "Action failed: No previous installed version is available for rollback.",
  );
  await expect(page.locator("p.alert")).toHaveCount(0);
});

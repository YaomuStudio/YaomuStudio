import { expect, test, type Page } from "@playwright/test";
import {
  holdApproveReview,
  readApproveCalls,
  readApproveInputs,
  releaseApproveReview,
  releaseSaveSubtitle,
  seedHeldSaveSubtitle,
  seedMockControl,
  seedOnboardedSettings,
  seedReviewFixtures,
  seedReviewReadFailure,
  seedReviewScenario,
  seedSaveSubtitleFailure,
  type ReviewGateFixture,
} from "./support";

// 浏览器 mock 只证明 UI 控制面：未保存/保存中/非法时不把本地草稿误当已审批，
// 旧/未知 current_bindings 不提供放行动作，多 pending 逐条批准且不显示总体已审。
// 真实 Core 的摘要绑定、批准事务与导出授权由 Go 用例和真实 Electron 夹具另行覆盖。
test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

const approveButtons = (page: Page) =>
  page.getByRole("button", { name: "Approve", exact: true });

const saveButton = (page: Page) =>
  page.getByRole("button", { name: "Save corrections", exact: true });

const confirmLabel =
  "I confirm the translation, voice selection and subtitle style";

const confirmCheckbox = (page: Page) =>
  page.getByRole("checkbox", { name: confirmLabel });

const DEMO_RUN = "run-demo-1";
const DEMO_REVISION = "rev-demo-en-1";

const gateCard = (page: Page, reviewId: string) =>
  page.locator(`[data-testid="review-gate"][data-review-id="${reviewId}"]`);

const deliveryAggregate = (page: Page) =>
  page.getByTestId("delivery-review-aggregate");

async function openDelivery(page: Page): Promise<void> {
  await page.goto("/#/pipeline?project=proj-demo");
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();
  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(page.getByRole("heading", { name: "Delivery review" })).toBeVisible();
}

async function editFirstSegment(page: Page, text: string): Promise<void> {
  const first = page.getByTestId("segment-row").first();
  await first.locator("textarea").fill(text);
  await expect(page.getByText("Unsaved changes", { exact: true })).toBeVisible();
}

async function selectExportLanguage(page: Page, language: string): Promise<void> {
  const field = page.locator(".ym-field").filter({ hasText: "Export language" });
  await field.locator(".ym-select__trigger").click();
  await page.locator(`.ym-select__option[data-value="${language}"]`).click();
}

/** 默认 demo：1 个 translation + mix/字幕 revision 两个 delivery 当前门槛。 */
test("blocks approval and delivery confirmation while corrections are unsaved", async ({
  page,
}) => {
  await openDelivery(page);
  // 基线：proj-demo 预置 translation + 两个 delivery 当前 pending 门槛，可正常放行。
  await expect(approveButtons(page)).toHaveCount(3);
  await expect(approveButtons(page).first()).toBeEnabled();

  await page.getByRole("tab", { name: "Subtitle correction", exact: true }).click();
  await editFirstSegment(page, "Unsaved draft line");
  await page.getByRole("tab", { name: "Delivery & export" }).click();

  // dirty：全部审核按钮、交付确认与导出都被同一原因阻断，且不发批准请求。
  await expect(approveButtons(page)).toHaveCount(3);
  for (const index of [0, 1, 2]) {
    await expect(approveButtons(page).nth(index)).toBeDisabled();
  }
  await expect(page.locator(".review-gate__blocked")).toHaveText(
    "Save subtitle corrections before review or export",
  );
  await expect(page.locator(".delivery__blocked-reason")).toHaveText(
    "Save subtitle corrections before review or export",
  );
  await expect(page.locator(".export__blocked")).toHaveText(
    "Save subtitle corrections before review or export",
  );
  const confirm = confirmCheckbox(page);
  await expect(confirm).toHaveCount(1);
  await expect(confirm).toBeDisabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);

  // 保存成功后解除阻断；视频导出确认仍只放行 final mix 这一条当前交付门槛。
  await page.getByRole("tab", { name: "Subtitle correction", exact: true }).click();
  await saveButton(page).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(approveButtons(page).first()).toBeEnabled();
  await expect(confirm).toBeEnabled();
  await page.getByText(confirmLabel, { exact: true }).click();
  // 视频导出只要求 final mix 已放行；字幕 revision 门槛仍 pending，但不阻塞视频导出。
  await expect(confirm).toBeChecked();
  await expect(page.getByRole("button", { name: "Start export" })).toBeEnabled();
  await expect.poll(() => readApproveCalls(page)).toBe(1);
  const sent = await readApproveInputs(page);
  expect(sent[0]?.currentBinding).toEqual({
    language: "en",
    sourceKind: "delivery_mix",
    sourceId: `task-mix:${DEMO_RUN}:en`,
  });
});

test("blocks approval while a timestamp draft is invalid", async ({ page }) => {
  await page.goto("/#/pipeline?project=proj-demo");

  const first = page.getByTestId("segment-row").first();
  await first.locator("input").first().fill("not-a-timestamp");
  await expect(first.getByText("Invalid timestamp format")).toBeVisible();

  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(approveButtons(page).first()).toBeDisabled();
  await expect(page.locator(".review-gate__blocked")).toHaveText(
    "Fix timestamp issues before review or export",
  );
  await expect(confirmCheckbox(page)).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("allows revoking a confirmed delivery checkbox while blocked", async ({ page }) => {
  // 浏览器 mock 只证明 UI 撤销路径：已确认时阻断不锁死取消，且撤销不发批准请求；
  // Core 的确认/摘要绑定语义由 Go 用例和真实 Electron 夹具另行覆盖。
  // 单条交付门槛让“确认 = 总体已审”，专注撤销行为。
  await seedReviewFixtures(page, [
    {
      id: "delivery-mix-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-en",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" },
      ],
    },
  ]);
  await openDelivery(page);
  const confirm = confirmCheckbox(page);
  await page.getByText(confirmLabel, { exact: true }).click();
  await expect(confirm).toBeChecked();
  await expect.poll(() => readApproveCalls(page)).toBe(1);

  // 非法时间戳使审核阻断但不产生 dirty，已确认状态保留，取消必须仍可用。
  await page.getByRole("tab", { name: "Subtitle correction", exact: true }).click();
  const first = page.getByTestId("segment-row").first();
  await first.locator("input").first().fill("not-a-timestamp");
  await expect(first.getByText("Invalid timestamp format")).toBeVisible();
  await page.getByRole("tab", { name: "Delivery & export" }).click();

  await expect(page.locator(".delivery__blocked-reason")).toHaveText(
    "Fix timestamp issues before review or export",
  );
  await expect(confirm).toBeChecked();
  await expect(confirm).toBeEnabled();
  await page.getByText(confirmLabel, { exact: true }).click();
  await expect(confirm).not.toBeChecked();
  expect(await readApproveCalls(page)).toBe(1);
});

test("blocks approval while a save is in flight and restores after it completes", async ({
  page,
}) => {
  await seedHeldSaveSubtitle(page);
  await page.goto("/#/pipeline?project=proj-demo");

  await editFirstSegment(page, "Held save draft");
  await saveButton(page).click();

  // 保存挂起期间不能放行旧持久 revision，也不能确认交付。
  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(page.locator(".review-gate__blocked")).toHaveText(
    "Wait for the current operation to finish",
  );
  await expect(approveButtons(page).first()).toBeDisabled();
  await expect(confirmCheckbox(page)).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);

  await releaseSaveSubtitle(page);
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toBeVisible();
  await expect(approveButtons(page).first()).toBeEnabled();
});

test("keeps the draft and approval block when saving hits a revision conflict", async ({
  page,
}) => {
  await seedSaveSubtitleFailure(page, "conflict");
  await page.goto("/#/pipeline?project=proj-demo");

  await editFirstSegment(page, "Conflict draft");
  await saveButton(page).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "The content changed; reload before saving." }),
  ).toBeVisible();

  // 冲突不覆盖草稿；dirty 继续阻断审核与交付确认。
  await expect(page.getByTestId("segment-row").first().locator("textarea")).toHaveValue(
    "Conflict draft",
  );
  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(approveButtons(page).first()).toBeDisabled();
  await expect(confirmCheckbox(page)).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("selects the current gate by binding instead of list order", async ({ page }) => {
  // 旧记录故意排在前面：旧 revision、旧 `摘要:语言` 后缀、空 language 多语种门槛
  // 都只有显式 currentBindings 才可操作，列表顺序和 gate.language 不作为当前依据。
  const fixtures: ReviewGateFixture[] = [
    {
      id: "a-translation-en-old",
      runId: DEMO_RUN,
      type: "translation",
      language: "en",
      revisionDigest: "old-revision-digest",
      status: "pending",
      currentBindings: [],
    },
    {
      id: "b-suffixed-en",
      runId: DEMO_RUN,
      type: "translation",
      language: "en",
      revisionDigest: `${DEMO_REVISION}:en`,
      status: "pending",
      currentBindings: [],
    },
    {
      id: "z-translation-en-current",
      runId: DEMO_RUN,
      type: "translation",
      language: "en",
      revisionDigest: DEMO_REVISION,
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "translation_revision", sourceId: DEMO_REVISION },
      ],
    },
    {
      id: "delivery-mix-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-en",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" },
      ],
    },
    {
      id: "delivery-mix-ja",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-ja",
      status: "pending",
      currentBindings: [
        { language: "ja", sourceKind: "delivery_mix", sourceId: "task-mix-ja" },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await seedReviewScenario(page, { languages: ["en", "ja"] });
  await openDelivery(page);

  // 只有 z-translation-en-current 有当前绑定；旧记录不渲染可操作卡片。
  const currentTranslation = gateCard(page, "z-translation-en-current");
  await expect(currentTranslation).toBeVisible();
  await expect(gateCard(page, "a-translation-en-old")).toHaveCount(0);
  await expect(gateCard(page, "b-suffixed-en")).toHaveCount(0);
  await expect(page.getByTestId("review-history")).toBeVisible();

  await currentTranslation.getByRole("button", { name: "Approve" }).click();
  await expect
    .poll(async () => (await readApproveInputs(page))[0]?.currentBinding)
    .toEqual({
      language: "en",
      sourceKind: "translation_revision",
      sourceId: DEMO_REVISION,
    });

  // 空 language 的两个 delivery 门槛按绑定语种选择：默认 en 只出现 en 当前卡片。
  await expect(gateCard(page, "delivery-mix-en")).toBeVisible();
  await expect(gateCard(page, "delivery-mix-ja")).toHaveCount(0);
  await selectExportLanguage(page, "ja");
  await expect(gateCard(page, "delivery-mix-ja")).toBeVisible();
  await expect(gateCard(page, "delivery-mix-en")).toHaveCount(0);

  await gateCard(page, "delivery-mix-ja").getByRole("button", { name: "Approve" }).click();
  await expect
    .poll(async () => (await readApproveInputs(page)).at(-1)?.currentBinding)
    .toEqual({ language: "ja", sourceKind: "delivery_mix", sourceId: "task-mix-ja" });
});

test("keeps every current pending delivery review individually actionable", async ({ page }) => {
  const fixtures: ReviewGateFixture[] = [
    {
      id: "delivery-mix-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-en",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" },
      ],
    },
    {
      id: "delivery-sub-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:rev-demo-en-1",
      status: "pending",
      currentBindings: [
        {
          language: "en",
          sourceKind: "delivery_revision",
          sourceId: "rev-demo-en-1",
        },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await openDelivery(page);

  await expect(gateCard(page, "delivery-mix-en")).toBeVisible();
  await expect(gateCard(page, "delivery-sub-en")).toBeVisible();
  await expect(deliveryAggregate(page)).toHaveText(
    "Some current delivery reviews are still pending.",
  );

  await gateCard(page, "delivery-mix-en").getByRole("button", { name: "Approve" }).click();
  await expect
    .poll(async () => (await readApproveInputs(page))[0]?.currentBinding)
    .toEqual({ language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" });
  // 放行一条不代表总体已审：另一条当前 pending 仍必须逐条操作，但不阻塞已选导出。
  await expect(deliveryAggregate(page)).toHaveText(
    "Other current delivery reviews are still pending; they do not block the selected export.",
  );

  await gateCard(page, "delivery-sub-en").getByRole("button", { name: "Approve" }).click();
  await expect
    .poll(async () => (await readApproveInputs(page)).at(-1)?.currentBinding)
    .toEqual({
      language: "en",
      sourceKind: "delivery_revision",
      sourceId: "rev-demo-en-1",
    });
  await expect(deliveryAggregate(page)).toHaveText("All current delivery reviews are approved.");
  await expect(confirmCheckbox(page)).toBeEnabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeEnabled();
});

test("video export only waits for the final mix review", async ({
  page,
}) => {
  const fixtures: ReviewGateFixture[] = [
    {
      id: "delivery-mix-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-en",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" },
      ],
    },
    {
      id: "delivery-sub-en",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:rev-demo-en-1",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_revision", sourceId: "rev-demo-en-1" },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await openDelivery(page);

  // 确认框只放行主门槛（mix）；字幕 revision 门槛仍 pending，但不阻塞视频导出。
  await page.getByText(confirmLabel, { exact: true }).click();
  await expect.poll(() => readApproveCalls(page)).toBe(1);
  await expect(deliveryAggregate(page)).toHaveText(
    "Other current delivery reviews are still pending; they do not block the selected export.",
  );
  await expect(confirmCheckbox(page)).toBeChecked();
  await expect(confirmCheckbox(page)).toBeEnabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeEnabled();

  // 逐条批准剩余门槛后，总体聚合恢复为已审。
  await gateCard(page, "delivery-sub-en").getByRole("button", { name: "Approve" }).click();
  await expect(deliveryAggregate(page)).toHaveText("All current delivery reviews are approved.");
  await expect(confirmCheckbox(page)).toBeChecked();
  await expect(page.getByRole("button", { name: "Start export" })).toBeEnabled();
});

test("keeps a non-primary delivery row approval in the current context", async ({ page }) => {
  // 同语种多条不同 source 的当前交付字幕门槛：被点击行不是导出主轨时，
  // 旧 responses guard 不能按主轨 revision 误判为过期；其他 current track
  // 可逐条批准，但不阻塞本次已审导出（与 Core currentDeliveryDigests 口径一致）。
  const fixtures: ReviewGateFixture[] = [
    {
      id: "delivery-sub-primary",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:rev-demo-en-1",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_revision", sourceId: "rev-demo-en-1" },
      ],
    },
    {
      id: "delivery-sub-secondary",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:rev-demo-en-second",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_revision", sourceId: "rev-demo-en-second" },
      ],
    },
    {
      id: "delivery-sub-other",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:rev-demo-en-other",
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "delivery_revision", sourceId: "rev-demo-en-other" },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await openDelivery(page);
  await page.getByRole("tab", { name: "Subtitles" }).click();

  const secondary = gateCard(page, "delivery-sub-secondary");
  await secondary.getByRole("button", { name: "Approve" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Review approved" }),
  ).toBeVisible();
  await expect.poll(() => readApproveInputs(page)).toEqual([
    {
      reviewId: "delivery-sub-secondary",
      revisionDigest: "delivery:rev-demo-en-second",
      type: "delivery",
      currentBinding: {
        language: "en",
        sourceKind: "delivery_revision",
        sourceId: "rev-demo-en-second",
      },
    },
  ]);
  // 列表及时刷新：非主行变 approved，主行仍 pending，且没有重复批准。
  await expect(secondary.getByText("Approved", { exact: true })).toBeVisible();
  await expect(secondary.getByRole("button", { name: "Approve" })).toHaveCount(0);
  await expect(
    gateCard(page, "delivery-sub-primary").getByText("Pending", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(1);

  // 放行实际选中的字幕 revision 后：本次导出已审，其他 current track 仍待放行但不阻塞。
  await gateCard(page, "delivery-sub-primary").getByRole("button", { name: "Approve" }).click();
  const aggregate = deliveryAggregate(page);
  await expect(aggregate).toHaveAttribute("data-relevant-aggregate", "approved");
  await expect(aggregate).toHaveText(
    "Other current delivery reviews are still pending; they do not block the selected export.",
  );
  await expect(confirmCheckbox(page)).toBeChecked();
  await expect(page.getByRole("button", { name: "Start export" })).toBeEnabled();
  await expect(gateCard(page, "delivery-sub-other").getByRole("button", { name: "Approve" })).toBeVisible();
});

test("does not treat a run without a current delivery review as locally confirmed", async ({
  page,
}) => {
  await seedReviewFixtures(page, []);
  await openDelivery(page);

  await expect(page.getByTestId("delivery-review-hint")).toHaveText(
    "The delivery review has not been created yet; wait for it to complete before confirming or exporting.",
  );
  await expect(confirmCheckbox(page)).toBeDisabled();
  await expect(page.locator(".export__blocked")).toHaveText(
    "The delivery review has not been created yet; wait for it to complete before confirming or exporting.",
  );
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("does not treat a failed review read as no review gate", async ({ page }) => {
  await seedReviewReadFailure(page);
  await openDelivery(page);

  await expect(page.getByTestId("delivery-review-hint")).toHaveText(
    "Review status is unavailable because the review list could not be read. Retry before confirming or exporting.",
  );
  await expect(confirmCheckbox(page)).toBeDisabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("does not carry a delivery confirmation across export languages", async ({ page }) => {
  await seedReviewScenario(page, { languages: ["en", "ja"] });
  await openDelivery(page);

  await page.getByText(confirmLabel, { exact: true }).click();
  // 默认 demo 同时有 mix 与字幕 revision 当前门槛；逐条放行后才恢复总体确认。
  await page
    .locator('[data-testid="review-gate"][data-source-kind="delivery_revision"]')
    .getByRole("button", { name: "Approve" })
    .click();
  await expect(confirmCheckbox(page)).toBeChecked();
  const exportButton = page.getByRole("button", { name: "Start export" });
  await expect(exportButton).toBeEnabled();

  // 切到 ja 后旧确认不适用：默认 mix 门槛只投影到 en，ja 需要重新显式确认。
  await selectExportLanguage(page, "ja");
  await expect(confirmCheckbox(page)).not.toBeChecked();
  await expect(exportButton).toBeDisabled();
});

test("fails closed when the Core does not report current bindings", async ({ page }) => {
  // 旧服务缺省 current_bindings：不能拿列表第一条或旧 approved 猜当前门槛。
  const fixtures: ReviewGateFixture[] = [
    {
      id: "legacy-translation",
      runId: DEMO_RUN,
      type: "translation",
      language: "en",
      revisionDigest: "legacy",
      status: "pending",
    },
    {
      id: "legacy-delivery",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "legacy-delivery",
      status: "pending",
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await openDelivery(page);

  await expect(approveButtons(page)).toHaveCount(0);
  await expect(page.getByTestId("review-projection-unknown")).toBeVisible();
  await expect(confirmCheckbox(page)).toBeDisabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("does not guess when the selected export has no matching current binding", async ({
  page,
}) => {
  // 当前选择的字幕 revision 与 Core 投影不一致：提示而不是拿别的 gate 放行。
  const fixtures: ReviewGateFixture[] = [
    {
      id: "delivery-sub-other",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "delivery:other-revision",
      status: "pending",
      currentBindings: [
        {
          language: "en",
          sourceKind: "delivery_revision",
          sourceId: "rev-demo-en-other",
        },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await openDelivery(page);
  await page.getByRole("tab", { name: "Subtitles" }).click();

  await expect(page.getByTestId("delivery-unmatched-hint")).toBeVisible();
  const confirm = confirmCheckbox(page);
  await expect(confirm).toBeDisabled();
  await expect(page.getByRole("button", { name: "Start export" })).toBeDisabled();
  expect(await readApproveCalls(page)).toBe(0);
});

test("allows approval records on a paused run", async ({ page }) => {
  await seedReviewScenario(page, { runStatus: "PAUSED" });
  await openDelivery(page);

  const translation = gateCard(page, "rev-trans-en");
  await expect(translation.getByRole("button", { name: "Approve" })).toBeEnabled();
  await translation.getByRole("button", { name: "Approve" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Review approved" }),
  ).toBeVisible();
  expect(await readApproveCalls(page)).toBe(1);
});

test("denies new approvals on a terminal run but keeps reviewed export available", async ({
  page,
}) => {
  const fixtures: ReviewGateFixture[] = [
    {
      id: "terminal-translation",
      runId: DEMO_RUN,
      type: "translation",
      language: "en",
      revisionDigest: DEMO_REVISION,
      status: "pending",
      currentBindings: [
        { language: "en", sourceKind: "translation_revision", sourceId: DEMO_REVISION },
      ],
    },
    {
      id: "terminal-delivery-approved",
      runId: DEMO_RUN,
      type: "delivery",
      revisionDigest: "mix-en",
      status: "approved",
      confirmedAt: "2026-11-08T00:00:00.000Z",
      currentBindings: [
        { language: "en", sourceKind: "delivery_mix", sourceId: "task-mix-en" },
      ],
    },
  ];
  await seedReviewFixtures(page, fixtures);
  await seedReviewScenario(page, { runStatus: "SUCCEEDED" });
  await openDelivery(page);

  await expect(
    gateCard(page, "terminal-translation").getByRole("button", { name: "Approve" }),
  ).toBeDisabled();
  await expect(
    page
      .locator(".review-gate__hint")
      .filter({ hasText: "This workflow has finished" }),
  ).toBeVisible();
  expect(await readApproveCalls(page)).toBe(0);

  // 已审核的终态导出例外保留：确认可恢复且导出仍可用。
  const exportButton = page.getByRole("button", { name: "Start export" });
  await expect(exportButton).toBeEnabled();
  await exportButton.click();
  await expect(page.getByRole("status").filter({ hasText: "Export queued" })).toBeVisible();
});

test("does not apply an old approval response after the visible revision changes", async ({
  page,
}) => {
  await page.goto("/#/pipeline?project=proj-demo");
  await expect(page.getByRole("heading", { level: 1, name: "Full Pipeline" })).toBeVisible();
  await expect(approveButtons(page)).toHaveCount(3);

  await holdApproveReview(page);
  await approveButtons(page).first().click();
  await expect.poll(() => readApproveCalls(page)).toBe(1);

  // 批准回包未到前保存新 revision：旧摘要不再对应当前可见内容。
  await editFirstSegment(page, "Revision moved before approval");
  await saveButton(page).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Corrections saved" }),
  ).toBeVisible();

  await releaseApproveReview(page);
  // 旧回包被丢弃：不显示成功提示、不恢复交付确认、不覆盖保存后的门槛列表。
  await expect(
    page.getByRole("status").filter({ hasText: "Review approved" }),
  ).toHaveCount(0);
  await page.getByRole("tab", { name: "Delivery & export" }).click();
  await expect(confirmCheckbox(page)).not.toBeChecked();
  expect(await readApproveCalls(page)).toBe(1);
});

test("shows a refresh-required notice when the post-approval re-read fails", async ({ page }) => {
  await seedReviewScenario(page, { refreshFailure: true });
  await openDelivery(page);

  await approveButtons(page).first().click();
  await expect(
    page.getByRole("status").filter({
      hasText:
        "Review recorded, but the review list could not be refreshed. Reload before exporting.",
    }),
  ).toBeVisible();
  // 重读失败不能假装当前内容已审。
  await expect(confirmCheckbox(page)).not.toBeChecked();
  expect(await readApproveCalls(page)).toBe(1);
});

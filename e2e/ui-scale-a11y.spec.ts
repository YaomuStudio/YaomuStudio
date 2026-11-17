import { expect, test, type Page } from "@playwright/test";
import { AxeBuilder } from "@axe-core/playwright";
import {
  releaseSaveSubtitle,
  seedHeldSaveSubtitle,
  seedMockControl,
  seedOnboardedSettings,
  seedSaveSubtitleFailure,
} from "./support";

// 浏览器 E2E 只证明渲染/交互结构；真实 Electron/Core 由 .design-build 下的隔离脚本验证。
// 本文件覆盖 ui-scale-a11y 的键盘/对话框子范围：Escape/焦点返回、行键盘选择、
// textarea/IME 不拦截、Ctrl/Cmd+S 单一保存入口与页面离开清理。

const saveActions = (page: Page) =>
  page.getByRole("button", { name: /^Save( corrections)?$/ });

interface SaveProbe {
  calls: number;
  revision: { segments: { id: string; text: string }[] } | null;
}

async function readSaveProbe(page: Page): Promise<SaveProbe> {
  return page.evaluate(() => {
    const control = (
      window as Window & {
        __yaomuMock?: {
          saveSubtitleCalls?: number;
          lastSavedRevision?: { segments: { id: string; text: string }[] };
        };
      }
    ).__yaomuMock;
    return {
      calls: control?.saveSubtitleCalls ?? 0,
      revision: control?.lastSavedRevision ?? null,
    };
  });
}

async function openCorrection(page: Page): Promise<void> {
  await page.goto("/#/projects/proj-demo/correction");
  await expect(page.getByText("星际译制 · 试播集")).toBeVisible();
  await expect(page.getByTestId("segment-row").first()).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
  await seedMockControl(page);
});

/** 预置中文/暗色/指定界面缩放的已引导设置；后注册覆盖 beforeEach 的英文默认值。 */
async function seedScaledSettings(page: Page, uiScale: number): Promise<void> {
  await page.addInitScript((scale) => {
    localStorage.setItem(
      "yaomu.settings",
      JSON.stringify({
        onboarded: true,
        locale: "zh-CN",
        theme: "dark",
        uiScale: scale,
        autoCheckUpdates: false,
      }),
    );
    localStorage.setItem("yaomu.sidebar.collapsed", "0");
  }, uiScale);
}

interface ScaledLayoutProbe {
  bodyColumns: string;
  panelClientWidth: number;
  panelScrollWidth: number;
  textareaClientWidth: number;
  tabLineTops: number[];
  stackedVertically: boolean;
  saveVisible: boolean;
  viewportOverflow: boolean;
}

/** 读取校正页在真实 CSS zoom 后的可用宽度、tab 行数与编辑区宽度。 */
async function readScaledLayout(page: Page): Promise<ScaledLayoutProbe> {
  return page.evaluate(() => {
    const body = document.querySelector(".mcs-body") as HTMLElement | null;
    const panel = document.querySelector(".mcs-panel") as HTMLElement | null;
    const media = document.querySelector(".mcs-stage") as HTMLElement | null;
    const textarea = document.querySelector(
      '[data-testid="segment-row"] [data-testid="segment-translation"]',
    ) as HTMLTextAreaElement | null;
    const tabs = Array.from(document.querySelectorAll(".mcs-tabs button")) as HTMLElement[];
    const save = document.querySelector(".mcs-top__actions button");
    const distinctLineTops = (element: HTMLElement): number => {
      const range = document.createRange();
      range.selectNodeContents(element);
      const rects = Array.from(range.getClientRects()).filter((rect) => rect.width > 0);
      return new Set(rects.map((rect) => Math.round(rect.top))).size;
    };
    const saveRect = save?.getBoundingClientRect();
    if (!body || !panel || !media || !textarea) {
      throw new Error("correction layout not mounted");
    }
    return {
      bodyColumns: getComputedStyle(body).gridTemplateColumns,
      panelClientWidth: panel.clientWidth,
      panelScrollWidth: panel.scrollWidth,
      textareaClientWidth: textarea.clientWidth,
      tabLineTops: tabs.map(distinctLineTops),
      // 单列堆叠后舞台在上、面板在下，保持 DOM 顺序。
      stackedVertically:
        panel.getBoundingClientRect().top >= media.getBoundingClientRect().bottom - 1,
      saveVisible: !!saveRect && saveRect.width > 0 && saveRect.height > 0,
      viewportOverflow:
        document.documentElement.scrollWidth > document.documentElement.clientWidth,
    };
  });
}

test("stacks the correction columns by available width at 150% UI scale", async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 700 });
  await seedScaledSettings(page, 150);
  await openCorrection(page);
  await expect
    .poll(() => page.evaluate(() => getComputedStyle(document.documentElement).zoom))
    .toBe("1.5");
  await expect(page.locator(".mcs-panel")).toBeVisible();

  const layout = await readScaledLayout(page);
  // 单列：容器查询按实际可用宽度触发，而不是按不感知 zoom 的 layout viewport。
  expect(layout.bodyColumns.trim()).not.toContain(" ");
  expect(layout.panelClientWidth).toBeGreaterThanOrEqual(360);
  expect(layout.textareaClientWidth).toBeGreaterThanOrEqual(320);
  // tab 文案保持一行（不再逐字纵排），面板无横向裁切。本页固定三个页签。
  expect(layout.tabLineTops).toHaveLength(3);
  expect(layout.tabLineTops.every((tops) => tops === 1)).toBe(true);
  expect(layout.panelScrollWidth).toBeLessThanOrEqual(layout.panelClientWidth + 1);
  // 媒体与编辑器纵向堆叠（保持 DOM 顺序），保存栏仍然可见；页面本身没有横向溢出。
  expect(layout.stackedVertically).toBe(true);
  expect(layout.saveVisible).toBe(true);
  expect(layout.viewportOverflow).toBe(false);

  // tab 语义不变：切换到字幕样式再切回内容校正。
  await page.getByRole("tab", { name: "字幕样式" }).click();
  await expect(page.locator(".style-panel")).toBeVisible();
  await expect(page.getByRole("tab", { name: "字幕样式" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.getByRole("tab", { name: "内容校正" }).click();
  await expect(page.locator(".subpanel")).toBeVisible();
});

test("keeps the two-column correction layout at 100% UI scale", async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 700 });
  await seedScaledSettings(page, 100);
  await openCorrection(page);
  await expect
    .poll(() => page.evaluate(() => getComputedStyle(document.documentElement).zoom))
    .toBe("1");

  const layout = await readScaledLayout(page);
  expect(layout.bodyColumns.trim().split(/\s+/)).toHaveLength(2);
  expect(layout.panelClientWidth).toBeGreaterThanOrEqual(360);
  expect(layout.textareaClientWidth).toBeGreaterThanOrEqual(320);
  expect(layout.viewportOverflow).toBe(false);
  expect(layout.saveVisible).toBe(true);
});

test("closes a dialog with Escape and returns focus to its trigger", async ({ page }) => {
  await page.goto("/#/projects");
  const card = page.locator("article.recent-card").filter({ hasText: "星际译制 · 试播集" });
  const trigger = card.getByRole("button", { name: "Download", exact: true });
  await trigger.focus();
  await trigger.click();

  const dialog = page.getByRole("dialog").filter({ hasText: "Download files" });
  await expect(dialog).toBeVisible();
  // 打开后焦点进入对话框的首个可聚焦元素（下载行按钮），而不是留在触发按钮上。
  await expect(dialog.locator("button.download-row").first()).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();

  // 按钮关闭语义保持，同样返回触发元素。
  await trigger.click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
});

test("selects rows with arrow keys and completes edit, candidate and save flow", async ({
  page,
}) => {
  await openCorrection(page);
  const rows = page.getByTestId("segment-row");
  await expect(rows).toHaveCount(8);

  // 行有可发现的 roving tabindex：第一个被选中行进入 Tab 序。
  await rows.first().focus();
  await expect(rows.first()).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(rows.nth(1)).toBeFocused();
  await expect(rows.nth(1)).toHaveClass(/is-selected/);

  await page.keyboard.press("End");
  await expect(rows.nth(7)).toBeFocused();
  await expect(rows.nth(7)).toHaveClass(/is-selected/);
  await page.keyboard.press("Home");
  await expect(rows.first()).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(rows.first()).toBeFocused();

  await page.keyboard.press("ArrowDown");
  const secondRow = rows.nth(1);
  const textarea = secondRow.getByTestId("segment-translation");
  await textarea.focus();
  await textarea.fill("");
  await page.keyboard.type("Keyboard edit");

  // 行内候选：展开编辑行，生成并选择一个候选；选中句保持与编辑行一致。
  await secondRow.getByTestId("candidate-toggle").click();
  const inline = page.getByTestId("inline-candidates");
  await expect(inline).toBeVisible();
  await expect(secondRow).toHaveClass(/is-selected/);

  await inline
    .getByRole("button", { name: /Generate candidates|Regenerate this sentence/ })
    .click();
  const select = inline.getByRole("button", { name: "Select", exact: true }).first();
  await expect(select).toBeVisible({ timeout: 10_000 });
  await select.click();
  await expect(
    page.getByRole("status").filter({ hasText: "Candidate selected" }),
  ).toBeVisible();

  // 页面可保存时 Ctrl/Cmd+S 走既有保存入口；可见保存按钮仍保留。
  await page.keyboard.press("ControlOrMeta+s");
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  await expect(saveActions(page)).toHaveCount(1);

  const probe = await readSaveProbe(page);
  expect(probe.calls).toBe(1);
  expect(probe.revision?.segments.find((segment) => segment.id === "seg-102")?.text).toBe(
    "Keyboard edit",
  );
});

test("keeps native textarea, Space and IME composition behavior", async ({ page }) => {
  await openCorrection(page);
  const rows = page.getByTestId("segment-row");
  const firstRow = rows.first();
  await firstRow.focus();
  await expect(firstRow).toBeFocused();

  const textarea = firstRow.getByTestId("segment-translation");
  await textarea.click();
  await textarea.fill("ab");
  await page.keyboard.press("End");
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.type("X");
  await expect(textarea).toHaveValue("aXb");
  await page.keyboard.type(" ");
  await expect(textarea).toHaveValue("aX b");

  // textarea 内的方向键不改变行选择。
  await page.keyboard.press("ArrowDown");
  await expect(firstRow).toHaveClass(/is-selected/);
  await expect(rows.nth(1)).not.toHaveClass(/is-selected/);

  // IME composition 中的方向键不切换行。
  await firstRow.evaluate((element) => {
    element.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, isComposing: true }),
    );
  });
  await expect(firstRow).toHaveClass(/is-selected/);

  // 行元素本身聚焦时方向键仍可导航。
  await firstRow.focus();
  await page.keyboard.press("ArrowDown");
  await expect(rows.nth(1)).toBeFocused();
});

test("editing one row does not re-render its neighboring rows", async ({ page }) => {
  await openCorrection(page);
  const rows = page.getByTestId("segment-row");
  await expect(rows).toHaveCount(8);

  // keyed v-for 即使全量 patch 也会复用 DOM 元素，不能用元素身份发现回归；
  // Vite dev 下 Vue 会在 patch 时更新 element.__vnode，memo 命中时 patch 直接返回，
  // 引用保持不变。因此同时记录 DOM 元素与 vnode 引用：
  // - 相邻元素/ vnode 必须保持原引用（未重新渲染）；
  // - 被编辑行的 vnode 必须变化（证明断言对全量渲染敏感）。
  // 先聚焦并选中编辑行，再记录基线：点击本身会更新行选中状态，属于预期渲染。
  const textarea = rows.nth(1).getByTestId("segment-translation");
  await textarea.click();
  await textarea.evaluate((element) => {
    const area = element as HTMLTextAreaElement;
    area.setSelectionRange(area.value.length, area.value.length);
  });

  const before = await page.evaluate(() => {
    const elements = Array.from(
      document.querySelectorAll<HTMLElement>('[data-testid="segment-row"]'),
    ).slice(0, 3);
    const probe = {
      elements,
      vnodes: elements.map(
        (element) => (element as HTMLElement & { __vnode?: unknown }).__vnode,
      ),
      values: elements.map((element) => element.querySelector('[data-testid="segment-translation"]')?.value ?? ""),
    };
    (
      window as Window & {
        __rowIsolationProbe?: {
          elements: HTMLElement[];
          vnodes: unknown[];
          values: string[];
        };
      }
    ).__rowIsolationProbe = probe;
    return { vnodeAvailable: probe.vnodes.every((vnode) => vnode !== undefined) };
  });
  expect(before.vnodeAvailable).toBe(true);

  await page.keyboard.type(" memo");

  const after = await page.evaluate(() => {
    const current = Array.from(
      document.querySelectorAll<HTMLElement>('[data-testid="segment-row"]'),
    ).slice(0, 3);
    const probe = (
      window as Window & {
        __rowIsolationProbe?: {
          elements: HTMLElement[];
          vnodes: unknown[];
          values: string[];
        };
      }
    ).__rowIsolationProbe;
    if (!probe) throw new Error("row isolation probe missing");
    return {
      elementsSame: current.map((element, index) => element === probe.elements[index]),
      vnodesSame: current.map(
        (element, index) =>
          (element as HTMLElement & { __vnode?: unknown }).__vnode === probe.vnodes[index],
      ),
      values: current.map((element) => element.querySelector('[data-testid="segment-translation"]')?.value ?? ""),
      previousValues: probe.values,
      editedFocused: document.activeElement === current[1]?.querySelector('[data-testid="segment-translation"]'),
      editedSelection: (() => {
        const area = current[1]?.querySelector('[data-testid="segment-translation"]');
        return area
          ? [area.selectionStart, area.selectionEnd, area.value.length]
          : null;
      })(),
    };
  });

  // DOM 常驻、相邻行未重渲染，编辑行只更新自身。
  expect(after.elementsSame).toEqual([true, true, true]);
  expect(after.vnodesSame).toEqual([true, false, true]);
  expect(after.values[0]).toBe(after.previousValues[0]);
  expect(after.values[2]).toBe(after.previousValues[2]);
  expect(after.values[1]).toBe(`${after.previousValues[1]} memo`);
  // 焦点与光标保持在被编辑行末尾。
  expect(after.editedFocused).toBe(true);
  expect(after.editedSelection?.[0]).toBe(after.editedSelection?.[2]);
  expect(after.editedSelection?.[1]).toBe(after.editedSelection?.[2]);
});

test("save shortcut only submits when the page can save", async ({ page }) => {
  await openCorrection(page);
  const firstRow = page.getByTestId("segment-row").first();
  const save = saveActions(page).first();
  await expect(save).toBeDisabled();

  // 干净页面：快捷键不创建空 revision。
  await page.keyboard.press("ControlOrMeta+s");
  expect((await readSaveProbe(page)).calls).toBe(0);

  // 非法时间草稿：快捷键不绕过保存按钮的禁用条件。
  const startInput = firstRow.locator("input").first();
  await startInput.fill("not-a-timestamp");
  await expect(firstRow.getByText("Invalid timestamp format")).toBeVisible();
  await expect(save).toBeDisabled();
  await page.keyboard.press("ControlOrMeta+s");
  expect((await readSaveProbe(page)).calls).toBe(0);

  // 修正后 dirty：快捷键调用同一保存入口。
  await startInput.fill("00:00:01,000");
  await firstRow.getByTestId("segment-translation").fill("Shortcut save");
  await expect(save).toBeEnabled();
  await page.keyboard.press("ControlOrMeta+s");
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  expect((await readSaveProbe(page)).calls).toBe(1);
});

test("does not submit twice while a save is in flight", async ({ page }) => {
  await seedHeldSaveSubtitle(page);
  await openCorrection(page);
  // 保存中按钮的可访问名会包含 loading 状态文本，用顶栏操作区定位更稳定。
  const save = page.locator(".mcs-top__actions button");
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Held save");

  await page.keyboard.press("ControlOrMeta+s");
  // 保存中：按钮 loading/disabled，第二次快捷键不得再发 PUT。
  await expect(save).toBeDisabled();
  await expect(page.getByText("Saving…", { exact: true })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+s");
  await releaseSaveSubtitle(page);
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  expect((await readSaveProbe(page)).calls).toBe(1);
});

test("shortcut ignores dialog focus and IME composition", async ({ page }) => {
  await openCorrection(page);
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Dirty draft");

  // 对话框焦点不触发页面保存（合成焦点上下文，只验证事件目标守卫）。
  await page.evaluate(() => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = "Dialog action";
    dialog.append(button);
    document.body.append(dialog);
    button.focus();
  });
  await page.keyboard.press("ControlOrMeta+s");
  expect((await readSaveProbe(page)).calls).toBe(0);

  // composition 中的快捷键不提交。
  await page.evaluate(() => {
    document.querySelector('[role="dialog"]')?.remove();
    document
      .querySelector('[data-testid="segment-row"] [data-testid="segment-translation"]')
      ?.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "s",
          ctrlKey: true,
          isComposing: true,
          bubbles: true,
        }),
      );
  });
  expect((await readSaveProbe(page)).calls).toBe(0);

  // 正常页面焦点时快捷键仍有效。
  await page.keyboard.press("ControlOrMeta+s");
  await expect(
    page.getByRole("status").filter({ hasText: "Correction saved." }),
  ).toBeVisible();
  expect((await readSaveProbe(page)).calls).toBe(1);
});

test("cleans up the save shortcut listener after leaving the correction page", async ({
  page,
}) => {
  // 让离开时保存失败，走确认弹窗后放弃，确保页面是在“不保存”的路径下卸载。
  await seedSaveSubtitleFailure(page, "conflict");
  await openCorrection(page);
  await page.getByTestId("segment-row").first().getByTestId("segment-translation").fill("Unsaved draft");
  await page.getByRole("button", { name: "Back to project" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Discard and leave" }).click();
  await expect(page).toHaveURL(/#\/projects\/proj-demo$/);
  const callsAfterLeave = (await readSaveProbe(page)).calls;

  // 页面已卸载：残留监听会在 keydown 内同步发起 PUT，这里必须不再增加。
  await page.keyboard.press("ControlOrMeta+s");
  expect((await readSaveProbe(page)).calls).toBe(callsAfterLeave);
  expect((await readSaveProbe(page)).revision).toBeNull();
});

test("keeps focus and selection on the affected row after merge and split", async ({
  page,
}) => {
  await openCorrection(page);
  const rows = page.getByTestId("segment-row");
  const firstRow = rows.first();
  await firstRow.focus();

  // Tab 进入本行操作区：试听 → 合并 → 拆分。
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(firstRow.getByRole("button", { name: "Merge next" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(rows).toHaveCount(7);
  const mergedRow = rows.first();
  await expect(mergedRow).toHaveClass(/is-selected/);
  await expect(mergedRow).toBeFocused();

  // 拆分合并后的行：焦点与选中都落在新选的左半句。
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(mergedRow.getByRole("button", { name: "Split" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(rows).toHaveCount(8);
  const leftRow = rows.first();
  await expect(leftRow).toHaveClass(/is-selected/);
  await expect(leftRow).toBeFocused();
  await expect(leftRow.getByTestId("segment-translation")).toHaveValue("Welcome to YaoMu. This is");
});


test("creation pages expose no serious accessibility violations", async ({ page }) => {
  await page.goto("/#/tools/translate");
  await page.waitForTimeout(500);
  const translate = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  expect(
    translate.violations.filter((v) => v.impact === "critical" || v.impact === "serious"),
  ).toEqual([]);

  await page.goto("/#/voice-clone");
  await page.waitForTimeout(500);
  const clone = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  expect(
    clone.violations.filter((v) => v.impact === "critical" || v.impact === "serious"),
  ).toEqual([]);
});

import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { seedOnboardedSettings } from "./support";

// 可访问性基线：只覆盖当前 main 路由（旧分支的 /manual-correction/:id 与
// /pipeline/new 不复存在）。浏览器 mock 只证明结构与可访问名；真实
// Electron/Core 的键盘与视觉验证仍由隔离运行补充。
const PAGES: { name: string; path: string }[] = [
  { name: "workbench", path: "/" },
  { name: "projects", path: "/projects" },
  { name: "project detail", path: "/projects/proj-demo" },
  { name: "pipeline", path: "/pipeline?project=proj-demo" },
  { name: "manual correction", path: "/projects/proj-demo/correction" },
  { name: "extract tool", path: "/tools/extract" },
  { name: "settings general", path: "/settings/general" },
  { name: "models", path: "/settings/models" },
];

test.beforeEach(async ({ page }) => {
  await seedOnboardedSettings(page);
});

for (const entry of PAGES) {
  test(`${entry.name} has no WCAG A/AA violations`, async ({ page }) => {
    await page.goto(`/#${entry.path}`);
    await page.waitForLoadState("networkidle");
    const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
    expect(
      results.violations.map((violation) => ({
        id: violation.id,
        impact: violation.impact,
        nodes: violation.nodes.map((node) => node.target.join(" ")),
      })),
    ).toEqual([]);
  });
}

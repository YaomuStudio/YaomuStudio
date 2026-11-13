import { defineConfig, devices } from "@playwright/test";

// UI E2E：浏览器下没有 window.yaomu 桥，Playwright 显式开启测试夹具模式
// （VITE_E2E_FIXTURES=1；仅 Vite dev serve 生效，build 一律禁用）才会加载测试数据。
// 产品没有演示模式入口或横幅；夹具只用于测试。
// 不复用未知模式的已有服务：复用可能把非测试服务器当成夹具环境，得出假结论。
// 报告与运行产物统一写入 .design-build/e2e/（已被 .gitignore 排除）。
export default defineConfig({
  testDir: "./e2e",
  outputDir: ".design-build/e2e/test-results",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: [
    ["list"],
    ["html", { outputFolder: ".design-build/e2e/html-report", open: "never" }],
    ["json", { outputFile: ".design-build/e2e/results.json" }],
  ],
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "npm run dev -w ui -- --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    env: { VITE_E2E_FIXTURES: "1" },
    reuseExistingServer: false,
    timeout: 120_000,
  },
});

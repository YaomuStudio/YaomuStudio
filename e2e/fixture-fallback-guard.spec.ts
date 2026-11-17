import { expect, test } from "@playwright/test";
import { build, createServer, preview, type PreviewServer, type ViteDevServer } from "vite";
import fs from "node:fs/promises";
import path from "node:path";
import { seedOnboardedSettings } from "./support";

// 生产/测试边界回归：
// - 只有显式 VITE_E2E_FIXTURES=1 的开发 serve 才加载测试夹具；产品没有演示模式或横幅；
// - 无桥且未开测试夹具：安全 CORE_NOT_READY、同步取消订阅、无示例项目；
// - 已有 bridge 时绝不因调用失败降级到夹具；
// - 生产 build（包括 NODE_ENV=development + VITE_E2E_FIXTURES=1 负例）一律禁用夹具。
// 构建产物写入 .design-build/（已忽略），不覆盖 ui/dist 的桌面验证产物。

const repoRoot = process.cwd();
const uiRoot = path.join(repoRoot, "ui");
const viteConfig = path.join(uiRoot, "vite.config.ts");

function baseURL(url: string): string {
  return url.endsWith("/") ? url : `${url}/`;
}

test.describe.configure({ mode: "serial" });

test("test fixtures are opt-in and expose no product demo UI", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/projects");

  await expect(page.getByTestId("bridge-unavailable-banner")).toHaveCount(0);
  await expect(page.locator("article.recent-card").first()).toBeVisible();

  const mode = await page.evaluate(async () => {
    const module = await import("/src/api/index.ts");
    return module.apiMode;
  });
  expect(mode).toBe("fixtures");
});

test.describe("without the fixture flag", () => {
  let server: ViteDevServer;
  let url: string;

  test.beforeAll(async () => {
    const previous = process.env.VITE_E2E_FIXTURES;
    delete process.env.VITE_E2E_FIXTURES;
    try {
      server = await createServer({
        root: uiRoot,
        configFile: viteConfig,
        logLevel: "silent",
        server: { host: "127.0.0.1", port: 4273, strictPort: true },
      });
      await server.listen();
    } finally {
      if (previous !== undefined) process.env.VITE_E2E_FIXTURES = previous;
    }
    url = baseURL(server.resolvedUrls?.local[0] ?? "http://127.0.0.1:4273/");
  });

  test.afterAll(async () => {
    await server?.close();
  });

  test("plain development shows a browser preview note and never fixture data", async ({
    page,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await seedOnboardedSettings(page);
    await page.goto(`${url}#/projects`);

    await expect(page.getByTestId("browser-preview-banner")).toBeVisible();
    await expect(page.getByTestId("browser-preview-banner")).toContainText("Browser preview");
    await expect(page.getByTestId("bridge-unavailable-banner")).toHaveCount(0);
    await expect(page.locator("article.recent-card")).toHaveCount(0);
    await expect(page.locator("main.app-shell__main")).toBeVisible();

    const state = await page.evaluate(async () => {
      const module = await import("/src/api/index.ts");
      const rejected = await module.api.listProjects().then(
        () => ({ rejected: false as const }),
        (error: { name?: string; code?: string; messageKey?: string }) => ({
          rejected: true as const,
          name: error.name,
          code: error.code,
          messageKey: error.messageKey,
        }),
      );

      let cancelled = false;
      const unsubscribe = module.api.subscribeEvents(
        () => undefined,
        () => {
          cancelled = true;
        },
      );
      const unsubscribeType = typeof unsubscribe;
      unsubscribe();
      await new Promise((resolve) => setTimeout(resolve, 0));

      const delivered = await new Promise<{ code?: string } | null>((resolve) => {
        module.api.subscribeEvents(
          () => undefined,
          (error: { code?: string }) => resolve({ code: error.code }),
        );
        setTimeout(() => resolve(null), 500);
      });

      return { mode: module.apiMode, rejected, unsubscribeType, cancelled, delivered };
    });

    expect(state.mode).toBe("unavailable");
    expect(state.rejected).toMatchObject({
      rejected: true,
      name: "ApiError",
      code: "CORE_NOT_READY",
      messageKey: "api.coreNotReady",
    });
    expect(state.unsubscribeType).toBe("function");
    expect(state.cancelled).toBe(false);
    expect(state.delivered?.code).toBe("CORE_NOT_READY");
    // 无桥不可用必须走安全错误展示，不能在页面留下未捕获异常。
    expect(pageErrors, "unavailable mode must not raise uncaught page errors").toEqual([]);
  });
});

test("a partial bridge is treated as unavailable instead of truthy success", async ({ page }) => {
  await seedOnboardedSettings(page);
  await page.addInitScript(() => {
    (window as unknown as { yaomu?: unknown }).yaomu = {
      platform: "linux",
      api: {
        listProjects: () =>
          Promise.resolve({
            ok: true,
            data: [{ id: "fake", name: "fake project" }],
          }),
      },
      update: {},
    };
  });

  await page.goto("/#/projects");

  await expect(page.getByTestId("bridge-unavailable-banner")).toBeVisible();
  await expect(page.locator("article.recent-card")).toHaveCount(0);

  const mode = await page.evaluate(async () => {
    const module = await import("/src/api/index.ts");
    return module.apiMode;
  });
  expect(mode).toBe("unavailable");
});

test("a bridge whose property access throws stays unavailable instead of white-screening", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.addInitScript(() => {
    const api = new Proxy(
      {},
      {
        get() {
          throw new Error("broken bridge");
        },
      },
    );
    (window as unknown as { yaomu?: unknown }).yaomu = {
      platform: "linux",
      api,
      update: {},
    };
  });

  await page.goto("/#/projects");

  await expect(page.locator(".app-root")).toBeVisible();
  await expect(page.getByTestId("bridge-unavailable-banner")).toBeVisible();
  await expect(page.locator("article.recent-card")).toHaveCount(0);
});

test("an existing bridge keeps priority and failures never fall back to fixture data", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.addInitScript(() => {
    const error = {
      code: "CORE_NOT_READY",
      messageKey: "api.coreNotReady",
      retryable: true,
      actionIds: [],
    };
    const api = new Proxy(
      {},
      {
        get: (_target, property) => {
          if (property === "subscribeEvents") {
            return (
              _cb: (event: unknown) => void,
              onError?: (streamError: unknown) => void,
            ) => {
              queueMicrotask(() =>
                onError?.({ code: "CORE_NOT_READY", message: "CORE_NOT_READY", retryable: true }),
              );
              return () => undefined;
            };
          }
          return () => Promise.resolve({ ok: false, error });
        },
      },
    );
    (window as unknown as { yaomu?: unknown }).yaomu = {
      platform: "linux",
      api,
      update: {},
    };
  });

  await page.goto("/#/projects");

  await expect(page.getByTestId("bridge-unavailable-banner")).toHaveCount(0);
  await expect(page.locator("article.recent-card")).toHaveCount(0);
  await expect(
    page.getByRole("alert").filter({ hasText: "Local service is not ready" }).first(),
  ).toBeVisible();

  const mode = await page.evaluate(async () => {
    const module = await import("/src/api/index.ts");
    return module.apiMode;
  });
  expect(mode).toBe("bridge");
});

test("production build rejects fixtures even with NODE_ENV=development and VITE_E2E_FIXTURES=1", async ({
  page,
}) => {
  const outDir = path.join(repoRoot, ".design-build", "fixture-fallback-guard", "ui-dist");
  const previousNodeEnv = process.env.NODE_ENV;
  const previousFixtures = process.env.VITE_E2E_FIXTURES;
  process.env.NODE_ENV = "development";
  process.env.VITE_E2E_FIXTURES = "1";
  try {
    await build({
      root: uiRoot,
      configFile: viteConfig,
      logLevel: "silent",
      build: { outDir, emptyOutDir: true },
    });
  } finally {
    if (previousNodeEnv === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = previousNodeEnv;
    if (previousFixtures === undefined) delete process.env.VITE_E2E_FIXTURES;
    else process.env.VITE_E2E_FIXTURES = previousFixtures;
  }

  // 静态旁证：夹具的示例项目文案不应进入生产产物；运行态断言才是执行路径证明。
  const assetsDir = path.join(outDir, "assets");
  const assetNames = await fs.readdir(assetsDir);
  const scripts = await Promise.all(
    assetNames
      .filter((name) => name.endsWith(".js"))
      .map((name) => fs.readFile(path.join(assetsDir, name), "utf8")),
  );
  expect(scripts.join("\n")).not.toContain("星际译制 · 试播集");
  expect(scripts.join("\n")).not.toContain("Welcome to YaoMu");
  // 任务进度/日志测试钩子只存在于测试夹具，生产产物不得携带。
  expect(scripts.join("\n")).not.toContain("emitTaskProgress");
  expect(scripts.join("\n")).not.toContain("emitEventStreamConnected");
  expect(scripts.join("\n")).not.toContain("holdListTasks");
  expect(scripts.join("\n")).not.toContain("__yaomuMock");

  let server: PreviewServer;
  try {
    server = await preview({
      root: uiRoot,
      configFile: viteConfig,
      logLevel: "silent",
      preview: { host: "127.0.0.1", port: 4275, strictPort: true },
      build: { outDir },
    });
    const url = baseURL(server.resolvedUrls?.local[0] ?? "http://127.0.0.1:4275/");

    await seedOnboardedSettings(page);
    await page.goto(`${url}#/projects`);

    await expect(page.getByTestId("bridge-unavailable-banner")).toBeVisible();
    await expect(page.locator("article.recent-card")).toHaveCount(0);
    await expect(page.locator(".app-root")).toHaveAttribute("data-api-mode", "unavailable");
  } finally {
    await server?.close();
  }
});

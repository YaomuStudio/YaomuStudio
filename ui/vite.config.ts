import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { fileURLToPath, URL } from "node:url";
import { productionCspPlugin } from "./csp-policy.mjs";

const srcDir = fileURLToPath(new URL("./src", import.meta.url));
const fixturesEnabledSource = fileURLToPath(
  new URL("./src/api/e2eSource.enabled.ts", import.meta.url),
);
const fixturesDisabledSource = fileURLToPath(
  new URL("./src/api/e2eSource.disabled.ts", import.meta.url),
);

// 测试夹具模式（生产/测试边界）：
// - 仅 Vite 开发 serve（非 preview）且显式 VITE_E2E_FIXTURES=1 时启用 Playwright 夹具；
// - 产品没有演示模式入口；不能只看 import.meta.env.DEV，`NODE_ENV=development vite build` 的 DEV 也为 true；
// - 所有 build 一律禁用，且通过别名在构建期隔离夹具模块，避免示例数据进入产物。
export default defineConfig(({ command, isPreview }) => {
  const fixturesMode =
    command === "serve" && !isPreview && process.env.VITE_E2E_FIXTURES === "1";
  return {
    plugins: [vue(), productionCspPlugin(command)],
    base: "./",
    resolve: {
      alias: [
        {
          find: "yaomu-e2e-source",
          replacement: fixturesMode ? fixturesEnabledSource : fixturesDisabledSource,
        },
        { find: "@", replacement: srcDir },
      ],
    },
    server: {
      port: 5173,
    },
    build: {
      outDir: "dist",
    },
  };
});

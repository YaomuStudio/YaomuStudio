#!/usr/bin/env node
/**
 * 打包装配检查（artifact-only，不执行 WSL）。
 *
 * 用途：hosted Windows CI 没有准备好的 yaomu 镜像，不能运行严格 WSL smoke。
 * 本脚本只验证真实打包目录的装配完整性：
 * - 选定产物（默认 dist-app 下 win-unpacked）确有应用可执行文件与 resources；
 * - resources/core 下有 Core 二进制、Runner 启动器、runner_src 与清单；
 * - 用生产校验模块核对 runner-manifest.json 与逐文件 SHA-256（含拒绝字节码缓存）。
 *
 * 明确边界：WSL not-run；本脚本通过不等于 Windows/WSL 验收，也不调用完整
 * packaged smoke、不自动配置镜像、不猜发行版或 self-hosted 标签。
 */
import { createRequire } from "node:module";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import {
  resolvePackagedArtifacts,
  verifyPackagedLayout,
} from "./smoke-packaged-support.mjs";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const outDir = path.join(repoRoot, "desktop", "dist-app");
const require = createRequire(import.meta.url);

function log(message) {
  console.log(`[packaged-artifacts] ${message}`);
}

function fail(message) {
  console.error(`[packaged-artifacts] FAIL: ${message}`);
  process.exit(1);
}

async function main() {
  let runnerResources;
  try {
    runnerResources = require("../dist/runner-resources.js");
  } catch (error) {
    fail(
      `无法加载 desktop/dist 生产模块（${error instanceof Error ? error.message : String(error)}）；` +
        "先运行 npm run build -w desktop",
    );
  }

  let artifacts;
  try {
    artifacts = resolvePackagedArtifacts({ outDir });
  } catch (error) {
    fail(error instanceof Error ? error.message : String(error));
  }
  log(`app executable: ${artifacts.executable}`);
  log(`app resources: ${artifacts.resourcesDir}`);

  let layout;
  try {
    layout = verifyPackagedLayout(artifacts.resourcesDir);
  } catch (error) {
    fail(error instanceof Error ? error.message : String(error));
  }
  log(`core binary: ${layout.coreBinary}`);

  const validation = await runnerResources.validateBundledRunnerResources({
    coreDir: layout.coreDir,
  });
  if (!validation.ok) {
    fail(`包内 Runner 资源校验失败：${validation.reason}`);
  }
  log(
    `runner resources: version=${validation.descriptor.runnerVersion} ` +
      `manifest=${validation.descriptor.manifestDigest.slice(0, 12)}`,
  );
  log("WSL: not-run (artifact-only assembly check; does not satisfy Windows acceptance)");
  log("ARTIFACT CHECK PASS (not a full packaged smoke)");
  process.exit(0);
}

main().catch((error) => {
  fail(error instanceof Error ? (error.stack ?? error.message) : String(error));
});

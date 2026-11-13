#!/usr/bin/env node
/**
 * 生成随包 Python Runner 资源清单（本地装配完整性，不是签名）。
 *
 * 权威：contracts/packaged-runner-resources.md 与 schema。
 * - 输入是 scripts/build-core.sh 已暂存的实际源码树（desktop/build/core/runner_src），
 *   不另建第二份源码、不导入 Runner 模块。
 * - 版本来自 runner/pyproject.toml 与包 __init__.py；两者不一致即失败。
 * - 逐文件流式 SHA-256；路径相对 runner_src、限制在 yaomustudio_runner/ 内；
 *   排序确定；部署树中的 `__pycache__`/`.pyc`/`.pyo`（不分大小写）一律失败，
 *   因为 `-B` 只禁止写入、仍会读取已有缓存；拒绝链接、特殊文件与逃逸。
 * - 先写临时文件再原子替换，失败不会留下半份清单。
 *
 * 用法：
 *   node scripts/generate-runner-manifest.mjs \
 *     --runner-src desktop/build/core/runner_src \
 *     --pyproject runner/pyproject.toml \
 *     --out desktop/build/core/runner-manifest.json
 */
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, mkdir, readdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const REQUIRED_PYTHON = ">=3.11";
const RUNNER_VERSION_PATTERN = /^[A-Za-z0-9][A-Za-z0-9.+_-]*$/;
const FILE_PATH_PATTERN =
  /^yaomustudio_runner\/(?:[A-Za-z0-9_][A-Za-z0-9_.-]*\/)*[A-Za-z0-9_][A-Za-z0-9_.-]*$/;
const MAX_FILES = 4096;
const ENTRY_FILES = [
  "yaomustudio_runner/__init__.py",
  "yaomustudio_runner/__main__.py",
  "yaomustudio_runner/main.py",
];

function fail(message) {
  throw new Error(message);
}

/** 读取 TOML 表中某个字符串值；只支持本仓库 pyproject 的简单标量。 */
export function readTomlString(text, table, key) {
  let inTable = false;
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (/^\[.*\]$/.test(line)) {
      inTable = line === `[${table}]`;
      continue;
    }
    if (!inTable) continue;
    const match = new RegExp(`^${key}\\s*=\\s*["']([^"']+)["']\\s*(?:#.*)?$`).exec(line);
    if (match) return match[1];
  }
  return null;
}

/** 读取包 __init__.py 的 __version__；不执行/导入包代码。 */
export function readInitVersion(text) {
  const match = /^__version__\s*=\s*["']([^"']+)["']\s*$/m.exec(text);
  return match ? match[1] : null;
}

function isBytecodeCache(relativePath) {
  const lower = relativePath.toLowerCase();
  return (
    lower.split("/").includes("__pycache__") ||
    lower.endsWith(".pyc") ||
    lower.endsWith(".pyo")
  );
}

function isInside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}

async function sha256File(filePath) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(filePath, { highWaterMark: 1024 * 1024 })) {
    hash.update(chunk);
  }
  return hash.digest("hex");
}

/** 枚举源码树并计算逐文件摘要；拒绝链接/特殊文件/包外条目。 */
export async function walkRunnerSource(runnerSrc) {
  let rootStat;
  try {
    rootStat = await lstat(runnerSrc);
  } catch {
    fail(`runner_src 不存在: ${runnerSrc}`);
  }
  if (rootStat.isSymbolicLink() || !rootStat.isDirectory()) {
    fail("runner_src 必须是普通目录（不接受链接）");
  }
  const root = path.resolve(runnerSrc);
  const files = [];
  const pending = [""];
  while (pending.length > 0) {
    const relativeDir = pending.pop();
    const absoluteDir = relativeDir ? path.join(root, ...relativeDir.split("/")) : root;
    if (relativeDir && !isInside(root, absoluteDir)) {
      fail(`源码路径逃逸: ${relativeDir}`);
    }
    let entries;
    try {
      entries = await readdir(absoluteDir, { withFileTypes: true });
    } catch (error) {
      fail(`无法读取源码目录 ${relativeDir || "runner_src"}: ${error.message}`);
    }
    for (const entry of entries) {
      const relative = relativeDir ? `${relativeDir}/${entry.name}` : entry.name;
      // 部署树不能携带字节码缓存：-B 只禁止写入，仍可能读取未验证的 .pyc。
      if (isBytecodeCache(relative)) {
        fail(`源码树包含 Python 字节码缓存（清单不可忽略）: ${relative}`);
      }
      if (entry.isSymbolicLink()) fail(`源码树包含链接: ${relative}`);
      const absolute = path.join(absoluteDir, entry.name);
      if (!isInside(root, absolute)) fail(`源码路径逃逸: ${relative}`);
      if (entry.isDirectory()) {
        if (relative !== "yaomustudio_runner" && !relative.startsWith("yaomustudio_runner/")) {
          fail(`源码文件必须位于 yaomustudio_runner/: ${relative}`);
        }
        pending.push(relative);
        continue;
      }
      if (!entry.isFile()) fail(`源码树包含特殊文件: ${relative}`);
      if (!relative.startsWith("yaomustudio_runner/")) {
        fail(`源码文件必须位于 yaomustudio_runner/: ${relative}`);
      }
      if (!FILE_PATH_PATTERN.test(relative) || relative.length > 512) {
        fail(`源码文件路径不符合清单模式: ${relative}`);
      }
      const stat = await lstat(absolute);
      if (stat.isSymbolicLink() || !stat.isFile()) {
        fail(`源码树包含特殊文件: ${relative}`);
      }
      files.push({
        path: relative,
        size: stat.size,
        sha256: await sha256File(absolute),
      });
      if (files.length > MAX_FILES) fail(`源码文件超过 ${MAX_FILES} 个`);
    }
  }
  files.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  const folded = new Set();
  for (const file of files) {
    const key = file.path.toLowerCase();
    if (folded.has(key)) fail(`源码路径大小写折叠后重复: ${file.path}`);
    folded.add(key);
  }
  if (files.length < 3) fail("源码文件过少（缺少 Runner 入口）");
  for (const entry of ENTRY_FILES) {
    if (!files.some((file) => file.path === entry)) fail(`缺少入口文件: ${entry}`);
  }
  return files;
}

export function buildManifest({ runnerVersion, files }) {
  return {
    kind: "yaomu_runner_resources",
    schema_version: 1,
    runner_version: runnerVersion,
    requires_python: REQUIRED_PYTHON,
    files,
  };
}

/**
 * 生成并原子写入清单。
 * @returns {{manifest: object, manifestPath: string, digest: string, text: string}}
 */
export async function generateRunnerManifest({ runnerSrc, pyprojectPath, outPath }) {
  const [pyprojectText, initText] = await Promise.all([
    readFile(pyprojectPath, "utf8"),
    readFile(path.join(runnerSrc, "yaomustudio_runner", "__init__.py"), "utf8"),
  ]);
  const pyprojectVersion = readTomlString(pyprojectText, "project", "version");
  const requiresPython = readTomlString(pyprojectText, "project", "requires-python");
  const initVersion = readInitVersion(initText);
  if (!pyprojectVersion) fail(`${pyprojectPath} 缺少 [project] version`);
  if (!initVersion) fail("Runner __init__.py 缺少 __version__");
  if (pyprojectVersion !== initVersion) {
    fail(`Runner 版本不一致: pyproject=${pyprojectVersion} __init__=${initVersion}`);
  }
  if (!RUNNER_VERSION_PATTERN.test(pyprojectVersion) || pyprojectVersion.length > 64) {
    fail(`Runner 版本格式不合法: ${pyprojectVersion}`);
  }
  if (requiresPython !== REQUIRED_PYTHON) {
    fail(`requires-python 必须是 ${REQUIRED_PYTHON}（当前 ${requiresPython ?? "缺失"}）`);
  }

  const files = await walkRunnerSource(runnerSrc);
  const manifest = buildManifest({ runnerVersion: pyprojectVersion, files });
  const text = `${JSON.stringify(manifest, null, 2)}\n`;
  const digest = createHash("sha256").update(text, "utf8").digest("hex");

  await mkdir(path.dirname(outPath), { recursive: true });
  const temporary = `${outPath}.tmp-${process.pid}`;
  try {
    await writeFile(temporary, text, { mode: 0o644 });
    await rename(temporary, outPath);
  } catch (error) {
    await rm(temporary, { force: true }).catch(() => undefined);
    throw error;
  }
  return { manifest, manifestPath: outPath, digest, text };
}

function parseArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    if (key === "--runner-src" || key === "--pyproject" || key === "--out") {
      args[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = argv[index + 1];
      index += 1;
      continue;
    }
    fail(`未知参数: ${key}`);
  }
  if (!args.runnerSrc || !args.pyproject || !args.out) {
    fail("用法: generate-runner-manifest.mjs --runner-src <dir> --pyproject <file> --out <file>");
  }
  return args;
}

const isMain =
  process.argv[1] !== undefined &&
  pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = await generateRunnerManifest({
      runnerSrc: path.resolve(args.runnerSrc),
      pyprojectPath: path.resolve(args.pyproject),
      outPath: path.resolve(args.out),
    });
    console.log(
      `[runner-manifest] version=${result.manifest.runner_version} files=${result.manifest.files.length} ` +
        `sha256=${result.digest} -> ${result.manifestPath}`,
    );
  } catch (error) {
    console.error(`[runner-manifest] FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(1);
  }
}

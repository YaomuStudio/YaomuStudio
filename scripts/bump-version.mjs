#!/usr/bin/env node
/**
 * 版本提升：一次性同步根、ui、desktop 三处 package.json、package-lock.json 与两份 README。
 *
 * 存在的理由：`scripts/check-readme.mjs` 要求 README 里的安装包名与 package 版本一致，
 * 而版本号曾经分两步改（先改 package.json，再补 README），一旦中间被打断或忘记，
 * 公开仓 CI 的 `test` 就会在版本提交上变红。这里把两类文件合并为一个不可分的动作：
 * 任何一处对不上就整体中止，绝不落半成品。
 *
 * 用法：
 *   node scripts/bump-version.mjs 0.1.0-beta.7            # 只改文件
 *   node scripts/bump-version.mjs 0.1.0-beta.7 --commit   # 改完并提交为一个 commit
 *   node scripts/bump-version.mjs 0.1.0-beta.7 --root DIR # 指定仓库根（测试用）
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const SEMVER = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?$/;
const JSON_FILES = ["package.json", "ui/package.json", "desktop/package.json", "package-lock.json"];
const README_FILES = ["README.md", "README.zh-CN.md"];

export class BumpVersionError extends Error {}

function readJson(root, rel) {
  const full = path.join(root, rel);
  if (!fs.existsSync(full)) throw new BumpVersionError(`${rel}: 文件不存在`);
  try {
    return JSON.parse(fs.readFileSync(full, "utf8"));
  } catch (error) {
    throw new BumpVersionError(`${rel}: JSON 解析失败（${error.message}）`);
  }
}

function writeJson(root, rel, value) {
  fs.writeFileSync(path.join(root, rel), `${JSON.stringify(value, null, 2)}\n`);
}

function splitVersion(version) {
  const [core, prerelease = ""] = version.split("-");
  return { core: core.split(".").map(Number), prerelease: prerelease ? prerelease.split(".") : [] };
}

/** 仅用于拦截“往低版本走”的手滑；不追求完整 semver 优先级语义。 */
export function compareVersions(a, b) {
  const left = splitVersion(a);
  const right = splitVersion(b);
  for (let i = 0; i < 3; i += 1) {
    if (left.core[i] !== right.core[i]) return left.core[i] < right.core[i] ? -1 : 1;
  }
  if (left.prerelease.length === 0 && right.prerelease.length === 0) return 0;
  if (left.prerelease.length === 0) return 1;
  if (right.prerelease.length === 0) return -1;
  const size = Math.max(left.prerelease.length, right.prerelease.length);
  for (let i = 0; i < size; i += 1) {
    const x = left.prerelease[i];
    const y = right.prerelease[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (x === y) continue;
    const nx = /^\d+$/.test(x);
    const ny = /^\d+$/.test(y);
    if (nx && ny) return Number(x) < Number(y) ? -1 : 1;
    if (nx !== ny) return nx ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

/**
 * 计算并落盘全部改动。所有校验都在写第一个文件之前完成，
 * 因此校验失败时工作区保持原样。
 */
export function bumpVersion({ root, version, allowDowngrade = false }) {
  if (!SEMVER.test(version)) {
    throw new BumpVersionError(`版本格式非法：${version}（期望 X.Y.Z 或 X.Y.Z-beta.N）`);
  }

  const pkg = readJson(root, "package.json");
  const from = pkg.version;
  if (!from) throw new BumpVersionError("package.json: 缺少 version 字段");
  if (from === version) throw new BumpVersionError(`版本未变化：已经是 ${version}`);
  if (!allowDowngrade && compareVersions(version, from) < 0) {
    throw new BumpVersionError(`目标版本 ${version} 低于当前版本 ${from}`);
  }

  const jsonWrites = [];

  // 三处 package.json：只改 version，其余字段原样保留。
  for (const rel of JSON_FILES.slice(0, 3)) {
    const data = readJson(root, rel);
    if (data.version !== from) {
      throw new BumpVersionError(`${rel}: 版本是 ${data.version}，与根 package.json 的 ${from} 不一致`);
    }
    jsonWrites.push([rel, { ...data, version }]);
  }

  // 锁文件：npm workspaces 会在根、""、各 workspace 上重复记录版本。
  const lock = readJson(root, "package-lock.json");
  if (lock.version !== from) {
    throw new BumpVersionError(`package-lock.json: 版本是 ${lock.version}，与根 package.json 的 ${from} 不一致`);
  }
  lock.version = version;
  for (const key of ["", "ui", "desktop"]) {
    const entry = lock.packages?.[key];
    if (entry && entry.version !== undefined) {
      if (entry.version !== from) {
        throw new BumpVersionError(
          `package-lock.json: packages["${key}"].version 是 ${entry.version}，与根版本 ${from} 不一致`,
        );
      }
      entry.version = version;
    }
  }
  jsonWrites.push(["package-lock.json", lock]);

  // README：资产名与版本徽标都内嵌版本号，整体替换。
  // 负向先行断言避免把 0.1.0-beta.6 吃进 0.1.0-beta.60 这类更长版本号里。
  const pattern = new RegExp(`${from.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?!\\d)`, "g");
  const readmeWrites = [];
  for (const rel of README_FILES) {
    const full = path.join(root, rel);
    if (!fs.existsSync(full)) throw new BumpVersionError(`${rel}: 文件不存在`);
    const text = fs.readFileSync(full, "utf8");
    const count = [...text.matchAll(pattern)].length;
    if (count === 0) {
      throw new BumpVersionError(
        `${rel}: 未找到版本号 ${from}；README 与 package.json 已经脱节，请先人工核对`,
      );
    }
    readmeWrites.push([rel, text.replace(pattern, version), count]);
  }

  for (const [rel, data] of jsonWrites) writeJson(root, rel, data);
  for (const [rel, text] of readmeWrites) fs.writeFileSync(path.join(root, rel), text);

  return { from, to: version, files: [...jsonWrites.map(([rel]) => rel), ...readmeWrites.map(([rel]) => rel)] };
}

export function commitVersion({ root, version }) {
  const files = [...JSON_FILES, ...README_FILES];
  const add = spawnSync("git", ["-C", root, "add", "--", ...files], { encoding: "utf8" });
  if (add.status !== 0) throw new BumpVersionError(`git add 失败：${add.stderr.trim()}`);
  const commit = spawnSync(
    "git",
    ["-C", root, "commit", "-m", `chore(release): 提升版本到 ${version}`],
    { encoding: "utf8" },
  );
  if (commit.status !== 0) throw new BumpVersionError(`git commit 失败：${commit.stderr.trim()}`);
  return commit.stdout.trim();
}

function parseArgs(argv) {
  const options = { root: null, version: null, commit: false, allowDowngrade: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--commit") options.commit = true;
    else if (arg === "--allow-downgrade") options.allowDowngrade = true;
    else if (arg === "--root") {
      i += 1;
      if (!argv[i]) throw new BumpVersionError("--root 需要一个目录参数");
      options.root = argv[i];
    } else if (arg.startsWith("-")) throw new BumpVersionError(`未知参数：${arg}`);
    else if (options.version) throw new BumpVersionError(`多余的版本参数：${arg}`);
    else options.version = arg;
  }
  if (!options.version) throw new BumpVersionError("缺少目标版本，例如 0.1.0-beta.7");
  return options;
}

function main(argv) {
  const options = parseArgs(argv);
  const root = options.root
    ? path.resolve(options.root)
    : path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

  const result = bumpVersion({ root, version: options.version, allowDowngrade: options.allowDowngrade });
  console.log(`[bump-version] ${result.from} -> ${result.to}`);
  for (const rel of result.files) console.log(`[bump-version] 已更新 ${rel}`);

  if (options.commit) {
    commitVersion({ root, version: result.to });
    console.log(`[bump-version] 已提交：chore(release): 提升版本到 ${result.to}`);
  } else {
    console.log(`[bump-version] 未提交；确认无误后执行：git commit -m "chore(release): 提升版本到 ${result.to}"`);
  }
  console.log("[bump-version] 提醒：三处 package.json 与 README 必须在同一个提交里，否则 CI 会在版本提交上变红。");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    console.error(`[bump-version] ${error.message}`);
    process.exit(1);
  }
}

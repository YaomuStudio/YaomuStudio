#!/usr/bin/env node
/**
 * README 公开面检查：语言互链、相对链接/图片存在、禁止词与内部路径、版本化资产名。
 * 用法：node scripts/check-readme.mjs
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const files = ["README.md", "README.zh-CN.md"];
const failures = [];

const banned = [
  "docs/research",
  "source-migration",
  "competitor-comparison",
  "runner-handoff",
  "presentations/",
  ".trellis",
  ".design-build",
  "gitlab",
  "gitlab.aodianyun",
  "flashhub-cdn",
  "flashhub-api",
  "/Users/",
  "C:\\",
  "Laev",
  "Trellis",
  "赋能",
  "一站式",
  "无缝",
  "革命性",
  "重新定义",
  "释放无限可能",
];

const emojiHeading = /^#{1,6}\s.*[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;

function read(rel) {
  const full = path.join(root, rel);
  if (!fs.existsSync(full)) {
    failures.push(`${rel}: missing`);
    return "";
  }
  return fs.readFileSync(full, "utf8");
}

const texts = Object.fromEntries(files.map((f) => [f, read(f)]));

for (const [rel, text] of Object.entries(texts)) {
  if (!text) continue;
  if (rel === "README.md" && !text.includes("[简体中文](README.zh-CN.md)")) {
    failures.push(`${rel}: missing Chinese language link`);
  }
  if (rel === "README.zh-CN.md" && !text.includes("[English](README.md)")) {
    failures.push(`${rel}: missing English language link`);
  }
  for (const word of banned) {
    if (text.includes(word)) failures.push(`${rel}: banned text "${word}"`);
  }
  for (const line of text.split(/\r?\n/)) {
    if (emojiHeading.test(line)) failures.push(`${rel}: emoji in heading: ${line.slice(0, 60)}`);
  }
}

const linkPattern = /!?\[[^\]]*\]\(([^)\s]+)\)/g;
const htmlPattern = /<(?:img|source)[^>]+(?:src|srcset)="([^"]+)"/gi;
for (const [rel, text] of Object.entries(texts)) {
  const targets = new Set();
  for (const match of text.matchAll(linkPattern)) targets.add(match[1]);
  for (const match of text.matchAll(htmlPattern)) {
    for (const part of match[1].split(",")) targets.add(part.trim().split(/\s+/)[0]);
  }
  for (const target of targets) {
    if (!target || target.startsWith("#") || /^(https?:|mailto:)/i.test(target)) continue;
    const local = target.split("#")[0];
    if (!local) continue;
    if (!fs.existsSync(path.join(root, local))) failures.push(`${rel}: broken relative link ${target}`);
  }
}

const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
const version = pkg.version;
// macOS 发布构建暂时排除；恢复 macOS 时把 mac-arm64/mac-x64 资产加回。
const assets = [
  `YaoMuStudio-${version}-win-x64.exe`,
  `YaoMuStudio-${version}-linux-x64.AppImage`,
];
for (const [rel, text] of Object.entries(texts)) {
  for (const asset of assets) {
    if (!text.includes(asset)) failures.push(`${rel}: missing release asset ${asset}`);
  }
}

for (const rel of [
  "assets/readme/logo.svg",
  "assets/readme/workbench-light.png",
  "assets/readme/workbench-dark.png",
  "assets/readme/workbench-zh-light.png",
  "assets/readme/workbench-zh-dark.png",
]) {
  if (!fs.existsSync(path.join(root, rel))) failures.push(`missing asset ${rel}`);
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`[check-readme] ${failure}`);
  process.exit(1);
}
console.log("[check-readme] ok");

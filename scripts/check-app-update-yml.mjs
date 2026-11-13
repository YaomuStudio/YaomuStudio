#!/usr/bin/env node
/**
 * 校验打包产物中的 app-update.yml 是否指向公开仓。
 * 用法：node scripts/check-app-update-yml.mjs <resources-dir | app-update.yml>
 *
 * 发布流水线在 electron-builder 打包后调用；本地 --dir 构建不一定生成该文件，
 * 因此不把它塞进日常打包路径。
 */
import fs from "node:fs";
import path from "node:path";
import yaml from "js-yaml";

function fail(message) {
  console.error(`[check-app-update-yml] ${message}`);
  process.exit(1);
}

const input = process.argv[2];
if (!input || input === "--help" || input === "-h") {
  console.log("usage: node scripts/check-app-update-yml.mjs <resources-dir | app-update.yml>");
  process.exit(input ? 0 : 1);
}

let file = path.resolve(input);
if (fs.existsSync(file) && fs.statSync(file).isDirectory()) {
  file = path.join(file, "app-update.yml");
}
if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
  fail(`missing app-update.yml under ${path.basename(path.dirname(file))}`);
}

let doc;
try {
  doc = yaml.load(fs.readFileSync(file, "utf8"));
} catch {
  fail("app-update.yml is not valid YAML");
}
if (!doc || typeof doc !== "object") fail("app-update.yml is empty or malformed");

if (doc.provider !== "github" || doc.owner !== "YaomuStudio" || doc.repo !== "YaomuStudio") {
  fail("app-update.yml does not point to YaomuStudio/YaomuStudio");
}
console.log("[check-app-update-yml] ok: YaomuStudio/YaomuStudio");

#!/usr/bin/env node
/**
 * 校验打包产物中的 app-update.yml 与 desktop/electron-builder.yml 的 publish 配置一致。
 * 用法：node scripts/check-app-update-yml.mjs <resources-dir | app-update.yml>
 *
 * 发布流水线在 electron-builder 打包后调用；本地 --dir 构建不一定生成该文件，
 * 因此不把它塞进日常打包路径。
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));

function fail(message) {
  console.error(`[check-app-update-yml] ${message}`);
  process.exit(1);
}

function expandEnv(value) {
  return String(value).replace(/\$\{env\.([A-Z0-9_]+)\}/g, (_, name) => {
    const resolved = process.env[name];
    if (!resolved) fail(`env ${name} is not defined`);
    return resolved;
  });
}

function normalizeUrl(value) {
  let url;
  try {
    url = new URL(expandEnv(value));
  } catch {
    fail(`invalid publish url: ${value}`);
  }
  if (url.protocol !== "https:") fail(`publish url must use https: ${value}`);
  if (url.search || url.hash) fail(`publish url must not contain query or hash: ${value}`);
  url.pathname = `${url.pathname.replace(/\/+$/, "")}/`;
  return url.href;
}

function expectedPublish() {
  const configPath = path.join(repoRoot, "desktop", "electron-builder.yml");
  let config;
  try {
    config = yaml.load(fs.readFileSync(configPath, "utf8"));
  } catch {
    fail("desktop/electron-builder.yml is not valid YAML");
  }
  const publish = Array.isArray(config?.publish) ? config.publish[0] : config?.publish;
  if (!publish || publish.provider !== "generic" || !publish.url) {
    fail("desktop/electron-builder.yml publish must be generic with url");
  }
  const url = normalizeUrl(publish.url);
  if (/(^|\.)(example|invalid)(\.|$)/i.test(new URL(url).hostname) || /localhost/i.test(url)) {
    fail("desktop/electron-builder.yml publish url is still a placeholder");
  }
  return url;
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

const expectedUrl = expectedPublish();
if (doc.provider !== "generic" || typeof doc.url !== "string") {
  fail("app-update.yml does not use a generic update provider");
}
if (normalizeUrl(doc.url) !== expectedUrl) {
  fail(`app-update.yml url mismatch: ${doc.url} != ${expectedUrl}`);
}
console.log(`[check-app-update-yml] ok: generic ${expectedUrl}`);

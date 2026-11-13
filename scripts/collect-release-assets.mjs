#!/usr/bin/env node
/**
 * 从 electron-builder 输出目录收集发布资产（只复制预期文件）。
 * 用法：node scripts/collect-release-assets.mjs --platform win|mac|linux --version 0.1.0 --dist desktop/dist-app --out <dir>
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

function fail(message) {
  console.error(`[collect-release-assets] FAIL: ${message}`);
  process.exit(1);
}

function parseArgs(argv) {
  const options = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--platform" || arg === "--version" || arg === "--dist" || arg === "--out") {
      const value = argv[i + 1];
      if (!value) fail(`missing value for ${arg}`);
      options[arg.slice(2)] = value;
      i += 1;
    } else if (arg === "--help" || arg === "-h") {
      console.log("usage: node scripts/collect-release-assets.mjs --platform win|mac|linux --version 0.1.0 --dist <dir> --out <dir>");
      process.exit(0);
    } else {
      fail(`unknown argument: ${arg}`);
    }
  }
  for (const key of ["platform", "version", "dist", "out"]) {
    if (!options[key]) fail(`missing --${key}`);
  }
  return options;
}

export function expectedAssets(platform, version) {
  const base = `YaoMuStudio-${version}`;
  if (platform === "win") {
    return {
      required: [`${base}-win-x64.exe`, `${base}-win-x64.exe.blockmap`, "latest.yml"],
      optional: [],
    };
  }
  if (platform === "mac") {
    const required = [];
    for (const arch of ["arm64", "x64"]) {
      required.push(
        `${base}-mac-${arch}.dmg`,
        `${base}-mac-${arch}.dmg.blockmap`,
        `${base}-mac-${arch}.zip`,
        `${base}-mac-${arch}.zip.blockmap`,
      );
    }
    required.push("latest-mac.yml");
    return { required, optional: [] };
  }
  if (platform === "linux") {
    return {
      required: [`${base}-linux-x64.AppImage`, "latest-linux.yml"],
      optional: [`${base}-linux-x64.AppImage.blockmap`],
    };
  }
  fail(`unsupported platform: ${platform}`);
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-beta\.(0|[1-9]\d*))?$/.test(options.version)) {
    fail(`invalid version: ${options.version}`);
  }
  const dist = path.resolve(options.dist);
  const out = path.resolve(options.out);
  if (dist === out) fail("--out must differ from --dist");
  if (!fs.existsSync(dist) || !fs.statSync(dist).isDirectory()) fail(`dist not found: ${dist}`);

  const { required, optional } = expectedAssets(options.platform, options.version);
  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });

  const copied = [];
  for (const name of required) {
    const source = path.join(dist, name);
    if (!fs.existsSync(source) || !fs.statSync(source).isFile()) {
      fail(`missing expected asset: ${name}`);
    }
    fs.copyFileSync(source, path.join(out, name));
    copied.push(name);
  }
  for (const name of optional) {
    const source = path.join(dist, name);
    if (fs.existsSync(source) && fs.statSync(source).isFile()) {
      fs.copyFileSync(source, path.join(out, name));
      copied.push(name);
    }
  }

  console.log(JSON.stringify({ platform: options.platform, version: options.version, assets: copied }, null, 2));
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : "";
if (invokedPath === fileURLToPath(import.meta.url)) main();

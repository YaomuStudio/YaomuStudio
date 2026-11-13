#!/usr/bin/env node
/**
 * 发布资产校验：资产集、更新清单引用、SHA-512/大小、macOS 双架构、未知文件拒绝。
 * 用法：node scripts/verify-release-assets.mjs --dir <assets> --version 0.1.0 [--platform all|win|mac|linux]
 */
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";
import { expectedAssets } from "./collect-release-assets.mjs";

export class ReleaseAssetError extends Error {
  constructor(code, detail = "") {
    super(code);
    this.code = code;
    this.detail = detail;
  }
}

function parseArgs(argv) {
  const options = { platform: "all" };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--dir" || arg === "--version" || arg === "--platform") {
      const value = argv[i + 1];
      if (!value) throw new ReleaseAssetError("argument-missing", arg);
      options[arg.slice(2)] = value;
      i += 1;
    } else if (arg === "--help" || arg === "-h") {
      console.log("usage: node scripts/verify-release-assets.mjs --dir <assets> --version 0.1.0 [--platform all|win|mac|linux]");
      process.exit(0);
    } else {
      throw new ReleaseAssetError("argument-invalid", arg);
    }
  }
  if (!options.dir || !options.version) throw new ReleaseAssetError("argument-missing", "dir/version");
  if (!["all", "win", "mac", "linux"].includes(options.platform)) {
    throw new ReleaseAssetError("platform-invalid", options.platform);
  }
  return options;
}

function expectedFor(platform, version) {
  if (platform !== "all") return expectedAssets(platform, version);
  const required = new Set();
  const optional = new Set();
  for (const item of ["win", "mac", "linux"]) {
    const set = expectedAssets(item, version);
    for (const name of set.required) required.add(name);
    for (const name of set.optional) optional.add(name);
  }
  return { required: [...required], optional: [...optional] };
}

function sha512File(file) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha512");
    const stream = fs.createReadStream(file);
    stream.on("data", (chunk) => hash.update(chunk));
    stream.on("error", reject);
    stream.on("end", () => resolve(hash.digest("base64")));
  });
}

function safeBasename(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 255 &&
    path.basename(value) === value &&
    !value.includes("/") &&
    !value.includes("\\") &&
    !value.includes("..") &&
    !value.includes("%")
  );
}

async function verifyManifest(dir, name, version, requireBothMacArch = false) {
  const file = path.join(dir, name);
  if (!fs.existsSync(file)) throw new ReleaseAssetError("manifest-missing", name);
  let doc;
  try {
    doc = yaml.load(fs.readFileSync(file, "utf8"));
  } catch {
    throw new ReleaseAssetError("manifest-invalid", name);
  }
  if (!doc || !Array.isArray(doc.files) || doc.files.length === 0) {
    throw new ReleaseAssetError("manifest-files-missing", name);
  }
  const urls = new Set();
  for (const entry of doc.files) {
    if (!safeBasename(entry?.url)) throw new ReleaseAssetError("manifest-url-invalid", name);
    if (!Number.isSafeInteger(entry.size) || entry.size <= 0) {
      throw new ReleaseAssetError("manifest-size-invalid", entry.url);
    }
    if (typeof entry.sha512 !== "string" || entry.sha512.length === 0) {
      throw new ReleaseAssetError("manifest-sha512-missing", entry.url);
    }
    const asset = path.join(dir, entry.url);
    if (!fs.existsSync(asset) || !fs.statSync(asset).isFile()) {
      throw new ReleaseAssetError("manifest-asset-missing", entry.url);
    }
    if (fs.statSync(asset).size !== entry.size) {
      throw new ReleaseAssetError("manifest-size-mismatch", entry.url);
    }
    if ((await sha512File(asset)) !== entry.sha512) {
      throw new ReleaseAssetError("manifest-sha512-mismatch", entry.url);
    }
    urls.add(entry.url);
  }
  if (doc.path !== undefined) {
    if (!safeBasename(doc.path) || !urls.has(doc.path)) {
      throw new ReleaseAssetError("manifest-path-invalid", name);
    }
  }
  if (requireBothMacArch) {
    const base = `YaoMuStudio-${version}-mac-`;
    if (![...urls].some((url) => url.startsWith(`${base}arm64.`))) {
      throw new ReleaseAssetError("manifest-mac-arm64-missing", name);
    }
    if (![...urls].some((url) => url.startsWith(`${base}x64.`))) {
      throw new ReleaseAssetError("manifest-mac-x64-missing", name);
    }
  }
  return urls;
}

export async function verifyReleaseAssets({ dir, version, platform = "all" }) {
  const root = path.resolve(dir);
  if (!fs.existsSync(root) || !fs.statSync(root).isDirectory()) {
    throw new ReleaseAssetError("assets-dir-missing", root);
  }
  if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-beta\.(0|[1-9]\d*))?$/.test(version)) {
    throw new ReleaseAssetError("version-invalid", version);
  }
  const { required, optional } = expectedFor(platform, version);
  const allowed = new Set([...required, ...optional]);
  const entries = fs.readdirSync(root, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isFile()) throw new ReleaseAssetError("asset-not-regular", entry.name);
    if (!allowed.has(entry.name)) throw new ReleaseAssetError("asset-unexpected", entry.name);
  }
  for (const name of required) {
    if (!fs.existsSync(path.join(root, name))) throw new ReleaseAssetError("asset-missing", name);
  }

  if (platform === "all" || platform === "win") {
    const urls = await verifyManifest(root, "latest.yml", version);
    const installer = `YaoMuStudio-${version}-win-x64.exe`;
    if (!urls.has(installer)) throw new ReleaseAssetError("manifest-win-installer-missing", installer);
  }
  if (platform === "all" || platform === "mac") {
    await verifyManifest(root, "latest-mac.yml", version, true);
  }
  if (platform === "all" || platform === "linux") {
    const urls = await verifyManifest(root, "latest-linux.yml", version);
    const appimage = `YaoMuStudio-${version}-linux-x64.AppImage`;
    if (!urls.has(appimage)) throw new ReleaseAssetError("manifest-linux-appimage-missing", appimage);
  }

  return { ok: true, assets: entries.map((entry) => entry.name) };
}

async function main() {
  try {
    const options = parseArgs(process.argv.slice(2));
    const result = await verifyReleaseAssets(options);
    console.log(JSON.stringify(result, null, 2));
  } catch (error) {
    if (error instanceof ReleaseAssetError) {
      console.error(`[verify-release-assets] FAIL: ${error.code}${error.detail ? ` (${error.detail})` : ""}`);
      process.exit(1);
    }
    console.error(`[verify-release-assets] FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(1);
  }
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : "";
if (invokedPath === fileURLToPath(import.meta.url)) main();

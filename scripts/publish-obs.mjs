#!/usr/bin/env node
/**
 * 将已校验的发布资产上传到对象存储（OBS）。
 *
 * 布局：固定 feed 基地址 + 带版本号的文件名；latest*.yml 与 latest.json 最后发布。
 * CI 用法：
 *   node scripts/publish-obs.mjs --dir release-assets --version 0.1.0-beta.1 --stage artifacts
 *   node scripts/publish-obs.mjs --dir release-assets --version 0.1.0-beta.1 --stage manifests
 *
 * 环境变量：
 *   OBS_ENDPOINT          https://<object-storage-endpoint>
 *   OBS_BUCKET            bucket 名称
 *   OBS_ACCESS_KEY_ID     IAM AK
 *   OBS_SECRET_ACCESS_KEY IAM SK
 *   OBS_SECURITY_TOKEN    可选，仅临时凭证
 *   OBS_PREFIX            可选，对象键前缀（如 releases/）
 *   OBS_FEED_BASE_URL     公开 feed 基地址，必须与 OBS_PREFIX 对应并以 / 结尾
 */
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ObsClient from "esdk-obs-nodejs";
import { verifyReleaseAssets } from "./verify-release-assets.mjs";

const MANIFEST_RE = /^latest(?:-mac|-linux)?\.yml$/;
const IMMUTABLE_CACHE = "public, max-age=31536000, immutable";
const MANIFEST_CACHE = "no-cache";

export class PublishObsError extends Error {
  constructor(code, detail = "", status) {
    super(code);
    this.code = code;
    this.detail = detail;
    this.status = status;
  }
}

function parseArgs(argv) {
  const options = { stage: "all", platform: "win,linux" };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--dry-run") {
      options.dryRun = true;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      console.log(
        "usage: node scripts/publish-obs.mjs --dir <assets> --version <V> " +
          "[--stage artifacts|manifests|all] [--platform win,linux] [--dry-run]",
      );
      process.exit(0);
    }
    if (["--dir", "--version", "--stage", "--platform"].includes(arg)) {
      const value = argv[i + 1];
      if (!value) throw new PublishObsError("argument-missing", arg);
      options[arg.slice(2)] = value;
      i += 1;
      continue;
    }
    throw new PublishObsError("argument-invalid", arg);
  }
  if (!options.dir || !options.version) throw new PublishObsError("argument-missing", "dir/version");
  if (!["artifacts", "manifests", "all"].includes(options.stage)) {
    throw new PublishObsError("stage-invalid", options.stage);
  }
  return options;
}

function requiredEnv(env, name) {
  const value = env[name]?.trim();
  if (!value) throw new PublishObsError("env-missing", name);
  return value;
}

function normalizeBaseUrl(value) {
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new PublishObsError("feed-url-invalid", value);
  }
  if (url.protocol !== "https:") throw new PublishObsError("feed-url-invalid", value);
  if (url.search || url.hash) throw new PublishObsError("feed-url-invalid", value);
  url.pathname = `${url.pathname.replace(/\/+$/, "")}/`;
  return url.href;
}

function normalizePrefix(value) {
  const raw = String(value ?? "").trim().replace(/^\/+/, "");
  if (!raw) return "";
  if (raw.includes("\\") || raw.split("/").some((part) => part === "..")) {
    throw new PublishObsError("prefix-invalid", raw);
  }
  return raw.replace(/\/+$/, "") + "/";
}

function loadConfig(env, { requireCredentials }) {
  const config = {
    endpoint: requiredEnv(env, "OBS_ENDPOINT"),
    bucket: requiredEnv(env, "OBS_BUCKET"),
    feedBaseUrl: normalizeBaseUrl(requiredEnv(env, "OBS_FEED_BASE_URL")),
    prefix: normalizePrefix(env.OBS_PREFIX),
    accessKeyId: env.OBS_ACCESS_KEY_ID?.trim(),
    secretAccessKey: env.OBS_SECRET_ACCESS_KEY?.trim(),
    securityToken: env.OBS_SECURITY_TOKEN?.trim(),
  };
  if (!/^https:\/\//i.test(config.endpoint)) {
    throw new PublishObsError("endpoint-invalid", config.endpoint);
  }
  if (requireCredentials && (!config.accessKeyId || !config.secretAccessKey)) {
    throw new PublishObsError("credentials-missing");
  }
  return config;
}

function listAssets(dir) {
  const root = path.resolve(dir);
  if (!fs.existsSync(root) || !fs.statSync(root).isDirectory()) {
    throw new PublishObsError("assets-dir-missing", root);
  }
  const names = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    if (!entry.isFile()) throw new PublishObsError("asset-not-regular", entry.name);
    if (entry.name === "latest.json") continue;
    names.push(entry.name);
  }
  names.sort();
  return {
    root,
    manifests: names.filter((name) => MANIFEST_RE.test(name)),
    artifacts: names.filter((name) => !MANIFEST_RE.test(name)),
    all: names,
  };
}

function assetSize(file) {
  return fs.statSync(file).size;
}

function assetHash(file, algorithm, encoding) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash(algorithm);
    const stream = fs.createReadStream(file);
    stream.on("data", (chunk) => hash.update(chunk));
    stream.on("error", reject);
    stream.on("end", () => resolve(hash.digest(encoding)));
  });
}

function contentType(name) {
  if (name.endsWith(".yml")) return "text/yaml; charset=utf-8";
  if (name.endsWith(".json")) return "application/json; charset=utf-8";
  if (name.endsWith(".exe")) return "application/vnd.microsoft.portable-executable";
  if (name.endsWith(".AppImage")) return "application/octet-stream";
  if (name.endsWith(".dmg")) return "application/x-apple-diskimage";
  if (name.endsWith(".zip")) return "application/zip";
  return "application/octet-stream";
}

function publicUrl(feedBaseUrl, name) {
  return new URL(name, feedBaseUrl).href;
}

function primaryPlatform(name) {
  if (name.endsWith("-win-x64.exe")) return "win";
  if (name.endsWith("-linux-x64.AppImage")) return "linux";
  const mac = /-mac-(arm64|x64)\.(dmg|zip)$/.exec(name);
  if (mac) return mac[2] === "dmg" ? `mac-${mac[1]}` : `mac-${mac[1]}-zip`;
  return null;
}

async function buildLatestJson({ files, root, version, feedBaseUrl, publishedAt }) {
  const platforms = {};
  for (const name of files) {
    const platform = primaryPlatform(name);
    if (!platform) continue;
    if (platform.endsWith("-zip") && platforms[platform.slice(0, -4)]) continue;
    const file = path.join(root, name);
    platforms[platform.replace(/-zip$/, "")] = {
      url: name,
      size: assetSize(file),
      sha512: await assetHash(file, "sha512", "base64"),
    };
  }
  return `${JSON.stringify(
    {
      schema: 1,
      version,
      published_at: publishedAt,
      feed: feedBaseUrl,
      platforms,
    },
    null,
    2,
  )}\n`;
}

function planFor({ assets, config, stage, version, publishedAt, latestJson }) {
  const prefix = config.prefix;
  const artifacts = assets.artifacts.map((name) => ({
    name,
    key: `${prefix}${name}`,
    url: publicUrl(config.feedBaseUrl, name),
    size: assetSize(path.join(assets.root, name)),
    cacheControl: IMMUTABLE_CACHE,
    contentType: contentType(name),
  }));
  const manifests = assets.manifests.map((name) => ({
    name,
    key: `${prefix}${name}`,
    url: publicUrl(config.feedBaseUrl, name),
    size: assetSize(path.join(assets.root, name)),
    cacheControl: MANIFEST_CACHE,
    contentType: contentType(name),
  }));
  if (latestJson !== undefined) {
    manifests.push({
      name: "latest.json",
      key: `${prefix}latest.json`,
      url: publicUrl(config.feedBaseUrl, "latest.json"),
      size: Buffer.byteLength(latestJson),
      cacheControl: MANIFEST_CACHE,
      contentType: contentType("latest.json"),
    });
  }
  return {
    version,
    publishedAt,
    stage,
    prefix,
    feedBaseUrl: config.feedBaseUrl,
    artifacts,
    manifests,
    uploads: stage === "artifacts" ? artifacts : stage === "manifests" ? manifests : [...artifacts, ...manifests],
  };
}

function obsError(method, result) {
  const common = result?.CommonMsg ?? {};
  return new PublishObsError(
    "obs-http-error",
    `${method} ${common.Status ?? "?"} ${common.Code ?? ""} ${common.Message ?? ""}`.trim(),
    Number(common.Status),
  );
}

function call(client, method, params) {
  return new Promise((resolve, reject) => {
    client[method](params, (error, result) => {
      if (error) return reject(new PublishObsError("obs-client-error", `${method}: ${error}`));
      const status = Number(result?.CommonMsg?.Status);
      if (!Number.isFinite(status)) {
        return reject(new PublishObsError("obs-response-invalid", method));
      }
      if (status >= 300) return reject(obsError(method, result));
      resolve(result);
    });
  });
}

function headObject(client, bucket, key) {
  return new Promise((resolve, reject) => {
    client.getObjectMetadata({ Bucket: bucket, Key: key }, (error, result) => {
      if (error) return reject(new PublishObsError("obs-client-error", `getObjectMetadata: ${error}`));
      const status = Number(result?.CommonMsg?.Status);
      if (status === 404) return resolve(null);
      if (!Number.isFinite(status)) return reject(new PublishObsError("obs-response-invalid", "getObjectMetadata"));
      if (status >= 300) return reject(obsError("getObjectMetadata", result));
      const meta = result.InterfaceResult ?? {};
      return resolve({
        contentLength: Number(meta.ContentLength),
        etag: String(meta.ETag ?? "").replace(/"/g, "").toLowerCase(),
      });
    });
  });
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function withRetry(label, fn, attempts = 3) {
  let last;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      return await fn();
    } catch (error) {
      last = error;
      if (error instanceof PublishObsError && error.status && error.status < 500) throw error;
      if (attempt < attempts - 1) await sleep(1000 * 2 ** attempt);
    }
  }
  throw last;
}

async function uploadArtifact({ client, bucket, item, root, log }) {
  const file = path.join(root, item.name);
  const existing = await headObject(client, bucket, item.key);
  if (existing) {
    const localMd5 = await assetHash(file, "md5", "hex");
    const remoteEtagIsMd5 = existing.etag && !existing.etag.includes("-");
    if (existing.contentLength !== item.size || (remoteEtagIsMd5 && existing.etag !== localMd5)) {
      throw new PublishObsError("obs-object-conflict", item.key);
    }
    log(`[publish-obs] skip ${item.key} (already present)`);
    return { key: item.key, skipped: true };
  }
  await withRetry(`put ${item.key}`, () =>
    call(client, "putObject", {
      Bucket: bucket,
      Key: item.key,
      SourceFile: file,
      ContentType: item.contentType,
      CacheControl: item.cacheControl,
      ContentDisposition: `attachment; filename="${item.name}"`,
    }),
  );
  const after = await headObject(client, bucket, item.key);
  if (!after || after.contentLength !== item.size) {
    throw new PublishObsError("obs-upload-size-mismatch", item.key);
  }
  log(`[publish-obs] uploaded ${item.key} (${item.size} bytes)`);
  return { key: item.key, skipped: false };
}

async function uploadManifest({ client, bucket, item, root, latestJson, log }) {
  const body = item.name === "latest.json" ? Buffer.from(latestJson, "utf8") : null;
  const params = {
    Bucket: bucket,
    Key: item.key,
    ContentType: item.contentType,
    CacheControl: item.cacheControl,
  };
  if (body) params.Body = body;
  else params.SourceFile = path.join(root, item.name);
  await withRetry(`put ${item.key}`, () => call(client, "putObject", params));
  log(`[publish-obs] published ${item.key}`);
  return { key: item.key, skipped: false };
}

function noCacheUrl(url) {
  const next = new URL(url);
  next.searchParams.set("noCache", Date.now().toString(36));
  return next.href;
}

async function verifyPublic(fetchImpl, item, { localBytes } = {}) {
  const response = await fetchImpl(noCacheUrl(item.url), {
    method: localBytes ? "GET" : "HEAD",
    cache: "no-store",
  });
  if (!response.ok) {
    throw new PublishObsError("public-fetch-failed", `${response.status} ${item.url}`);
  }
  if (localBytes) {
    const remote = Buffer.from(await response.arrayBuffer());
    if (!remote.equals(localBytes)) {
      throw new PublishObsError("public-content-mismatch", item.url);
    }
    return;
  }
  const length = Number(response.headers.get("content-length"));
  if (!Number.isFinite(length) || length !== item.size) {
    throw new PublishObsError("public-size-mismatch", item.url);
  }
}

async function verifyLatestJson(fetchImpl, url) {
  const response = await fetchImpl(noCacheUrl(url), { cache: "no-store" });
  if (!response.ok) throw new PublishObsError("public-fetch-failed", `${response.status} ${url}`);
  let doc;
  try {
    doc = JSON.parse(await response.text());
  } catch {
    throw new PublishObsError("latest-json-invalid", url);
  }
  const platforms = doc?.platforms ?? {};
  for (const [platform, entry] of Object.entries(platforms)) {
    if (!entry || typeof entry.url !== "string" || !Number.isSafeInteger(entry.size)) {
      throw new PublishObsError("latest-json-invalid", `${url}#${platform}`);
    }
    await verifyPublic(fetchImpl, { url: new URL(entry.url, url).href, size: entry.size });
  }
  return doc;
}

export async function publishToObs({
  dir,
  version,
  stage = "all",
  platform = "win,linux",
  env = process.env,
  client,
  fetchImpl = globalThis.fetch,
  dryRun = false,
  publishedAt = new Date().toISOString(),
  log = console.log,
} = {}) {
  if (!dir || !version) throw new PublishObsError("argument-missing", "dir/version");
  await verifyReleaseAssets({ dir, version, platform });
  const assets = listAssets(dir);
  if (!assets.manifests.length) throw new PublishObsError("manifest-missing");
  const config = loadConfig(env, { requireCredentials: !client && !dryRun });
  const latestJson =
    stage === "artifacts"
      ? undefined
      : await buildLatestJson({
          files: assets.all,
          root: assets.root,
          version,
          feedBaseUrl: config.feedBaseUrl,
          publishedAt,
        });
  const plan = planFor({ assets, config, stage, version, publishedAt, latestJson });
  if (dryRun) {
    log(`[publish-obs] dry run: ${plan.uploads.length} object(s)`);
    return { dryRun: true, plan };
  }

  const obs =
    client ??
    new ObsClient({
      access_key_id: config.accessKeyId,
      secret_access_key: config.secretAccessKey,
      security_token: config.securityToken,
      server: config.endpoint,
    });
  const uploaded = [];
  try {
    if (stage !== "manifests") {
      for (const item of plan.artifacts) {
        uploaded.push(await uploadArtifact({ client: obs, bucket: config.bucket, item, root: assets.root, log }));
      }
      for (const item of plan.artifacts) {
        await verifyPublic(fetchImpl, item);
      }
    }

    if (stage !== "artifacts") {
      // 切清单前先确认所有安装包已通过公开 URL 可读；否则旧清单继续生效。
      for (const item of plan.artifacts) {
        await verifyPublic(fetchImpl, item);
      }
      for (const item of plan.manifests) {
        uploaded.push(await uploadManifest({ client: obs, bucket: config.bucket, item, root: assets.root, latestJson, log }));
      }
      for (const item of plan.manifests) {
        if (item.name === "latest.json") {
          await verifyLatestJson(fetchImpl, item.url);
        } else {
          await verifyPublic(fetchImpl, item, {
            localBytes: fs.readFileSync(path.join(assets.root, item.name)),
          });
        }
      }
    }

    return { version, stage, uploaded, plan };
  } finally {
    if (!client && typeof obs.close === "function") obs.close();
  }
}

async function main() {
  try {
    const options = parseArgs(process.argv.slice(2));
    const result = await publishToObs({
      dir: options.dir,
      version: options.version,
      stage: options.stage,
      platform: options.platform,
      dryRun: Boolean(options.dryRun),
    });
    console.log(JSON.stringify(result, null, 2));
  } catch (error) {
    if (error instanceof PublishObsError) {
      console.error(`[publish-obs] FAIL: ${error.code}${error.detail ? ` (${error.detail})` : ""}`);
      process.exit(1);
    }
    console.error(`[publish-obs] FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(1);
  }
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : "";
if (invokedPath === fileURLToPath(import.meta.url)) main();

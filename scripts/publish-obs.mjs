#!/usr/bin/env node
/**
 * 将已校验的发布资产上传到对象存储（OBS）。
 *
 * 布局：固定 feed 基地址 + 带版本号的文件名；latest*.yml 与 latest.json 最后发布。
 * CI 用法：
 *   node scripts/publish-obs.mjs --dir release-assets --version 0.1.0-beta.1 --stage artifacts
 *   node scripts/publish-obs.mjs --dir release-assets --version 0.1.0-beta.1 --stage manifests
 *
 * 修复已发布对象的下载文件名（历史上的 Content-Disposition 误带引号，下载会多出下划线）：
 *   node scripts/publish-obs.mjs --version 0.1.0-beta.6 --repair-disposition
 * 目标文件名取自线上 latest.json，只做服务端 copy-to-self 改元数据，不需要本地安装包。
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
    if (arg === "--repair-disposition") {
      options.repairDisposition = true;
      continue;
    }
    if (arg === "--repair-versions") {
      const value = argv[i + 1];
      if (!value) throw new PublishObsError("argument-missing", arg);
      options.repairVersions = value;
      i += 1;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      console.log(
        "usage: node scripts/publish-obs.mjs --dir <assets> --version <V> " +
          "[--stage artifacts|manifests|all] [--platform win,linux] [--dry-run] [--repair-disposition] " +
        "[--repair-versions v0.1.0-beta.1,v0.1.0-beta.2]",
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
  if (!options.version) throw new PublishObsError("argument-missing", "version");
  // 修复模式只读线上清单、只改对象元数据，不需要本地资产目录。
  if (!options.dir && !options.repairDisposition) throw new PublishObsError("argument-missing", "dir");
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

/**
 * Content-Disposition 的 filename 参数。
 *
 * 曾经无条件写成 `filename="<name>"`。下载器少剥一层引号时，那个 `"` 会被当成文件名的一部分；
 * `"` 在 Windows 上是非法字符，浏览器再把它替换成 `_`，用户保存出来就是 `...exe_`。
 * 发布资产名都是纯 ASCII、无空格，按 RFC 6266 可以走不带引号的 token 形式；
 * 只有确实含必须加引号的字符时才加引号并转义。
 */
export function contentDisposition(name) {
  const cleaned = name.replace(/[\r\n]/g, "");
  if (/^[A-Za-z0-9._-]+$/.test(cleaned)) return `attachment; filename=${cleaned}`;
  return `attachment; filename="${cleaned.replace(/["\\]/g, "\\$&")}"`;
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

/** 官网使用的固定安装包名；内容每次发布覆盖，缓存策略见调用方。 */
function fixedAliasName(name) {
  if (name.endsWith("-win-x64.exe")) return "YaoMuStudio-latest-win-x64.exe";
  if (name.endsWith("-linux-x64.AppImage")) return "YaoMuStudio-latest-linux-x64.AppImage";
  const mac = /-mac-(arm64|x64)\.(dmg|zip)$/.exec(name);
  if (mac) return `YaoMuStudio-latest-mac-${mac[1]}.${mac[2]}`;
  return null;
}

async function buildLatestJson({ files, root, version, feedBaseUrl, publishedAt }) {
  const platforms = {};
  for (const name of files) {
    const platform = primaryPlatform(name);
    if (!platform) continue;
    if (platform.endsWith("-zip") && platforms[platform.slice(0, -4)]) continue;
    const file = path.join(root, name);
    const alias = fixedAliasName(name);
    platforms[platform.replace(/-zip$/, "")] = {
      url: name,
      ...(alias ? { latest_url: alias } : {}),
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
  // 官网固定下载别名：服务端复制当前版本化安装包，内容每次发布覆盖。
  const aliases = artifacts
    .filter((item) => fixedAliasName(item.name))
    .map((item) => {
      const name = fixedAliasName(item.name);
      return {
        name,
        sourceName: item.name,
        sourceKey: item.key,
        key: `${prefix}${name}`,
        url: publicUrl(config.feedBaseUrl, name),
        size: item.size,
        cacheControl: MANIFEST_CACHE,
        contentType: contentType(name),
      };
    });
  return {
    version,
    publishedAt,
    stage,
    prefix,
    feedBaseUrl: config.feedBaseUrl,
    artifacts,
    aliases,
    manifests,
    uploads:
      stage === "artifacts"
        ? artifacts
        : stage === "manifests"
          ? [...aliases, ...manifests]
          : [...artifacts, ...aliases, ...manifests],
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

/**
 * 单次 OBS 调用的时间上限。
 *
 * SDK 自带的 socket 超时只覆盖读写停顿；beta.6 发布实际出现过
 * 「33 分钟零输出、一个对象都没传上去」的悬死（连第一次 getObjectMetadata 都没回调）。
 * 这里在应用层兜一层，让 withRetry 能接上重试，最终以明确错误快速失败。
 */
function attemptTimeoutMs() {
  const value = Number(process.env.OBS_ATTEMPT_TIMEOUT_MS ?? "120000");
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function withAttemptTimeout(promise, label) {
  const ms = attemptTimeoutMs();
  if (!ms) return promise;
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(
      () => reject(new PublishObsError("obs-attempt-timeout", `${label} 超过 ${ms}ms 未响应`)),
      ms,
    );
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function call(client, method, params) {
  const pending = new Promise((resolve, reject) => {
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
  return withAttemptTimeout(pending, method);
}

function headObject(client, bucket, key) {
  // 每次重试都要重新发起调用：复用同一个 pending Promise 只会在原位继续挂着。
  const attempt = () =>
    withAttemptTimeout(
      new Promise((resolve, reject) => {
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
      }),
      "getObjectMetadata",
    );
  return withRetry(`head ${key}`, attempt);
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
      ContentDisposition: contentDisposition(item.name),
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

/** 服务端复制版本化安装包到官网固定别名，避免重复上传大文件。 */
async function copyFixedAlias({ client, bucket, item, log }) {
  await withRetry(`copy ${item.key}`, () =>
    call(client, "copyObject", {
      Bucket: bucket,
      Key: item.key,
      CopySource: `${bucket}/${item.sourceKey}`,
      MetadataDirective: "REPLACE",
      ContentType: item.contentType,
      CacheControl: item.cacheControl,
      ContentDisposition: contentDisposition(item.name),
    }),
  );
  log(`[publish-obs] published fixed alias ${item.key} -> ${item.sourceKey}`);
  return { key: item.key, skipped: false };
}

/**
 * 就地修复已发布对象的 Content-Disposition。
 *
 * 版本化安装包的键是不可变的，uploadArtifact 遇到已存在的对象会跳过，
 * 因此头写错的旧版本不会被重新上传覆盖。这里用服务端 copy-to-self + REPLACE 元数据修复，
 * 不产生大文件传输。
 */
function createObsClient(config) {
  return new ObsClient({
    access_key_id: config.accessKeyId,
    secret_access_key: config.secretAccessKey,
    security_token: config.securityToken,
    server: config.endpoint,
  });
}

/**
 * 从线上 latest.json 反推需要修复的对象名。
 *
 * 刻意不复制打包命名规则：文件名以当前公开清单为准，构建侧改名（artifactName、别名）时
 * 这里自动跟着走，不会留下一份会腐化的平行命名表。
 */
async function repairTargets({ config, version, platform, fetchImpl }) {
  const url = noCacheUrl(publicUrl(config.feedBaseUrl, "latest.json"));
  let response;
  try {
    response = await fetchImpl(url);
  } catch (error) {
    throw new PublishObsError("repair-manifest-unavailable", `${url}: ${error}`);
  }
  if (!response.ok) {
    throw new PublishObsError("repair-manifest-unavailable", `${url} -> ${response.status}`, response.status);
  }
  let doc;
  try {
    doc = await response.json();
  } catch (error) {
    throw new PublishObsError("repair-manifest-invalid", `${url}: ${error}`);
  }
  if (doc?.version !== version) {
    throw new PublishObsError("repair-version-mismatch", `latest.json 是 ${doc?.version}，请求的是 ${version}`);
  }

  const targets = [];
  const seen = new Set();
  for (const key of platform.split(",").map((item) => item.trim())) {
    const entry = doc.platforms?.[key];
    if (!entry) continue;
    const names = [entry.url, entry.latest_url];
    if (typeof entry.url === "string" && entry.url.endsWith(".exe")) names.push(`${entry.url}.blockmap`);
    for (const name of names) {
      if (typeof name !== "string" || name.length === 0 || seen.has(name)) continue;
      if (name.includes("/") || name.includes("\\") || name.startsWith(".")) {
        throw new PublishObsError("repair-name-invalid", name);
      }
      seen.add(name);
      targets.push({
        name,
        key: `${config.prefix}${name}`,
        // 与 planFor 一致：版本化安装包长缓存，官网固定别名每次发布覆盖。
        cacheControl: name.startsWith("YaoMuStudio-latest") ? MANIFEST_CACHE : IMMUTABLE_CACHE,
        contentType: contentType(name),
      });
    }
  }
  if (targets.length === 0) throw new PublishObsError("repair-target-empty", `${version}/${platform}`);
  return targets;
}

/**
 * 按打包命名规则为显式版本列表生成修复目标。
 *
 * 只用于历史版本：当前清单指向的版本应走 repairTargets（以线上 latest.json 为准，
 * 构建侧改名时自动跟随）。这里的命名与 desktop/electron-builder.yml 的 artifactName 一致，
 * 不包含 latest 别名——别名每次发布都会被新版本覆盖，无需修历史。
 */
function repairTargetsForVersions({ config, versions, platform }) {
  const targets = [];
  const seen = new Set();
  for (const raw of String(versions).split(",").map((item) => item.trim()).filter(Boolean)) {
    const version = raw.replace(/^v/, "");
    if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
      throw new PublishObsError("repair-version-invalid", raw);
    }
    const names = [];
    if (platform.includes("win")) names.push(`YaoMuStudio-${version}-win-x64.exe`, `YaoMuStudio-${version}-win-x64.exe.blockmap`);
    if (platform.includes("linux")) names.push(`YaoMuStudio-${version}-linux-x64.AppImage`);
    for (const name of names) {
      if (seen.has(name)) continue;
      seen.add(name);
      targets.push({
        name,
        key: `${config.prefix}${name}`,
        cacheControl: IMMUTABLE_CACHE,
        contentType: contentType(name),
      });
    }
  }
  if (targets.length === 0) throw new PublishObsError("repair-target-empty", `${versions}/${platform}`);
  return targets;
}

async function repairDispositionItem({ client, bucket, item, log }) {
  const existing = await headObject(client, bucket, item.key);
  if (!existing) {
    log(`[publish-obs] repair skipped ${item.key} (absent)`);
    return { key: item.key, repaired: false };
  }
  await withRetry(`repair ${item.key}`, () =>
    call(client, "copyObject", {
      Bucket: bucket,
      Key: item.key,
      CopySource: `${bucket}/${item.key}`,
      MetadataDirective: "REPLACE",
      ContentType: item.contentType,
      CacheControl: item.cacheControl,
      ContentDisposition: contentDisposition(item.name),
    }),
  );
  log(`[publish-obs] repaired ${item.key}`);
  return { key: item.key, repaired: true };
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
    // 公开面校验同样不能无限等；AbortSignal.timeout 由运行时实现，假 fetch 会忽略它。
    signal: AbortSignal.timeout(attemptTimeoutMs() || 120000),
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
    if (typeof entry.latest_url === "string" && entry.latest_url.length > 0) {
      await verifyPublic(fetchImpl, { url: new URL(entry.latest_url, url).href, size: entry.size });
    }
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
  repairDisposition = false,
  repairVersions = "",
  publishedAt = new Date().toISOString(),
  log = console.log,
} = {}) {
  if (!version) throw new PublishObsError("argument-missing", "version");
  const config = loadConfig(env, { requireCredentials: !client && !dryRun });

  // 只修复已存在对象的元数据，不上传、不切清单；用于纠正历史版本写错的 Content-Disposition。
  // 目标文件名取自线上 latest.json，因此不需要本地持有安装包。
  if (repairDisposition) {
    const targets = repairVersions
      ? repairTargetsForVersions({ config, versions: repairVersions, platform })
      : await repairTargets({ config, version, platform, fetchImpl });
    if (dryRun) {
      log(`[publish-obs] dry run: repair ${targets.length} object(s)`);
      return { dryRun: true, repair: targets };
    }
    const repairClient = client ?? createObsClient(config);
    const repaired = [];
    for (const item of targets) {
      repaired.push(await repairDispositionItem({ client: repairClient, bucket: config.bucket, item, log }));
    }
    return { repaired };
  }

  if (!dir) throw new PublishObsError("argument-missing", "dir");
  await verifyReleaseAssets({ dir, version, platform });
  const assets = listAssets(dir);
  if (!assets.manifests.length) throw new PublishObsError("manifest-missing");
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

  const obs = client ?? createObsClient(config);

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
      // 官网固定别名先于应用清单更新；别名失败时不切换 latest*.yml。
      for (const item of plan.aliases) {
        uploaded.push(await copyFixedAlias({ client: obs, bucket: config.bucket, item, log }));
      }
      for (const item of plan.aliases) {
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
      repairDisposition: Boolean(options.repairDisposition),
      repairVersions: options.repairVersions ?? "",
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

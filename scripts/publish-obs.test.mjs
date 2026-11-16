import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import fs from "node:fs";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { PublishObsError, publishToObs } from "./publish-obs.mjs";

const VERSION = "0.1.0-beta.1";
const ENV = {
  OBS_ENDPOINT: "https://obs.example.com",
  OBS_BUCKET: "test-bucket",
  OBS_FEED_BASE_URL: "https://dl.example.com/",
};

function sha512(content) {
  return createHash("sha512").update(content).digest("base64");
}

function md5(content) {
  return createHash("md5").update(content).digest("hex");
}

function writeAsset(dir, name, content) {
  writeFileSync(path.join(dir, name), content);
  return { url: name, size: Buffer.byteLength(content), sha512: sha512(content) };
}

function writeManifest(dir, name, files) {
  const lines = [`version: ${VERSION}`, "files:"];
  for (const file of files) {
    lines.push(`  - url: ${file.url}`, `    sha512: ${file.sha512}`, `    size: ${file.size}`);
  }
  lines.push(`path: ${files[0].url}`, `sha512: ${files[0].sha512}`);
  writeFileSync(path.join(dir, name), `${lines.join("\n")}\n`);
}

function makeRelease() {
  const dir = mkdtempSync(path.join(os.tmpdir(), "yaomu-publish-obs-"));
  const exe = writeAsset(dir, `YaoMuStudio-${VERSION}-win-x64.exe`, "windows-installer");
  writeAsset(dir, `YaoMuStudio-${VERSION}-win-x64.exe.blockmap`, "windows-blockmap");
  writeManifest(dir, "latest.yml", [exe]);
  const appimage = writeAsset(dir, `YaoMuStudio-${VERSION}-linux-x64.AppImage`, "linux-appimage");
  writeManifest(dir, "latest-linux.yml", [appimage]);
  return dir;
}

function okResponse(etag) {
  return { CommonMsg: { Status: 200, Code: "NoError" }, InterfaceResult: { ETag: `"${etag}"` } };
}

function fakeObs(initial = new Map()) {
  const objects = new Map(initial);
  const calls = [];
  return {
    calls,
    objects,
    putObject(params, callback) {
      const content = params.SourceFile ? fs.readFileSync(params.SourceFile) : Buffer.from(params.Body);
      const etag = md5(content);
      const key = params.Key;
      objects.set(key, { content, etag });
      calls.push({ method: "putObject", key, cacheControl: params.CacheControl });
      callback(null, okResponse(etag));
    },
    copyObject(params, callback) {
      const sourceKey = params.CopySource.slice(params.CopySource.indexOf("/") + 1);
      const source = objects.get(sourceKey);
      calls.push({ method: "copyObject", key: params.Key, source: sourceKey, cacheControl: params.CacheControl });
      if (!source) {
        callback(null, { CommonMsg: { Status: 404, Code: "NoSuchKey" } });
        return;
      }
      objects.set(params.Key, { content: source.content, etag: source.etag });
      callback(null, okResponse(source.etag));
    },
    getObjectMetadata(params, callback) {
      const object = objects.get(params.Key);
      calls.push({ method: "getObjectMetadata", key: params.Key });
      if (!object) {
        callback(null, { CommonMsg: { Status: 404, Code: "NoSuchKey" } });
        return;
      }
      callback(null, {
        CommonMsg: { Status: 200, Code: "NoError" },
        InterfaceResult: {
          ContentLength: String(object.content.length),
          ETag: `"${object.etag}"`,
        },
      });
    },
    close() {},
  };
}

function fakeFetch(objects, overrides = new Map()) {
  const calls = [];
  const fetchImpl = async (url, init = {}) => {
    calls.push({ url, method: init.method ?? "GET" });
    const name = decodeURIComponent(new URL(url).pathname.replace(/^\//, ""));
    if (overrides.has(name)) {
      return new Response(overrides.get(name), { status: 200 });
    }
    const object = objects.get(name);
    if (!object) return new Response(null, { status: 404 });
    if (init.method === "HEAD") {
      return new Response(null, {
        status: 200,
        headers: { "content-length": String(object.content.length) },
      });
    }
    return new Response(object.content, {
      status: 200,
      headers: { "content-length": String(object.content.length) },
    });
  };
  fetchImpl.calls = calls;
  return fetchImpl;
}

function baseOptions(dir, overrides = {}) {
  return {
    dir,
    version: VERSION,
    platform: "win,linux",
    env: ENV,
    publishedAt: "2026-11-16T00:00:00.000Z",
    log: () => {},
    ...overrides,
  };
}

test("dry run plans artifacts before manifests and needs no credentials", async () => {
  const dir = makeRelease();
  try {
    const result = await publishToObs(baseOptions(dir, { dryRun: true }));
    const names = result.plan.uploads.map((item) => item.name);
    assert.equal(names.at(-1), "latest.json");
    assert.ok(names.includes("latest.yml"));
    assert.ok(names.includes("latest-linux.yml"));
    assert.ok(names.includes(`YaoMuStudio-${VERSION}-win-x64.exe`));
    const manifestStart = names.findIndex((name) => name.startsWith("latest"));
    assert.ok(names.slice(manifestStart).every((name) => name.startsWith("latest")));
    const artifact = result.plan.uploads.find((item) => item.name.endsWith(".exe"));
    assert.equal(artifact.cacheControl, "public, max-age=31536000, immutable");
    assert.equal(artifact.key, `YaoMuStudio-${VERSION}-win-x64.exe`);
    assert.deepEqual(
      result.plan.aliases.map((item) => item.name).sort(),
      ["YaoMuStudio-latest-linux-x64.AppImage", "YaoMuStudio-latest-win-x64.exe"],
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("artifacts stage uploads only installers and verifies public sizes", async () => {
  const dir = makeRelease();
  try {
    const obs = fakeObs();
    const fetchImpl = fakeFetch(obs.objects);
    const result = await publishToObs(
      baseOptions(dir, { stage: "artifacts", client: obs, fetchImpl }),
    );
    const uploaded = obs.calls.filter((call) => call.method === "putObject").map((call) => call.key);
    assert.ok(uploaded.includes(`YaoMuStudio-${VERSION}-win-x64.exe`));
    assert.ok(uploaded.includes(`YaoMuStudio-${VERSION}-linux-x64.AppImage`));
    assert.ok(!uploaded.includes("latest.yml"));
    assert.ok(fetchImpl.calls.every((call) => call.method === "HEAD"));
    assert.equal(result.uploaded.length, uploaded.length);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("manifests stage publishes latest.json last and verifies platform URLs", async () => {
  const dir = makeRelease();
  try {
    const obs = fakeObs();
    const fetchImpl = fakeFetch(obs.objects);
    await publishToObs(baseOptions(dir, { stage: "artifacts", client: obs, fetchImpl }));
    obs.calls.length = 0;
    const result = await publishToObs(
      baseOptions(dir, { stage: "manifests", client: obs, fetchImpl }),
    );
    const puts = obs.calls.filter((call) => call.method === "putObject").map((call) => call.key);
    assert.equal(puts.at(-1), "latest.json");
    assert.ok(puts.includes("latest.yml"));
    assert.ok(puts.includes("latest-linux.yml"));
    const copies = obs.calls.filter((call) => call.method === "copyObject");
    assert.deepEqual(
      copies.map((call) => call.key).sort(),
      ["YaoMuStudio-latest-linux-x64.AppImage", "YaoMuStudio-latest-win-x64.exe"],
    );
    assert.equal(copies[0].cacheControl, "no-cache");
    assert.equal(
      obs.objects.get("YaoMuStudio-latest-win-x64.exe").content.length,
      obs.objects.get(`YaoMuStudio-${VERSION}-win-x64.exe`).content.length,
    );
    const latest = JSON.parse(obs.objects.get("latest.json").content.toString("utf8"));
    assert.equal(latest.version, VERSION);
    assert.equal(latest.platforms.win.url, `YaoMuStudio-${VERSION}-win-x64.exe`);
    assert.equal(latest.platforms.linux.url, `YaoMuStudio-${VERSION}-linux-x64.AppImage`);
    assert.equal(latest.platforms.win.latest_url, "YaoMuStudio-latest-win-x64.exe");
    assert.equal(latest.platforms.linux.latest_url, "YaoMuStudio-latest-linux-x64.AppImage");
    assert.equal(latest.platforms.win.size, obs.objects.get(latest.platforms.win.url).content.length);
    assert.equal(result.uploaded.at(-1).key, "latest.json");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("existing identical artifact is skipped", async () => {
  const dir = makeRelease();
  try {
    const exe = fs.readFileSync(path.join(dir, `YaoMuStudio-${VERSION}-win-x64.exe`));
    const obs = fakeObs(
      new Map([[`YaoMuStudio-${VERSION}-win-x64.exe`, { content: exe, etag: md5(exe) }]]),
    );
    const fetchImpl = fakeFetch(obs.objects);
    const result = await publishToObs(baseOptions(dir, { stage: "artifacts", client: obs, fetchImpl }));
    const exeResult = result.uploaded.find((item) => item.key.endsWith(".exe"));
    assert.equal(exeResult.skipped, true);
    assert.ok(
      !obs.calls.some(
        (call) => call.method === "putObject" && call.key.endsWith(".exe"),
      ),
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("existing artifact with different content fails closed", async () => {
  const dir = makeRelease();
  try {
    const obs = fakeObs(
      new Map([[`YaoMuStudio-${VERSION}-win-x64.exe`, { content: Buffer.from("different"), etag: md5("different") }]]),
    );
    const fetchImpl = fakeFetch(obs.objects);
    await assert.rejects(
      publishToObs(baseOptions(dir, { stage: "artifacts", client: obs, fetchImpl })),
      (error) => error instanceof PublishObsError && error.code === "obs-object-conflict",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("manifests stage refuses to switch when an artifact is not publicly readable", async () => {
  const dir = makeRelease();
  try {
    const obs = fakeObs();
    const fetchImpl = fakeFetch(obs.objects);
    await assert.rejects(
      publishToObs(baseOptions(dir, { stage: "manifests", client: obs, fetchImpl })),
      (error) => error instanceof PublishObsError && error.code === "public-fetch-failed",
    );
    assert.equal(obs.calls.filter((call) => call.method === "putObject").length, 0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("public manifest content mismatch fails closed", async () => {
  const dir = makeRelease();
  try {
    const obs = fakeObs();
    const overrides = new Map([["latest.yml", "tampered"]]);
    const fetchImpl = fakeFetch(obs.objects, overrides);
    await publishToObs(baseOptions(dir, { stage: "artifacts", client: obs, fetchImpl }));
    obs.calls.length = 0;
    await assert.rejects(
      publishToObs(baseOptions(dir, { stage: "manifests", client: obs, fetchImpl })),
      (error) => error instanceof PublishObsError && error.code === "public-content-mismatch",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

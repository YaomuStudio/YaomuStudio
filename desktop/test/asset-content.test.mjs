import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test, { after, before } from "node:test";

const require = createRequire(import.meta.url);
const {
  assetContentUrl,
  downloadAssetContent,
  sanitizeSuggestedName,
  validateAssetSaveInput,
} = require("../dist/ipc/asset-content.js");

const payload = Buffer.from("voice-bytes");
let server;
let baseUrl;
let lastToken;
let redirectHits = 0;
let mode = "ok";
let workDir;

before(async () => {
  workDir = await mkdtemp(path.join(os.tmpdir(), "yaomu-asset-save-"));
  server = http.createServer((req, res) => {
    lastToken = req.headers["x-yaomu-token"];
    if (req.url === "/redirect-target") {
      redirectHits += 1;
      res.statusCode = 200;
      res.end(payload);
      return;
    }
    if (mode === "redirect") {
      res.statusCode = 302;
      res.setHeader("location", "/redirect-target");
      res.end();
      return;
    }
    if (req.url === "/api/v1/assets/asset-1/content") {
      if (mode === "not-found") {
        res.statusCode = 404;
        res.end("not found");
        return;
      }
      if (mode === "empty") {
        res.statusCode = 200;
        res.end();
        return;
      }
      if (mode === "truncated") {
        // 声明比实际发送更长的 body，再断开连接：模拟下载中途失败。
        res.setHeader("content-length", String(payload.length + 64));
        res.write(payload.subarray(0, 4));
        res.destroy();
        return;
      }
      res.setHeader("content-type", "application/octet-stream");
      res.end(payload);
      return;
    }
    res.statusCode = 404;
    res.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  baseUrl = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  await new Promise((resolve) => server.close(resolve));
  await rm(workDir, { recursive: true, force: true });
});

test("validateAssetSaveInput accepts only an opaque asset id", () => {
  assert.deepEqual(validateAssetSaveInput({ assetId: "asset-1" }), { assetId: "asset-1" });
  assert.deepEqual(
    validateAssetSaveInput({ assetId: "asset-1", suggestedName: " voice.wav ", title: " Save " }),
    { assetId: "asset-1", suggestedName: "voice.wav", title: "Save" },
  );
  for (const bad of [
    null,
    [],
    {},
    { assetId: "" },
    { assetId: ".." },
    { assetId: "a/b" },
    { assetId: "a\\b" },
    { assetId: "a\u0000b" },
    { assetId: "x".repeat(201) },
  ]) {
    assert.throws(() => validateAssetSaveInput(bad), /ASSET_SAVE_INPUT_INVALID/);
  }
});

test("sanitizeSuggestedName strips path components and empty values", () => {
  assert.equal(sanitizeSuggestedName("../../voice.wav", "asset-1"), "voice.wav");
  assert.equal(sanitizeSuggestedName("/tmp/ambience.wav", "asset-1"), "ambience.wav");
  assert.equal(sanitizeSuggestedName("C:\\media\\voice.wav", "asset-1"), "voice.wav");
  assert.equal(sanitizeSuggestedName("", "asset-1"), "asset-asset-1");
  assert.equal(sanitizeSuggestedName("..", "asset-1"), "asset-asset-1");
  assert.equal(sanitizeSuggestedName(".", "asset-1"), "asset-asset-1");
  assert.equal(sanitizeSuggestedName("a\u0000b.wav", "asset-1"), "ab.wav");
  assert.equal(sanitizeSuggestedName("a\tb.wav", "asset-1"), "ab.wav");
});

test("long suggested paths keep their file name after sanitizing", () => {
  const nested = `${"x".repeat(300)}/voice.wav`;
  const input = validateAssetSaveInput({ assetId: "asset-1", suggestedName: nested });
  assert.equal(sanitizeSuggestedName(input.suggestedName, "asset-1"), "voice.wav");
});

test("assetContentUrl joins base URL and encodes the asset id", () => {
  assert.equal(
    assetContentUrl("http://127.0.0.1:9000/", "asset-1"),
    "http://127.0.0.1:9000/api/v1/assets/asset-1/content",
  );
  assert.equal(
    assetContentUrl("http://127.0.0.1:9000", "a b"),
    "http://127.0.0.1:9000/api/v1/assets/a%20b/content",
  );
});

test("downloadAssetContent writes the asset and sends the token", async () => {
  mode = "ok";
  const target = path.join(workDir, "voice.wav");
  await downloadAssetContent({
    connection: { baseUrl, token: "test-token" },
    assetId: "asset-1",
    targetPath: target,
  });
  assert.equal(await readFile(target, "utf8"), payload.toString());
  assert.equal(lastToken, "test-token");
  const leftovers = (await readdir(workDir)).filter((name) => name.startsWith(".yaomu-save-"));
  assert.deepEqual(leftovers, []);
});

test("downloadAssetContent leaves no target on HTTP failure", async () => {
  mode = "not-found";
  const target = path.join(workDir, "missing.wav");
  await assert.rejects(
    downloadAssetContent({
      connection: { baseUrl, token: "test-token" },
      assetId: "asset-1",
      targetPath: target,
    }),
    /ASSET_SAVE_HTTP_404/,
  );
  await assert.rejects(readFile(target), /ENOENT/);
});

test("downloadAssetContent rejects an empty body without creating a target", async () => {
  mode = "empty";
  const target = path.join(workDir, "empty.wav");
  await assert.rejects(
    downloadAssetContent({
      connection: { baseUrl, token: "test-token" },
      assetId: "asset-1",
      targetPath: target,
    }),
    /ASSET_SAVE_EMPTY_BODY/,
  );
  await assert.rejects(readFile(target), /ENOENT/);
  const leftovers = (await readdir(workDir)).filter((name) => name.startsWith(".yaomu-save-"));
  assert.deepEqual(leftovers, []);
});

test("downloadAssetContent cleans up when the stream fails mid-transfer", async () => {
  mode = "truncated";
  const target = path.join(workDir, "truncated.wav");
  await assert.rejects(
    downloadAssetContent({
      connection: { baseUrl, token: "test-token" },
      assetId: "asset-1",
      targetPath: target,
    }),
    /ASSET_SAVE_WRITE_FAILED|ASSET_SAVE_FETCH_FAILED/,
  );
  await assert.rejects(readFile(target), /ENOENT/);
  const leftovers = (await readdir(workDir)).filter((name) => name.startsWith(".yaomu-save-"));
  assert.deepEqual(leftovers, []);
});

test("downloadAssetContent does not follow redirects", async () => {
  mode = "redirect";
  redirectHits = 0;
  const target = path.join(workDir, "redirected.wav");
  await assert.rejects(
    downloadAssetContent({
      connection: { baseUrl, token: "test-token" },
      assetId: "asset-1",
      targetPath: target,
    }),
    /ASSET_SAVE_FETCH_FAILED/,
  );
  assert.equal(redirectHits, 0);
  await assert.rejects(readFile(target), /ENOENT/);
});

test("write failures surface a safe code without local paths", async () => {
  mode = "ok";
  const target = path.join(workDir, "target-directory");
  await mkdir(target);
  await assert.rejects(
    downloadAssetContent({
      connection: { baseUrl, token: "test-token" },
      assetId: "asset-1",
      targetPath: target,
    }),
    (error) => {
      assert.equal(error.message, "ASSET_SAVE_WRITE_FAILED");
      assert.equal(error.message.includes(workDir), false);
      return true;
    },
  );
  const leftovers = (await readdir(workDir)).filter((name) => name.startsWith(".yaomu-save-"));
  assert.deepEqual(leftovers, []);
});

test("downloadAssetContent overwrites an existing target", async () => {
  mode = "ok";
  const target = path.join(workDir, "ambience.wav");
  await writeFile(target, "old");
  await downloadAssetContent({
    connection: { baseUrl, token: "test-token" },
    assetId: "asset-1",
    targetPath: target,
  });
  assert.equal(await readFile(target, "utf8"), payload.toString());
});

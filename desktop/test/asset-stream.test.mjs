import assert from "node:assert/strict";
import http from "node:http";
import { createRequire } from "node:module";
import test, { after, before } from "node:test";

const require = createRequire(import.meta.url);
const {
  createAssetStreamHandler,
  assetPlaybackUrl,
  parseAssetUrl,
  sanitizeRange,
} = require("../dist/ipc/asset-stream.js");

const TOKEN = "test-token";
const payload = Buffer.from("0123456789abcdef");

let server;
let baseUrl;
let mode = "ok";
let redirectHits = 0;
let lastRequest;

before(async () => {
  server = http.createServer((req, res) => {
    lastRequest = {
      method: req.method,
      url: req.url,
      range: req.headers.range,
      token: req.headers["x-yaomu-token"],
    };
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
    if (mode === "not-found") {
      res.statusCode = 404;
      res.end("not found");
      return;
    }
    if (mode === "unsatisfiable") {
      res.statusCode = 416;
      res.setHeader("content-range", `bytes */${payload.length}`);
      res.end();
      return;
    }
    res.setHeader("content-type", "video/mp4");
    res.setHeader("accept-ranges", "bytes");
    res.setHeader("cache-control", "no-store");
    res.setHeader("set-cookie", "session=mock");
    if (req.method === "HEAD") {
      res.setHeader("content-length", String(payload.length));
      res.end();
      return;
    }
    const range = req.headers.range;
    if (typeof range === "string") {
      const match = range.match(/^bytes=(\d*)-(\d*)$/);
      if (match) {
        const start = match[1] === "" ? 0 : Number(match[1]);
        const end = match[2] === "" ? payload.length - 1 : Number(match[2]);
        if (start >= payload.length || end < start) {
          res.statusCode = 416;
          res.setHeader("content-range", `bytes */${payload.length}`);
          res.end();
          return;
        }
        const slice = payload.subarray(start, end + 1);
        res.statusCode = 206;
        res.setHeader("content-range", `bytes ${start}-${end}/${payload.length}`);
        res.setHeader("content-length", String(slice.length));
        res.end(slice);
        return;
      }
    }
    res.statusCode = 200;
    res.setHeader("content-length", String(payload.length));
    res.end(payload);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  baseUrl = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  await new Promise((resolve) => server.close(resolve));
});

function request(url, { method = "GET", headers = {} } = {}) {
  return { url, method, headers: new Headers(headers) };
}

function handler() {
  return createAssetStreamHandler(() => ({ baseUrl, token: TOKEN }));
}

test("assetPlaybackUrl only builds the fixed asset host", () => {
  assert.equal(assetPlaybackUrl("asset-1"), "yaomu-asset://asset/asset-1");
  assert.equal(assetPlaybackUrl("a b"), "yaomu-asset://asset/a%20b");
  for (const bad of ["", ".", "..", "a/b", "a\\b", "a\u0000b", "x".repeat(201), null, 42]) {
    assert.equal(assetPlaybackUrl(bad), "");
  }
});

test("parseAssetUrl accepts only a single opaque asset id", () => {
  assert.equal(parseAssetUrl("yaomu-asset://asset/asset-1"), "asset-1");
  assert.equal(parseAssetUrl("yaomu-asset://asset/a%20b"), "a b");
  for (const bad of [
    "",
    "https://asset/asset-1",
    "yaomu-asset://other/asset-1",
    "yaomu-asset://asset/",
    "yaomu-asset://asset/asset-1/extra",
    "yaomu-asset://asset/asset-1?x=1",
    "yaomu-asset://asset/asset-1#frag",
    "yaomu-asset://asset/..",
    "yaomu-asset://asset/a%2Fb",
    "yaomu-asset://asset/a%00b",
    "not a url",
  ]) {
    assert.equal(parseAssetUrl(bad), null, bad);
  }
});

test("sanitizeRange forwards only single byte ranges", () => {
  assert.equal(sanitizeRange("bytes=0-3"), "bytes=0-3");
  assert.equal(sanitizeRange(" bytes=100- "), "bytes=100-");
  assert.equal(sanitizeRange("bytes=-500"), "bytes=-500");
  for (const bad of [
    undefined,
    null,
    "",
    "bytes=",
    "bytes=0-1,3-4",
    "items=0-1",
    "bytes=abc-def",
    "bytes=0-1-2",
    `bytes=0-${"9".repeat(200)}`,
  ]) {
    assert.equal(sanitizeRange(bad), undefined, String(bad));
  }
});

test("GET streams the asset with safe headers and the instance token upstream", async () => {
  mode = "ok";
  const response = await handler()(request("yaomu-asset://asset/asset-1"));
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("content-type"), "video/mp4");
  assert.equal(response.headers.get("accept-ranges"), "bytes");
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.equal(response.headers.get("set-cookie"), null);
  assert.equal(response.headers.get("access-control-allow-origin"), "*");
  assert.equal(response.headers.get("access-control-allow-credentials"), null);
  assert.equal(await response.text(), payload.toString());
  assert.equal(lastRequest.method, "GET");
  assert.equal(lastRequest.token, TOKEN);
  assert.equal(lastRequest.range, undefined);
  assert.equal(lastRequest.url, "/api/v1/assets/asset-1/content");
  const serialized = `${JSON.stringify([...response.headers])}${payload.toString()}`;
  assert.equal(serialized.includes(TOKEN), false);
});

test("Range is forwarded and 206 headers pass through", async () => {
  mode = "ok";
  const response = await handler()(
    request("yaomu-asset://asset/asset-1", { headers: { Range: "bytes=0-3" } }),
  );
  assert.equal(response.status, 206);
  assert.equal(response.headers.get("content-range"), `bytes 0-3/${payload.length}`);
  assert.equal(response.headers.get("accept-ranges"), "bytes");
  assert.equal(await response.text(), "0123");
  assert.equal(lastRequest.range, "bytes=0-3");
});

test("malformed and multi-range requests fall back to the full response", async () => {
  mode = "ok";
  const response = await handler()(
    request("yaomu-asset://asset/asset-1", { headers: { Range: "bytes=0-1,3-4" } }),
  );
  assert.equal(response.status, 200);
  assert.equal(lastRequest.range, undefined);
  await response.text();
});

test("HEAD returns headers without a body", async () => {
  mode = "ok";
  const response = await handler()(
    request("yaomu-asset://asset/asset-1", { method: "HEAD" }),
  );
  assert.equal(response.status, 200);
  assert.equal(response.body, null);
  assert.equal(response.headers.get("content-length"), String(payload.length));
  assert.equal(response.headers.get("access-control-allow-origin"), "*");
  assert.equal(lastRequest.method, "HEAD");
});

test("upstream status codes pass through", async () => {
  mode = "not-found";
  const missing = await handler()(request("yaomu-asset://asset/asset-1"));
  assert.equal(missing.status, 404);
  await missing.text();

  mode = "unsatisfiable";
  const unsatisfiable = await handler()(
    request("yaomu-asset://asset/asset-1", { headers: { Range: "bytes=999-1000" } }),
  );
  assert.equal(unsatisfiable.status, 416);
  assert.equal(unsatisfiable.headers.get("content-range"), `bytes */${payload.length}`);
  await unsatisfiable.text();
  mode = "ok";
});

test("Core not ready returns 503 without contacting upstream", async () => {
  lastRequest = undefined;
  const response = await createAssetStreamHandler(() => null)(
    request("yaomu-asset://asset/asset-1"),
  );
  assert.equal(response.status, 503);
  assert.equal(lastRequest, undefined);
  const body = await response.text();
  assert.match(body, /CORE_NOT_READY/);
  assert.equal(response.headers.get("access-control-allow-origin"), "*");
});

test("invalid method and URL are rejected before Core access", async () => {
  lastRequest = undefined;
  const post = await handler()(
    request("yaomu-asset://asset/asset-1", { method: "POST" }),
  );
  assert.equal(post.status, 405);
  assert.equal(post.headers.get("access-control-allow-origin"), "*");
  assert.equal(post.headers.get("access-control-allow-credentials"), null);
  await post.text();

  const invalid = await handler()(request("yaomu-asset://asset/a%2Fb"));
  assert.equal(invalid.status, 400);
  assert.equal(invalid.headers.get("access-control-allow-origin"), "*");
  await invalid.text();
  assert.equal(lastRequest, undefined);
});

test("redirects are not followed", async () => {
  mode = "redirect";
  redirectHits = 0;
  const response = await handler()(request("yaomu-asset://asset/asset-1"));
  assert.equal(response.status, 502);
  await response.text();
  assert.equal(redirectHits, 0);
  mode = "ok";
});

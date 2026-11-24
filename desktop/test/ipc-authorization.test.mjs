import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, readFileSync, renameSync, rmSync, symlinkSync, unlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test, { after, before } from "node:test";

const require = createRequire(import.meta.url);
const { createApiInvoker, createAuthorizedApiHandler } = require("../dist/ipc/api.js");
const { DesktopAuthorization, WindowPathGrants, isApplicationURL } = require("../dist/ipc/authorization.js");
const { registerDesktopSettingsIpc } = require("../dist/ipc/settings.js");

const appURL = "file:///app/ui/dist/index.html";
function sender(id = 1) {
  const contents = {
    id,
    mainFrame: { url: appURL },
    getURL() { return this.mainFrame.url; },
    isDestroyed() { return false; },
  };
  return { contents, event: { sender: contents, senderFrame: contents.mainFrame } };
}

let root;
let selected;
let other;
let output;
before(() => {
  root = mkdtempSync(path.join(os.tmpdir(), "yaomu-ipc-auth-"));
  output = path.join(root, "output");
  mkdirSync(output);
  selected = path.join(root, "selected.mp4");
  other = path.join(root, "unselected.mp4");
  writeFileSync(selected, "selected");
  writeFileSync(other, "other");
});
after(() => rmSync(root, { recursive: true, force: true }));

// 旧版 invoke 只检查方法名，会把 Renderer 构造的宿主路径直接转发到 Core。
test("import/export fail closed without a window path authorization", async () => {
  let forwarded = 0;
  const invoke = createApiInvoker(
    () => ({ baseUrl: "http://127.0.0.1:1234", token: "test-instance" }),
    async () => {
      forwarded += 1;
      return Response.json({ asset_ids: ["asset-1"] });
    },
  );
  for (const [method, input] of [
    ["importAssets", { project_id: "proj-1", file_grants: ["/unselected/source.mp4"], copy_mode: false }],
    ["export", { project_id: "proj-1", output_dir_grant: "/unselected/output" }],
  ]) {
    const result = await invoke(method, [input]);
    assert.equal(result.ok, false);
    assert.equal(result.error.code, "LOCAL_ACCESS_DENIED");
  }
  assert.equal(forwarded, 0);
});

test("application URL accepts hash routing and rejects prefix, credentials, query and other files", () => {
  assert.equal(isApplicationURL(`${appURL}#/projects`, appURL), true);
  assert.equal(isApplicationURL("http://localhost:5173/#/projects", "http://localhost:5173"), true);
  for (const url of [
    "file:///app/ui/dist/other.html",
    `${appURL}?untrusted=1`,
    `${appURL}/child`,
    "https://untrusted.example/",
    "data:text/html,app",
  ]) assert.equal(isApplicationURL(url, appURL), false, url);
  for (const url of [
    "http://localhost:5173.evil.test/",
    "http://localhost:51730/",
    "http://localhost:5173@evil.test/",
    "http://localhost:5173/other",
  ]) assert.equal(isApplicationURL(url, "http://localhost:5173"), false, url);
});

test("percent-encoded unreserved characters identify the same document", () => {
  // Node pathToFileURL 生成 %7E，Chromium getURL 保留 ~（Windows 短路径 RUNNER~1）。
  assert.equal(
    isApplicationURL(
      "file:///C:/Users/RUNNER~1/app/index.html#/workbench",
      "file:///C:/Users/RUNNER%7E1/app/index.html",
    ),
    true,
  );
  assert.equal(
    isApplicationURL("file:///C:/Users/RUNNER%7E2/app/index.html", "file:///C:/Users/RUNNER~1/app/index.html"),
    false,
  );
  // %2F 是保留字符，不能与路径分隔符混同。
  assert.equal(isApplicationURL("file:///C:/app%2Fchild.html", "file:///C:/app/child.html"), false);
});

test("only registered contents and its actual top frame can use IPC", () => {
  const authorization = new DesktopAuthorization();
  const { contents, event } = sender();
  authorization.registerWindow(contents, appURL);
  const current = authorization.requireSender(event);
  assert.equal(current.sender, contents);
  assert.throws(() => authorization.requireSender({ ...event, senderFrame: null }), /LOCAL_ACCESS_DENIED/);
  assert.throws(() => authorization.requireSender({ ...event, senderFrame: { url: appURL } }), /LOCAL_ACCESS_DENIED/);
  const spoofed = sender();
  assert.throws(() => authorization.requireSender(spoofed.event), /LOCAL_ACCESS_DENIED/);
  contents.mainFrame.url = "https://untrusted.example/";
  assert.throws(() => authorization.requireSender(event), /LOCAL_ACCESS_DENIED/);
  contents.mainFrame.url = appURL;
  contents.isDestroyed = () => true;
  assert.throws(() => authorization.requireSender(event), /LOCAL_ACCESS_DENIED/);
  contents.isDestroyed = () => false;
  authorization.resetWindow(contents);
  assert.throws(() => authorization.assertCurrent(event, current), /LOCAL_ACCESS_DENIED/);
  authorization.removeWindow(contents);
  assert.throws(() => authorization.requireSender(event), /LOCAL_ACCESS_DENIED/);
});

test("file and directory grants are exact, separate and local to a window", () => {
  const authorization = new DesktopAuthorization();
  const a = sender(1);
  const b = sender(2);
  authorization.registerWindow(a.contents, appURL);
  authorization.registerWindow(b.contents, appURL);
  const grants = authorization.requireSender(a.event).paths;
  grants.grantInputFiles([selected]);
  grants.grantOutputDirectory(output);
  assert.doesNotThrow(() => grants.assertApiPaths("importAssets", [{ file_grants: [selected] }]));
  assert.doesNotThrow(() => grants.assertApiPaths("export", [{ output_dir_grant: output }]));
  assert.equal(grants.assertOpenPath(selected), selected);
  assert.equal(grants.assertOpenPath(output), output);
  for (const files of [[other], [selected, other], [output], ["relative.mp4"], ["/tmp/a\u0000b"], []]) {
    assert.throws(() => grants.assertApiPaths("importAssets", [{ file_grants: files }]), /LOCAL_ACCESS_DENIED/);
  }
  assert.throws(() => grants.assertApiPaths("export", [{ output_dir_grant: root }]), /LOCAL_ACCESS_DENIED/);
  assert.throws(() => grants.assertOpenPath(root), /LOCAL_ACCESS_DENIED/);
  assert.throws(() => authorization.requireSender(b.event).paths.assertApiPaths("importAssets", [
    { file_grants: [selected] },
  ]), /LOCAL_ACCESS_DENIED/);
  authorization.resetWindow(a.contents);
  assert.throws(() => authorization.requireSender(a.event).paths.assertOpenPath(selected), /LOCAL_ACCESS_DENIED/);
});

test("a swapped file, symlink or directory cannot retain the previous grant", () => {
  const grants = new WindowPathGrants();
  const link = path.join(root, "linked.mp4");
  symlinkSync(selected, link);
  grants.grantInputFiles([link]);
  unlinkSync(link);
  symlinkSync(other, link);
  assert.throws(() => grants.assertApiPaths("importAssets", [{ file_grants: [link] }]), /LOCAL_ACCESS_DENIED/);

  const original = path.join(root, "replaced.mp4");
  const replacement = path.join(root, "replacement.mp4");
  writeFileSync(original, "original");
  writeFileSync(replacement, "replacement");
  grants.grantInputFiles([original]);
  renameSync(replacement, original);
  assert.throws(() => grants.assertOpenPath(original), /LOCAL_ACCESS_DENIED/);

  const dirLink = path.join(root, "output-link");
  symlinkSync(output, dirLink, "dir");
  grants.grantOutputDirectory(dirLink);
  unlinkSync(dirLink);
  symlinkSync(root, dirLink, "dir");
  assert.throws(() => grants.assertApiPaths("export", [{ output_dir_grant: dirLink }]), /LOCAL_ACCESS_DENIED/);
});

test("authorized API forwards existing path strings and refuses untrusted sources before fetch", async () => {
  const authorization = new DesktopAuthorization();
  const { contents, event } = sender();
  authorization.registerWindow(contents, appURL);
  const grants = authorization.requireSender(event).paths;
  grants.grantInputFiles([selected]);
  grants.grantOutputDirectory(output);
  const requests = [];
  const handler = createAuthorizedApiHandler(createApiInvoker(
    () => ({ baseUrl: "http://127.0.0.1:1234", token: "test-instance" }),
    async (url, options) => {
      requests.push({ url, ...options });
      return Response.json({ asset_ids: ["asset-1"] });
    },
  ), authorization);
  const input = { project_id: "proj-1", file_grants: [selected], copy_mode: false };
  assert.equal((await handler(event, "importAssets", [input])).ok, true);
  assert.deepEqual(JSON.parse(requests[0].body), input);
  assert.equal(requests[0].redirect, "error");
  const invalidFrame = { ...event, senderFrame: { url: appURL } };
  assert.equal((await handler(invalidFrame, "getSystem", [])).error.code, "LOCAL_ACCESS_DENIED");
  assert.equal((await handler(event, "importAssets", [{ ...input, file_grants: [other] }])).error.code,
    "LOCAL_ACCESS_DENIED");
  assert.equal(requests.length, 1);
});

test("only defined Core project/export response fields authorize opening directories", () => {
  const grants = new WindowPathGrants();
  grants.rememberCorePaths("getTask", { directory: root, output_path: other });
  assert.throws(() => grants.assertOpenPath(root), /LOCAL_ACCESS_DENIED/);
  grants.rememberCorePaths("getProject", { directory: root });
  assert.equal(grants.assertOpenPath(root), root);
  assert.throws(() => grants.assertApiPaths("export", [{ output_dir_grant: root }]), /LOCAL_ACCESS_DENIED/);
  assert.throws(() => grants.assertApiPaths("importAssets", [{ file_grants: [selected] }]), /LOCAL_ACCESS_DENIED/);
  grants.rememberCorePaths("listExports", [{ output_path: path.join(output, "result.srt") }]);
  assert.equal(grants.assertOpenPath(output), output);
  assert.throws(() => grants.assertOpenPath(path.join(output, "result.srt")), /LOCAL_ACCESS_DENIED/);
});

test("an API reply after navigation cannot grant paths to the next document", async () => {
  const authorization = new DesktopAuthorization();
  const { contents, event } = sender();
  authorization.registerWindow(contents, appURL);
  let finish;
  const handler = createAuthorizedApiHandler(createApiInvoker(
    () => ({ baseUrl: "http://127.0.0.1:1234", token: "test-instance" }),
    () => new Promise((resolve) => { finish = resolve; }),
  ), authorization);
  const pending = handler(event, "getProject", ["proj-1"]);
  authorization.resetWindow(contents);
  finish(Response.json({ id: "proj-1", directory: root }));
  assert.equal((await pending).error.code, "LOCAL_ACCESS_DENIED");
  assert.throws(() => authorization.requireSender(event).paths.assertOpenPath(root), /LOCAL_ACCESS_DENIED/);
});

const settingsChannels = [
  ["yaomu:settings:set-close-behavior", "quit"],
  ["yaomu:settings:set-auto-check-updates", true],
  ["yaomu:settings:get-auto-check-updates", undefined],
  ["yaomu:settings:set-auto-download-updates", true],
  ["yaomu:settings:get-auto-download-updates", undefined],
  ["yaomu:settings:get-onboarding-completed", undefined],
  ["yaomu:settings:set-onboarding-completed", undefined],
  ["yaomu:settings:set-ui-scale", 1.25],
];

function settingsHarness() {
  const handlers = new Map();
  const calls = {
    get: 0,
    close: [],
    auto: [],
    download: [],
    downloadGet: 0,
    onboardGet: 0,
    onboardSet: 0,
    uiScale: [],
  };
  const state = {
    autoCheckUpdates: false,
    autoDownloadUpdates: false,
    onboardingCompleted: null,
  };
  const authorization = new DesktopAuthorization();
  const target = sender();
  authorization.registerWindow(target.contents, appURL);
  registerDesktopSettingsIpc(
    { handle(channel, listener) { handlers.set(channel, listener); } },
    authorization,
    {
      getAutoCheckUpdates() { calls.get += 1; return state.autoCheckUpdates; },
      getAutoDownloadUpdates() { calls.downloadGet += 1; return state.autoDownloadUpdates; },
      getOnboardingCompleted() { calls.onboardGet += 1; return state.onboardingCompleted; },
      applyOnboardingCompleted() {
        calls.onboardSet += 1;
        state.onboardingCompleted = true;
      },
      applyCloseBehavior(value) { calls.close.push(value); },
      applyAutoCheckUpdates(value) { calls.auto.push(value); state.autoCheckUpdates = value; },
      applyAutoDownloadUpdates(value) {
        calls.download.push(value);
        state.autoDownloadUpdates = value;
      },
      applyUiScale(contents, value) { calls.uiScale.push([contents.id, value]); },
    },
  );
  const invoke = (channel, event, ...args) => handlers.get(channel)(event, ...args);
  return { invoke, calls, state, target };
}

test("settings IPC rejects untrusted sources before any read or write", () => {
  const { invoke, calls, state, target } = settingsHarness();
  const { contents, event } = target;
  const denied = (fake, label) => {
    for (const [channel, value] of settingsChannels) {
      assert.throws(() => invoke(channel, fake, value), /LOCAL_ACCESS_DENIED/, `${label}: ${channel}`);
    }
  };

  denied(sender(99).event, "unregistered window");
  denied(sender(contents.id).event, "forged contents with the same id");
  denied({ sender: contents, senderFrame: { url: appURL } }, "child frame");

  contents.isDestroyed = () => true;
  denied(event, "destroyed contents");
  contents.isDestroyed = () => false;

  contents.mainFrame.url = "https://untrusted.example/";
  denied(event, "navigated document");
  contents.mainFrame.url = appURL;

  assert.deepEqual(calls, {
    get: 0,
    close: [],
    auto: [],
    download: [],
    downloadGet: 0,
    onboardGet: 0,
    onboardSet: 0,
    uiScale: [],
  });
  assert.equal(state.autoCheckUpdates, false);
  assert.equal(state.autoDownloadUpdates, false);
  assert.equal(state.onboardingCompleted, null);
});

test("settings IPC allows the registered top-level window and keeps value semantics", () => {
  const { invoke, calls, state, target } = settingsHarness();
  const { contents } = target;
  assert.equal(invoke("yaomu:settings:get-auto-check-updates", target.event), false);
  assert.equal(calls.get, 1);

  invoke("yaomu:settings:set-close-behavior", target.event, "minimize");
  assert.deepEqual(calls.close, ["minimize"]);
  assert.throws(() => invoke("yaomu:settings:set-close-behavior", target.event, "hide"), /closeBehavior 不合法/);
  assert.deepEqual(calls.close, ["minimize"]);

  invoke("yaomu:settings:set-auto-check-updates", target.event, true);
  assert.deepEqual(calls.auto, [true]);
  assert.equal(state.autoCheckUpdates, true);
  assert.equal(invoke("yaomu:settings:get-auto-check-updates", target.event), true);
  assert.throws(() => invoke("yaomu:settings:set-auto-check-updates", target.event, "true"),
    /autoCheckUpdates 不合法/);
  assert.deepEqual(calls.auto, [true]);
  assert.equal(state.autoCheckUpdates, true);

  assert.equal(invoke("yaomu:settings:get-auto-download-updates", target.event), false);
  assert.equal(calls.downloadGet, 1);
  invoke("yaomu:settings:set-auto-download-updates", target.event, true);
  assert.deepEqual(calls.download, [true]);
  assert.equal(state.autoDownloadUpdates, true);
  assert.equal(invoke("yaomu:settings:get-auto-download-updates", target.event), true);
  assert.throws(
    () => invoke("yaomu:settings:set-auto-download-updates", target.event, "true"),
    /autoDownloadUpdates 不合法/,
  );
  assert.deepEqual(calls.download, [true]);
  assert.equal(state.autoDownloadUpdates, true);

  // 引导状态：null 表示未设置；set 触发写入回调后 get 返回已完成。
  assert.equal(invoke("yaomu:settings:get-onboarding-completed", target.event), null);
  assert.equal(calls.onboardGet, 1);
  invoke("yaomu:settings:set-onboarding-completed", target.event);
  assert.equal(calls.onboardSet, 1);
  assert.equal(state.onboardingCompleted, true);
  assert.equal(invoke("yaomu:settings:get-onboarding-completed", target.event), true);
  assert.equal(calls.onboardGet, 2);

  invoke("yaomu:settings:set-ui-scale", target.event, 1.25);
  assert.deepEqual(calls.uiScale, [[contents.id, 1.25]]);
  for (const invalid of [0.5, 1.6, "1.25", Number.NaN, Infinity]) {
    assert.throws(() => invoke("yaomu:settings:set-ui-scale", target.event, invalid),
      /uiScale 不合法/, String(invalid));
  }
  assert.deepEqual(calls.uiScale, [[contents.id, 1.25]]);
});

test("main registers the shared settings handlers with the real ipcMain and authorization", () => {
  const main = readFileSync(new URL("../dist/main.js", import.meta.url), "utf8");
  assert.match(main, /registerDesktopSettingsIpc\)\((?:\w+\.)?ipcMain,\s*authorization\b/);
  assert.doesNotMatch(main, /ipcMain\.handle\(\s*["']yaomu:settings:/);
});

test("electron-builder excludes the dev-only high-privilege probe from the package", () => {
  const builder = readFileSync(new URL("../electron-builder.yml", import.meta.url), "utf8");
  const files = builder.slice(builder.indexOf("files:"), builder.indexOf("asar:"));
  const include = files.indexOf("dist/**/*");
  const exclude = files.indexOf("!dist/smoke-probe.js");
  assert.ok(include >= 0, "dist inclusion rule missing");
  assert.ok(exclude > include, "probe exclusion must follow the dist include rule");
  assert.ok(files.indexOf("!dist/smoke-probe.js.map") > include, "probe source map exclusion missing");
});

test("electron-builder enables asar and pins the hardening fuses", () => {
  const builder = readFileSync(new URL("../electron-builder.yml", import.meta.url), "utf8");
  assert.match(builder, /^asar:\s*true$/m, "asar must stay enabled");
  const start = builder.indexOf("electronFuses:");
  assert.ok(start >= 0, "electronFuses section missing");
  const section = builder.slice(start, builder.indexOf("extraResources:", start));
  const expected = {
    runAsNode: false,
    enableNodeOptionsEnvironmentVariable: false,
    enableNodeCliInspectArguments: false,
    enableEmbeddedAsarIntegrityValidation: true,
    onlyLoadAppFromAsar: true,
  };
  for (const [key, value] of Object.entries(expected)) {
    assert.match(section, new RegExp(`^\\s+${key}:\\s+${value}\\s*$`, "m"), key);
  }
});

test("smoke runs the high-privilege IPC probe only in unpackaged builds", () => {
  const smoke = readFileSync(new URL("../dist/smoke.js", import.meta.url), "utf8");
  assert.doesNotMatch(smoke, /nodeIntegration:\s*true/, "smoke still embeds a high-privilege window");
  const guard = smoke.indexOf("app.isPackaged");
  const probeImport = smoke.indexOf("./smoke-probe");
  assert.ok(guard >= 0 && probeImport > guard, "probe import must sit behind !app.isPackaged");
  assert.match(smoke, /not-run/, "packaged smoke must report the probe as not-run");

  const probe = readFileSync(new URL("../dist/smoke-probe.js", import.meta.url), "utf8");
  assert.match(probe, /nodeIntegration:\s*true/);
  assert.match(probe, /sandbox:\s*false/);
});

test("renderer permissions default-deny except trusted top-level fullscreen", () => {
  const { defaultDenyRendererPermissions } = require("../dist/permissions.js");
  const authorization = new DesktopAuthorization();
  const top = sender();
  authorization.registerWindow(top.contents, appURL);

  const handlers = {};
  const fakeSession = {
    setPermissionRequestHandler(handler) { handlers.request = handler; },
    setPermissionCheckHandler(handler) { handlers.check = handler; },
  };
  defaultDenyRendererPermissions(authorization, fakeSession);
  assert.equal(typeof handlers.request, "function");
  assert.equal(typeof handlers.check, "function");

  const allowed = { isMainFrame: true, requestingUrl: appURL };
  const request = (contents, permission, details) => {
    let granted;
    handlers.request(contents, permission, (value) => { granted = value; }, details);
    return granted;
  };

  assert.equal(request(top.contents, "fullscreen", allowed), true);
  assert.equal(handlers.check(top.contents, "fullscreen", appURL, allowed), true);

  const denied = [
    [top.contents, "fullscreen", { ...allowed, isMainFrame: false }, "subframe"],
    [null, "fullscreen", allowed, "null contents"],
    [sender(99).contents, "fullscreen", allowed, "unregistered contents"],
    [sender(top.contents.id).contents, "fullscreen", allowed, "forged contents with same id"],
    [top.contents, "automatic-fullscreen", allowed, "automatic fullscreen"],
    [top.contents, "media", allowed, "media"],
    [top.contents, "microphone", allowed, "microphone"],
    [top.contents, "notifications", allowed, "notifications"],
    [top.contents, "fullscreen", { isMainFrame: true }, "missing requestingUrl"],
    [top.contents, "fullscreen", undefined, "missing details"],
    [top.contents, "fullscreen", { ...allowed, requestingUrl: "https://untrusted.example/" }, "wrong requestingUrl"],
  ];
  for (const [contents, permission, details, label] of denied) {
    assert.equal(request(contents, permission, details), false, `request: ${label}`);
    assert.equal(handlers.check(contents, permission, appURL, details), false, `check: ${label}`);
  }

  top.contents.mainFrame.url = "https://untrusted.example/";
  assert.equal(request(top.contents, "fullscreen", allowed), false, "navigated request");
  assert.equal(handlers.check(top.contents, "fullscreen", appURL, allowed), false, "navigated check");
  top.contents.mainFrame.url = appURL;
  top.contents.isDestroyed = () => true;
  assert.equal(handlers.check(top.contents, "fullscreen", appURL, allowed), false, "destroyed contents");
  top.contents.isDestroyed = () => false;

  const main = readFileSync(new URL("../dist/main.js", import.meta.url), "utf8");
  assert.match(main, /defaultDenyRendererPermissions\)\(authorization,\s*(?:\w+\.)?session\.defaultSession/);
  assert.ok(
    main.indexOf("defaultDenyRendererPermissions") < main.indexOf("const win = createWindow()"),
    "permission handlers must be installed before the window is created/loaded",
  );
});

test("production build injects a local-only CSP meta and serve keeps HMR unrestricted", async () => {
  const { PRODUCTION_CSP, productionCspPlugin } = await import("../../ui/csp-policy.mjs");

  const buildTags = productionCspPlugin("build").transformIndexHtml();
  assert.equal(buildTags.length, 1);
  assert.equal(buildTags[0].tag, "meta");
  assert.equal(buildTags[0].attrs["http-equiv"], "Content-Security-Policy");
  assert.equal(buildTags[0].attrs.content, PRODUCTION_CSP);
  assert.deepEqual(productionCspPlugin("serve").transformIndexHtml(), []);

  for (const directive of [
    "default-src 'none'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "frame-src 'none'",
  ]) {
    assert.ok(PRODUCTION_CSP.includes(directive), directive);
  }
  const scriptSrc = /script-src[^;]*/.exec(PRODUCTION_CSP)?.[0] ?? "";
  assert.doesNotMatch(scriptSrc, /unsafe-inline|unsafe-eval/);
  assert.doesNotMatch(PRODUCTION_CSP, /\*|https?:/, "production CSP must not allow remote or wildcard sources");
  assert.doesNotMatch(PRODUCTION_CSP, /frame-ancestors/, "meta CSP cannot enforce header-only directives");

  const previous = process.env.NODE_ENV;
  process.env.NODE_ENV = "development";
  try {
    // 只看 command：NODE_ENV=development 的 build 仍必须注入 CSP。
    assert.equal(productionCspPlugin("build").transformIndexHtml().length, 1);
  } finally {
    if (previous === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = previous;
  }

  const viteConfig = readFileSync(new URL("../../ui/vite.config.ts", import.meta.url), "utf8");
  assert.match(viteConfig, /productionCspPlugin\(command\)/);
});

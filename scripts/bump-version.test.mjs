import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { bumpVersion, compareVersions, BumpVersionError } from "./bump-version.mjs";

const FROM = "0.1.0-beta.6";
const TO = "0.1.0-beta.7";

function makeFixture({ readmeVersion = FROM } = {}) {
  const root = mkdtempSync(path.join(os.tmpdir(), "yaomu-bump-"));
  for (const dir of ["", "ui", "desktop"]) {
    const target = dir ? path.join(root, dir) : root;
    mkdirSync(target, { recursive: true });
    writeFileSync(path.join(target, "package.json"), `${JSON.stringify({ name: dir || "yaomustudio", version: FROM }, null, 2)}\n`);
  }
  writeFileSync(
    path.join(root, "package-lock.json"),
    `${JSON.stringify(
      {
        name: "yaomustudio",
        version: FROM,
        lockfileVersion: 3,
        packages: {
          "": { name: "yaomustudio", version: FROM },
          ui: { name: "ui", version: FROM },
          desktop: { name: "desktop", version: FROM },
          "node_modules/foo": { version: "1.2.3" },
        },
      },
      null,
      2,
    )}\n`,
  );
  for (const rel of ["README.md", "README.zh-CN.md"]) {
    writeFileSync(
      path.join(root, rel),
      [
        `  <img alt="Release" src="https://img.shields.io/badge/release-v${readmeVersion}-6b7280">`,
        `| Windows x64 | \`YaoMuStudio-${readmeVersion}-win-x64.exe\` | NSIS |`,
        `| Linux x64 | \`YaoMuStudio-${readmeVersion}-linux-x64.AppImage\` | preview |`,
        "",
      ].join("\n"),
    );
  }
  return root;
}

function read(root, rel) {
  return readFileSync(path.join(root, rel), "utf8");
}

test("同步三处 package.json、锁文件与两份 README", (t) => {
  const root = makeFixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));

  const result = bumpVersion({ root, version: TO });
  assert.equal(result.from, FROM);
  assert.equal(result.to, TO);

  for (const rel of ["package.json", "ui/package.json", "desktop/package.json"]) {
    assert.equal(JSON.parse(read(root, rel)).version, TO, rel);
  }

  const lock = JSON.parse(read(root, "package-lock.json"));
  assert.equal(lock.version, TO);
  for (const key of ["", "ui", "desktop"]) assert.equal(lock.packages[key].version, TO, key);
  assert.equal(lock.packages["node_modules/foo"].version, "1.2.3", "第三方依赖版本不得被改");

  for (const rel of ["README.md", "README.zh-CN.md"]) {
    const text = read(root, rel);
    assert.ok(text.includes(`release-v${TO}`), `${rel}: 版本徽标未更新`);
    assert.ok(text.includes(`YaoMuStudio-${TO}-win-x64.exe`), `${rel}: Windows 资产名未更新`);
    assert.ok(text.includes(`YaoMuStudio-${TO}-linux-x64.AppImage`), `${rel}: Linux 资产名未更新`);
    assert.ok(!text.includes(FROM), `${rel}: 仍残留旧版本号`);
  }
});

test("升级后 check-readme 的约束依然成立", (t) => {
  const root = makeFixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  bumpVersion({ root, version: TO });

  const pkgVersion = JSON.parse(read(root, "package.json")).version;
  for (const rel of ["README.md", "README.zh-CN.md"]) {
    const text = read(root, rel);
    for (const asset of [`YaoMuStudio-${pkgVersion}-win-x64.exe`, `YaoMuStudio-${pkgVersion}-linux-x64.AppImage`]) {
      assert.ok(text.includes(asset), `${rel}: 缺少 ${asset}`);
    }
  }
});

test("README 与 package.json 脱节时整体中止，不落半成品", (t) => {
  const root = makeFixture({ readmeVersion: "0.1.0-beta.5" });
  t.after(() => rmSync(root, { recursive: true, force: true }));

  assert.throws(() => bumpVersion({ root, version: TO }), BumpVersionError);
  for (const rel of ["package.json", "ui/package.json", "desktop/package.json"]) {
    assert.equal(JSON.parse(read(root, rel)).version, FROM, `${rel} 不应被改动`);
  }
  assert.equal(JSON.parse(read(root, "package-lock.json")).version, FROM);
  assert.ok(read(root, "README.md").includes("0.1.0-beta.5"), "README 不应被改动");
});

test("校验不过的输入不改动任何文件", (t) => {
  const root = makeFixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));

  for (const version of ["not-a-version", FROM, "0.1.0-beta.5", "0.0.9", ""]) {
    assert.throws(() => bumpVersion({ root, version }), BumpVersionError, `应拒绝 ${version}`);
  }
  assert.equal(JSON.parse(read(root, "package.json")).version, FROM);
});

test("workspace 版本与根不一致时中止", (t) => {
  const root = makeFixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  writeFileSync(path.join(root, "desktop/package.json"), `${JSON.stringify({ version: "0.1.0-beta.1" }, null, 2)}\n`);

  assert.throws(() => bumpVersion({ root, version: TO }), BumpVersionError);
  assert.equal(JSON.parse(read(root, "package.json")).version, FROM);
});

test("CLI 支持 --root 并报告全部改动文件", (t) => {
  const root = makeFixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));

  const cli = path.join(path.dirname(fileURLToPath(import.meta.url)), "bump-version.mjs");
  const result = spawnSync(process.execPath, [cli, TO, "--root", root], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /0\.1\.0-beta\.6 -> 0\.1\.0-beta\.7/);
  assert.match(result.stdout, /package-lock\.json/);
  assert.equal(JSON.parse(read(root, "package.json")).version, TO);
});

test("版本比较用于拦截降级", () => {
  assert.equal(compareVersions("0.1.0-beta.7", "0.1.0-beta.6"), 1);
  assert.equal(compareVersions("0.1.0-beta.6", "0.1.0-beta.6"), 0);
  assert.equal(compareVersions("0.1.0-beta.10", "0.1.0-beta.9"), 1);
  assert.equal(compareVersions("0.1.0", "0.1.0-beta.10"), 1);
  assert.equal(compareVersions("0.2.0", "0.1.9"), 1);
  assert.equal(compareVersions("0.1.0-beta.9", "0.1.0"), -1);
});

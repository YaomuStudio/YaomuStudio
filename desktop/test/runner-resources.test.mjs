import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const { validateBundledRunnerResources } = require("../dist/runner-resources.js");
const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const generatorPath = path.join(repoRoot, "scripts", "generate-runner-manifest.mjs");

const SOURCE_FILES = {
  "yaomustudio_runner/__init__.py": '__version__ = "0.1.0"\n',
  "yaomustudio_runner/__main__.py": "from .main import main\nmain()\n",
  "yaomustudio_runner/main.py": "def main():\n    return 0\n",
  "yaomustudio_runner/sub/helper.py": "VALUE = 1\n",
};

const ENTRY_FILES = [
  "yaomustudio_runner/__init__.py",
  "yaomustudio_runner/__main__.py",
  "yaomustudio_runner/main.py",
];

async function writeTree(root) {
  for (const [relative, content] of Object.entries(SOURCE_FILES)) {
    const target = path.join(root, ...relative.split("/"));
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, content);
  }
}

async function sha256File(filePath) {
  return createHash("sha256").update(await readFile(filePath)).digest("hex");
}

async function manifestFor(coreDir) {
  const sourceDir = path.join(coreDir, "runner_src");
  const files = [];
  for (const relative of Object.keys(SOURCE_FILES).sort()) {
    const absolute = path.join(sourceDir, ...relative.split("/"));
    const bytes = await readFile(absolute);
    files.push({
      path: relative,
      size: bytes.length,
      sha256: createHash("sha256").update(bytes).digest("hex"),
    });
  }
  return {
    kind: "yaomu_runner_resources",
    schema_version: 1,
    runner_version: "0.1.0",
    requires_python: ">=3.11",
    files,
  };
}

async function validFixture() {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-resources-"));
  const coreDir = path.join(root, "core");
  await writeTree(path.join(coreDir, "runner_src"));
  const manifest = await manifestFor(coreDir);
  await writeFile(path.join(coreDir, "runner-manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return { root, coreDir, manifest };
}

async function writeManifest(coreDir, manifest) {
  await writeFile(path.join(coreDir, "runner-manifest.json"), `${JSON.stringify(manifest)}\n`);
}

async function cleanup(root) {
  await rm(root, { recursive: true, force: true });
}

test("valid staged resources produce a narrow descriptor", async () => {
  const fixture = await validFixture();
  try {
    const result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(result.descriptor.runnerVersion, "0.1.0");
    assert.equal(result.descriptor.sourceDir, path.join(fixture.coreDir, "runner_src"));
    const manifestBytes = await readFile(path.join(fixture.coreDir, "runner-manifest.json"));
    assert.equal(
      result.descriptor.manifestDigest,
      createHash("sha256").update(manifestBytes).digest("hex"),
    );
  } finally {
    await cleanup(fixture.root);
  }
});

test("Python bytecode caches are rejected, not ignored", async () => {
  const fixture = await validFixture();
  try {
    const sourceDir = path.join(fixture.coreDir, "runner_src");
    const packageDir = path.join(sourceDir, "yaomustudio_runner");
    await mkdir(path.join(packageDir, "__pycache__"), { recursive: true });
    await writeFile(path.join(packageDir, "__pycache__", "main.cpython-311.pyc"), "cache");
    let result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "bytecode-cache");
    assert.equal(result.detail, "yaomustudio_runner/__pycache__");

    await rm(path.join(packageDir, "__pycache__"), { recursive: true, force: true });
    await writeFile(path.join(packageDir, "stale.pyc"), "cache");
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "bytecode-cache");
    assert.equal(result.detail, "yaomustudio_runner/stale.pyc");

    // 大小写不敏感：Windows 会按同样名字找到缓存。
    await rm(path.join(packageDir, "stale.pyc"));
    await writeFile(path.join(packageDir, "STALE.PYO"), "cache");
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "bytecode-cache");
    assert.equal(result.detail, "yaomustudio_runner/STALE.PYO");
  } finally {
    await cleanup(fixture.root);
  }
});

test("a file disappearing during enumeration returns a fixed unavailability, not a rejection", async () => {
  const fixture = await validFixture();
  try {
    const moduleFS = require("node:fs/promises");
    const originalLstat = moduleFS.lstat;
    const target = path.join(
      fixture.coreDir,
      "runner_src",
      "yaomustudio_runner",
      "__init__.py",
    );
    moduleFS.lstat = async (filePath, ...args) => {
      if (String(filePath) === target) {
        throw Object.assign(new Error("controlled disappearance"), { code: "ENOENT" });
      }
      return originalLstat(filePath, ...args);
    };
    let result;
    try {
      result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    } finally {
      moduleFS.lstat = originalLstat;
    }
    assert.equal(result.ok, false);
    assert.equal(result.reason, "file-missing");
    assert.equal(result.detail, "yaomustudio_runner/__init__.py");
  } finally {
    await cleanup(fixture.root);
  }
});

test("missing manifest is a fixed failure, not legacy success", async () => {
  const fixture = await validFixture();
  try {
    await rm(path.join(fixture.coreDir, "runner-manifest.json"));
    const result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.deepEqual(result, { ok: false, reason: "manifest-missing", detail: "runner-manifest.json" });
  } finally {
    await cleanup(fixture.root);
  }
});

test("oversized manifest is rejected before parsing", async () => {
  const fixture = await validFixture();
  try {
    const manifestPath = path.join(fixture.coreDir, "runner-manifest.json");
    const valid = await readFile(manifestPath, "utf8");
    const padding = " ".repeat(2048);
    await writeFile(manifestPath, valid + padding);
    const result = await validateBundledRunnerResources({
      coreDir: fixture.coreDir,
      maxManifestBytes: 1024,
    });
    assert.equal(result.ok, false);
    assert.equal(result.reason, "manifest-too-large");
  } finally {
    await cleanup(fixture.root);
  }
});

test("invalid JSON and wrong shape are manifest-invalid", async () => {
  const fixture = await validFixture();
  try {
    const manifestPath = path.join(fixture.coreDir, "runner-manifest.json");
    await writeFile(manifestPath, "{not json");
    let result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "json");

    await writeManifest(fixture.coreDir, { ...fixture.manifest, kind: "other" });
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "kind");

    await writeManifest(fixture.coreDir, { ...fixture.manifest, extra: true });
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "unknown-field");
  } finally {
    await cleanup(fixture.root);
  }
});

test("path rules: duplicates, case-folding, traversal and ordering", async () => {
  const fixture = await validFixture();
  try {
    const base = fixture.manifest;

    const duplicated = structuredClone(base);
    duplicated.files.push({ ...duplicated.files[0] });
    await writeManifest(fixture.coreDir, duplicated);
    let result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "path-duplicate");

    const folded = structuredClone(base);
    folded.files.push({
      path: "yaomustudio_runner/MAIN.py",
      size: base.files[2].size,
      sha256: base.files[2].sha256,
    });
    folded.files.sort((a, b) => (a.path < b.path ? -1 : 1));
    await writeManifest(fixture.coreDir, folded);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "path-duplicate");

    const traversal = structuredClone(base);
    traversal.files[3].path = "yaomustudio_runner/../../escape.py";
    await writeManifest(fixture.coreDir, traversal);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "file-path");

    const absolute = structuredClone(base);
    absolute.files[3].path = "C:/evil.py";
    await writeManifest(fixture.coreDir, absolute);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "file-path");

    const unsorted = structuredClone(base);
    unsorted.files = [unsorted.files[1], unsorted.files[0], ...unsorted.files.slice(2)];
    await writeManifest(fixture.coreDir, unsorted);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "manifest-invalid");
    assert.equal(result.detail, "file-order");
  } finally {
    await cleanup(fixture.root);
  }
});

test("entry files must be declared", async () => {
  const fixture = await validFixture();
  try {
    const manifest = structuredClone(fixture.manifest);
    manifest.files = manifest.files.filter((file) => file.path !== ENTRY_FILES[1]);
    await writeManifest(fixture.coreDir, manifest);
    const result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "entry-missing");
    assert.equal(result.detail, ENTRY_FILES[1]);
  } finally {
    await cleanup(fixture.root);
  }
});

test("missing, extra and mismatched files fail with exact reasons", async () => {
  const fixture = await validFixture();
  try {
    const mainPath = path.join(
      fixture.coreDir,
      "runner_src",
      "yaomustudio_runner",
      "main.py",
    );
    await writeFile(mainPath, "def main():\n    return 1\n");
    let result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "file-mismatch");
    assert.equal(result.detail, "yaomustudio_runner/main.py");

    await writeFile(mainPath, SOURCE_FILES["yaomustudio_runner/main.py"]);
    const extraPath = path.join(fixture.coreDir, "runner_src", "yaomustudio_runner", "extra.py");
    await writeFile(extraPath, "x = 1\n");
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "file-extra");
    assert.equal(result.detail, "yaomustudio_runner/extra.py");

    await rm(extraPath);
    await rm(path.join(fixture.coreDir, "runner_src", "yaomustudio_runner", "sub", "helper.py"));
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "file-missing");
    assert.equal(result.detail, "yaomustudio_runner/sub/helper.py");

    // Size mismatch is reported before hashing.
    await writeTree(path.join(fixture.coreDir, "runner_src"));
    const sizeManifest = await manifestFor(fixture.coreDir);
    sizeManifest.files[2].size += 1;
    await writeManifest(fixture.coreDir, sizeManifest);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "file-mismatch");
  } finally {
    await cleanup(fixture.root);
  }
});

test("version format and requires_python are strict", async () => {
  const fixture = await validFixture();
  try {
    const badVersion = structuredClone(fixture.manifest);
    badVersion.runner_version = "v 1";
    await writeManifest(fixture.coreDir, badVersion);
    let result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.detail, "runner-version");

    const badPython = structuredClone(fixture.manifest);
    badPython.requires_python = ">=3.10";
    await writeManifest(fixture.coreDir, badPython);
    result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.detail, "requires-python");
  } finally {
    await cleanup(fixture.root);
  }
});

test("links and special files in the source tree are rejected", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows symlink 创建需要额外权限；生产消费仍拒绝链接");
    return;
  }
  const fixture = await validFixture();
  try {
    const linked = path.join(
      fixture.coreDir,
      "runner_src",
      "yaomustudio_runner",
      "linked.py",
    );
    await symlink(
      path.join(fixture.coreDir, "runner_src", "yaomustudio_runner", "main.py"),
      linked,
    );
    const result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "special-file");
    assert.equal(result.detail, "yaomustudio_runner/linked.py");
  } finally {
    await cleanup(fixture.root);
  }
});

test("manifest symlink is rejected", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows symlink 创建需要额外权限");
    return;
  }
  const fixture = await validFixture();
  try {
    const manifestPath = path.join(fixture.coreDir, "runner-manifest.json");
    const real = `${manifestPath}.real`;
    await writeFile(real, await readFile(manifestPath));
    await rm(manifestPath);
    await symlink(real, manifestPath);
    const result = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(result.reason, "special-file");
  } finally {
    await cleanup(fixture.root);
  }
});

test("special (non-regular) files are rejected", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows 无法在普通测试中创建 FIFO");
    return;
  }
  const fixture = await validFixture();
  try {
    const fifo = path.join(
      fixture.coreDir,
      "runner_src",
      "yaomustudio_runner",
      "pipe.py",
    );
    const created = spawnSync("mkfifo", [fifo], { encoding: "utf8" });
    if (created.status !== 0) {
      t.skip("mkfifo 不可用");
      return;
    }
    const validation = await validateBundledRunnerResources({ coreDir: fixture.coreDir });
    assert.equal(validation.reason, "special-file");
    assert.equal(validation.detail, "yaomustudio_runner/pipe.py");
  } finally {
    await cleanup(fixture.root);
  }
});

test("pre-aborted signal aborts instead of returning a resource verdict", async () => {
  const fixture = await validFixture();
  try {
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(
      () =>
        validateBundledRunnerResources({
          coreDir: fixture.coreDir,
          signal: controller.signal,
        }),
      (error) => error?.name === "AbortError",
    );
  } finally {
    await cleanup(fixture.root);
  }
});

test("generator output is accepted by the production consumer and is deterministic", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-generator-e2e-"));
  try {
    const runnerSrc = path.join(root, "core", "runner_src");
    const coreDir = path.join(root, "core");
    await writeTree(runnerSrc);
    await writeFile(
      path.join(root, "pyproject.toml"),
      '[project]\nversion = "0.1.0"\nrequires-python = ">=3.11"\n',
    );
    const outPath = path.join(coreDir, "runner-manifest.json");
    const run = () =>
      spawnSync(
        process.execPath,
        [generatorPath, "--runner-src", runnerSrc, "--pyproject", path.join(root, "pyproject.toml"), "--out", outPath],
        { encoding: "utf8" },
      );
    const first = run();
    assert.equal(first.status, 0, first.stderr);
    const firstBytes = await readFile(outPath);
    const second = run();
    assert.equal(second.status, 0, second.stderr);
    assert.deepEqual(await readFile(outPath), firstBytes);

    const result = await validateBundledRunnerResources({ coreDir });
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(result.descriptor.runnerVersion, "0.1.0");
    assert.equal(
      result.descriptor.manifestDigest,
      createHash("sha256").update(firstBytes).digest("hex"),
    );
  } finally {
    await cleanup(root);
  }
});

test("matching fixture digests use the same algorithm as sha256File", async () => {
  const fixture = await validFixture();
  try {
    assert.equal(
      await sha256File(path.join(fixture.coreDir, "runner_src", "yaomustudio_runner", "main.py")),
      fixture.manifest.files.find((file) => file.path === "yaomustudio_runner/main.py").sha256,
    );
  } finally {
    await cleanup(fixture.root);
  }
});

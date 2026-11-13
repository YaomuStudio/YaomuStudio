import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  generateRunnerManifest,
  readInitVersion,
  readTomlString,
} from "./generate-runner-manifest.mjs";

const scriptPath = fileURLToPath(new URL("./generate-runner-manifest.mjs", import.meta.url));

async function createFixture({ pyprojectVersion = "0.1.0", initVersion = "0.1.0", caches = false } = {}) {
  const root = await mkdtemp(path.join(os.tmpdir(), "yaomu-manifest-"));
  const runnerSrc = path.join(root, "src");
  const packageDir = path.join(runnerSrc, "yaomustudio_runner");
  await mkdir(path.join(packageDir, "sub"), { recursive: true });
  await writeFile(
    path.join(root, "pyproject.toml"),
    `[project]\nname = "yaomustudio-runner"\nversion = "${pyprojectVersion}"\nrequires-python = ">=3.11"\ndependencies = []\n`,
  );
  await writeFile(path.join(packageDir, "__init__.py"), `__version__ = "${initVersion}"\n`);
  await writeFile(path.join(packageDir, "__main__.py"), "from .main import main\nmain()\n");
  await writeFile(path.join(packageDir, "main.py"), "def main():\n    return 0\n");
  await writeFile(path.join(packageDir, "sub", "helper.py"), "VALUE = 1\n");
  if (caches) {
    await mkdir(path.join(packageDir, "__pycache__"), { recursive: true });
    await writeFile(path.join(packageDir, "__pycache__", "main.cpython-311.pyc"), "cached");
    await writeFile(path.join(packageDir, "stale.pyc"), "cached");
  }
  return { root, runnerSrc, pyprojectPath: path.join(root, "pyproject.toml"), outPath: path.join(root, "out", "runner-manifest.json") };
}

test("manifest generation is deterministic and lists only real source files", async () => {
  const fixture = await createFixture();
  try {
    const first = await generateRunnerManifest(fixture);
    const second = await generateRunnerManifest(fixture);
    assert.equal(first.text, second.text);
    assert.equal(first.digest, second.digest);

    const manifest = JSON.parse(await readFile(fixture.outPath, "utf8"));
    assert.equal(manifest.kind, "yaomu_runner_resources");
    assert.equal(manifest.schema_version, 1);
    assert.equal(manifest.runner_version, "0.1.0");
    assert.equal(manifest.requires_python, ">=3.11");
    const paths = manifest.files.map((file) => file.path);
    assert.deepEqual(paths, [...paths].sort());
    assert.ok(paths.includes("yaomustudio_runner/__init__.py"));
    assert.ok(paths.includes("yaomustudio_runner/__main__.py"));
    assert.ok(paths.includes("yaomustudio_runner/main.py"));
    assert.ok(paths.includes("yaomustudio_runner/sub/helper.py"));
    for (const file of manifest.files) {
      const bytes = await readFile(
        path.join(fixture.runnerSrc, ...file.path.split("/")),
      );
      assert.equal(file.size, bytes.length);
      assert.equal(file.sha256, createHash("sha256").update(bytes).digest("hex"));
      assert.match(file.sha256, /^[a-f0-9]{64}$/);
    }
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("bytecode caches are rejected, not listed or ignored", async () => {
  const fixture = await createFixture({ caches: true });
  try {
    await assert.rejects(() => generateRunnerManifest(fixture), /字节码缓存/);
    await assert.rejects(() => readFile(fixture.outPath), /ENOENT/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("bytecode cache detection is case-insensitive", async () => {
  const fixture = await createFixture();
  try {
    const packageDir = path.join(fixture.runnerSrc, "yaomustudio_runner");
    await mkdir(path.join(packageDir, "__PYCACHE__"), { recursive: true });
    await writeFile(path.join(packageDir, "__PYCACHE__", "x.PYC"), "cached");
    await assert.rejects(() => generateRunnerManifest(fixture), /字节码缓存/);
    await rm(path.join(packageDir, "__PYCACHE__"), { recursive: true, force: true });
    await writeFile(path.join(packageDir, "stale.PYO"), "cached");
    await assert.rejects(() => generateRunnerManifest(fixture), /字节码缓存/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("version disagreement between pyproject and package __init__ is rejected", async () => {
  const fixture = await createFixture({ pyprojectVersion: "0.2.0" });
  try {
    await assert.rejects(() => generateRunnerManifest(fixture), /版本不一致/);
    await assert.rejects(() => readFile(fixture.outPath), /ENOENT/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("missing entry file is rejected instead of producing a partial manifest", async () => {
  const fixture = await createFixture();
  try {
    await rm(path.join(fixture.runnerSrc, "yaomustudio_runner", "__main__.py"));
    await assert.rejects(() => generateRunnerManifest(fixture), /缺少入口文件/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("source outside yaomustudio_runner is rejected", async () => {
  const fixture = await createFixture();
  try {
    await writeFile(path.join(fixture.runnerSrc, "other.py"), "x = 1\n");
    await assert.rejects(() => generateRunnerManifest(fixture), /yaomustudio_runner/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("links in the source tree are rejected, not followed", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows symlink 创建需要额外权限；由消费者校验测试覆盖");
    return;
  }
  const fixture = await createFixture();
  try {
    await symlink(
      path.join(fixture.runnerSrc, "yaomustudio_runner", "main.py"),
      path.join(fixture.runnerSrc, "yaomustudio_runner", "linked.py"),
    );
    await assert.rejects(() => generateRunnerManifest(fixture), /链接/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("requires-python disagreement is rejected", async () => {
  const fixture = await createFixture();
  try {
    await writeFile(
      fixture.pyprojectPath,
      '[project]\nversion = "0.1.0"\nrequires-python = ">=3.10"\n',
    );
    await assert.rejects(() => generateRunnerManifest(fixture), /requires-python/);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("minimal TOML/__init__ readers do not import package code", () => {
  assert.equal(
    readTomlString('[project]\nversion = "1.2.3"\nrequires-python = ">=3.11"\n', "project", "version"),
    "1.2.3",
  );
  assert.equal(readTomlString('[tool.x]\nversion = "9"\n', "project", "version"), null);
  assert.equal(readInitVersion('"""doc"""\n__version__ = "0.1.0"\n'), "0.1.0");
  assert.equal(readInitVersion("__version__ = 1\n"), null);
  assert.equal(scriptPath.endsWith("generate-runner-manifest.mjs"), true);
});

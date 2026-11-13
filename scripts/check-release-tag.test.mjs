import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { parseTag, resolveRelease, ReleaseTagError } from "./check-release-tag.mjs";

function git(repo, args) {
  const result = spawnSync("git", ["-C", repo, ...args], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function makeRepo(version = "0.1.0") {
  const repo = mkdtempSync(path.join(os.tmpdir(), "yaomu-release-tag-"));
  git(repo, ["init", "-q"]);
  git(repo, ["config", "user.email", "test@example.com"]);
  git(repo, ["config", "user.name", "Test"]);
  for (const dir of ["", "ui", "desktop"]) {
    const target = dir ? path.join(repo, dir) : repo;
    mkdirSync(target, { recursive: true });
    writeFileSync(path.join(target, "package.json"), JSON.stringify({ version }, null, 2));
  }
  git(repo, ["add", "-A"]);
  git(repo, ["commit", "-q", "-m", "init"]);
  return repo;
}

function tag(repo, tagName) {
  git(repo, ["tag", tagName]);
  return git(repo, ["rev-parse", `${tagName}^{commit}`]);
}

test("parseTag accepts canonical stable and beta tags", () => {
  assert.equal(parseTag("v0.1.0"), "v0.1.0");
  assert.equal(parseTag("v1.2.3-beta.4"), "v1.2.3-beta.4");
});

test("parseTag rejects malformed tags", () => {
  for (const bad of ["0.1.0", "v01.0.0", "v1.2", "v1.2.3-rc.1", "v1.2.3-beta", " v1.2.3 "]) {
    assert.throws(() => parseTag(bad), (error) => error instanceof ReleaseTagError && error.code === "tag-invalid");
  }
});

test("resolveRelease resolves the tag to a full commit and version", () => {
  const repo = makeRepo("0.1.0");
  try {
    const commit = tag(repo, "v0.1.0");
    const result = resolveRelease({ repoRoot: repo, eventName: "push", refType: "tag", refName: "v0.1.0" });
    assert.deepEqual(result, { tag: "v0.1.0", version: "0.1.0", commit, prerelease: false });
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease marks beta tags as prerelease", () => {
  const repo = makeRepo("0.1.0-beta.1");
  try {
    tag(repo, "v0.1.0-beta.1");
    const result = resolveRelease({
      repoRoot: repo,
      eventName: "workflow_dispatch",
      inputTag: "v0.1.0-beta.1",
    });
    assert.equal(result.prerelease, true);
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease rejects version mismatches", () => {
  const repo = makeRepo("0.1.0");
  try {
    tag(repo, "v0.1.1");
    assert.throws(
      () => resolveRelease({ repoRoot: repo, eventName: "push", refType: "tag", refName: "v0.1.1" }),
      (error) => error instanceof ReleaseTagError && error.code === "version-mismatch",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease rejects missing tags and branch pushes", () => {
  const repo = makeRepo("0.1.0");
  try {
    assert.throws(
      () => resolveRelease({ repoRoot: repo, eventName: "push", refType: "tag", refName: "v9.9.9" }),
      (error) => error instanceof ReleaseTagError && error.code === "tag-not-found",
    );
    assert.throws(
      () => resolveRelease({ repoRoot: repo, eventName: "push", refType: "branch", refName: "main" }),
      (error) => error instanceof ReleaseTagError && error.code === "not-a-tag-push",
    );
    assert.throws(
      () => resolveRelease({ repoRoot: repo, eventName: "workflow_dispatch", inputTag: "" }),
      (error) => error instanceof ReleaseTagError && error.code === "tag-invalid",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

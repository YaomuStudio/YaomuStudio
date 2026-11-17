import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { parseTag, resolveRelease, ReleaseTagError } from "./check-release-tag.mjs";

const BOT_NAME = "YaomuStudio Release";
const BOT_EMAIL = "noreply@users.noreply.github.com";
const MAIN_REF = "refs/remotes/origin/main";

function git(repo, args) {
  const result = spawnSync("git", ["-C", repo, ...args], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function makeRepo(version = "0.1.0") {
  const repo = mkdtempSync(path.join(os.tmpdir(), "yaomu-release-tag-"));
  git(repo, ["init", "-q", "-b", "main"]);
  git(repo, ["config", "user.email", "test@example.com"]);
  git(repo, ["config", "user.name", "Test"]);
  for (const dir of ["", "ui", "desktop"]) {
    const target = dir ? path.join(repo, dir) : repo;
    mkdirSync(target, { recursive: true });
    writeFileSync(path.join(target, "package.json"), JSON.stringify({ version }, null, 2));
  }
  git(repo, ["add", "-A"]);
  git(repo, ["commit", "-q", "-m", "init"]);
  git(repo, ["update-ref", MAIN_REF, "HEAD"]);
  return repo;
}

function commit(repo, message) {
  writeFileSync(path.join(repo, "marker.txt"), message);
  git(repo, ["add", "-A"]);
  git(repo, ["commit", "-q", "-m", message]);
  return git(repo, ["rev-parse", "HEAD"]);
}

function tag(repo, tagName, { email = BOT_EMAIL, annotated = true } = {}) {
  if (annotated) {
    git(repo, [
      "-c",
      `user.name=${BOT_NAME}`,
      "-c",
      `user.email=${email}`,
      "tag",
      "-a",
      "-m",
      tagName,
      tagName,
    ]);
  } else {
    git(repo, ["tag", tagName]);
  }
  return git(repo, ["rev-parse", `${tagName}^{commit}`]);
}

function pushRelease(repo, tagName) {
  return resolveRelease({ repoRoot: repo, eventName: "push", refType: "tag", refName: tagName });
}

function dispatchRelease(repo, tagName) {
  return resolveRelease({ repoRoot: repo, eventName: "workflow_dispatch", inputTag: tagName });
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
    const sha = tag(repo, "v0.1.0");
    const result = pushRelease(repo, "v0.1.0");
    assert.deepEqual(result, { tag: "v0.1.0", version: "0.1.0", commit: sha, prerelease: false });
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease marks beta tags as prerelease", () => {
  const repo = makeRepo("0.1.0-beta.1");
  try {
    tag(repo, "v0.1.0-beta.1");
    const result = dispatchRelease(repo, "v0.1.0-beta.1");
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
      () => pushRelease(repo, "v0.1.1"),
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
      () => pushRelease(repo, "v9.9.9"),
      (error) => error instanceof ReleaseTagError && error.code === "tag-not-found",
    );
    assert.throws(
      () => resolveRelease({ repoRoot: repo, eventName: "push", refType: "branch", refName: "main" }),
      (error) => error instanceof ReleaseTagError && error.code === "not-a-tag-push",
    );
    assert.throws(
      () => dispatchRelease(repo, ""),
      (error) => error instanceof ReleaseTagError && error.code === "tag-invalid",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease rejects lightweight tags", () => {
  const repo = makeRepo("0.1.0");
  try {
    tag(repo, "v0.1.0", { annotated: false });
    assert.throws(
      () => pushRelease(repo, "v0.1.0"),
      (error) => error instanceof ReleaseTagError && error.code === "tag-not-annotated",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease rejects non-noreply taggers", () => {
  const repo = makeRepo("0.1.0");
  try {
    tag(repo, "v0.1.0", { email: "real@example.com" });
    assert.throws(
      () => pushRelease(repo, "v0.1.0"),
      (error) => error instanceof ReleaseTagError && error.code === "tagger-not-noreply",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("push requires the tag to equal the public main head", () => {
  const repo = makeRepo("0.1.0");
  try {
    tag(repo, "v0.1.0");
    const next = commit(repo, "next");
    git(repo, ["update-ref", MAIN_REF, next]);
    assert.throws(
      () => pushRelease(repo, "v0.1.0"),
      (error) => error instanceof ReleaseTagError && error.code === "tag-not-main-head",
    );
    assert.equal(dispatchRelease(repo, "v0.1.0").tag, "v0.1.0");
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("workflow_dispatch requires the tag to be reachable from public main", () => {
  const repo = makeRepo("0.1.0");
  try {
    git(repo, ["checkout", "-q", "-b", "side"]);
    commit(repo, "side");
    tag(repo, "v0.1.0");
    git(repo, ["checkout", "-q", "main"]);
    assert.throws(
      () => dispatchRelease(repo, "v0.1.0"),
      (error) => error instanceof ReleaseTagError && error.code === "tag-not-on-main",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test("resolveRelease fails closed when the public main ref is missing", () => {
  const repo = makeRepo("0.1.0");
  try {
    tag(repo, "v0.1.0");
    git(repo, ["update-ref", "-d", MAIN_REF]);
    assert.throws(
      () => pushRelease(repo, "v0.1.0"),
      (error) => error instanceof ReleaseTagError && error.code === "main-ref-missing",
    );
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

#!/usr/bin/env node
/**
 * 校验发布 tag：只接受既有 canonical tag，解析 full commit，并要求根/ui/desktop
 * 三处 package.json 版本与 tag 一致。同时要求公开 tag 为 annotated、tagger 为
 * noreply，且 tag 提交与公开 main 的关系合法（push 必须等于 main head，
 * workflow_dispatch 必须可从 main 到达）。输出 GITHUB_OUTPUT 供 build/publish 使用。
 *
 * 环境：RELEASE_EVENT_NAME、RELEASE_REF_TYPE、RELEASE_REF_NAME、RELEASE_INPUT_TAG、
 *       RELEASE_REPO_ROOT（可选）、GITHUB_OUTPUT（可选）。
 */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const TAG_RE = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-beta\.(0|[1-9]\d*))?$/;

export class ReleaseTagError extends Error {
  constructor(code, detail = "") {
    super(code);
    this.code = code;
    this.detail = detail;
  }
}

export function parseTag(raw) {
  const tag = String(raw ?? "");
  if (!TAG_RE.test(tag)) throw new ReleaseTagError("tag-invalid", tag);
  return tag;
}

function gitResult(repoRoot, args) {
  return spawnSync("git", ["-C", repoRoot, ...args], { encoding: "utf8" });
}

function git(repoRoot, args) {
  const result = gitResult(repoRoot, args);
  if (result.status !== 0) {
    throw new ReleaseTagError("git-failed", (result.stderr || result.stdout || "").trim());
  }
  return result.stdout.trim();
}

export function packageVersionAt(repoRoot, commit, file) {
  let raw;
  try {
    raw = git(repoRoot, ["show", `${commit}:${file}`]);
  } catch {
    throw new ReleaseTagError("package-missing", file);
  }
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new ReleaseTagError("package-invalid", file);
  }
  if (typeof parsed.version !== "string" || parsed.version.length === 0) {
    throw new ReleaseTagError("package-version-missing", file);
  }
  return parsed.version;
}

export function resolveRelease({ repoRoot, eventName, refType, refName, inputTag, mainRef }) {
  let tag;
  if (eventName === "push") {
    if (refType !== "tag") throw new ReleaseTagError("not-a-tag-push", String(refType ?? ""));
    tag = parseTag(refName);
  } else if (eventName === "workflow_dispatch") {
    tag = parseTag(inputTag);
  } else {
    throw new ReleaseTagError("event-invalid", String(eventName ?? ""));
  }

  let commit;
  try {
    commit = git(repoRoot, ["rev-parse", "--verify", `refs/tags/${tag}^{commit}`]);
  } catch {
    throw new ReleaseTagError("tag-not-found", tag);
  }
  if (!/^[0-9a-f]{40}$/.test(commit)) throw new ReleaseTagError("commit-invalid", commit);

  let tagType;
  try {
    tagType = git(repoRoot, ["cat-file", "-t", `refs/tags/${tag}`]);
  } catch {
    throw new ReleaseTagError("tag-not-found", tag);
  }
  if (tagType !== "tag") throw new ReleaseTagError("tag-not-annotated", tagType);

  const tagger = git(repoRoot, ["for-each-ref", "--format=%(taggeremail)", `refs/tags/${tag}`]).replace(
    /^<(.*)>$/,
    "$1",
  );
  if (!tagger.endsWith("@users.noreply.github.com")) {
    throw new ReleaseTagError("tagger-not-noreply", tagger || "(empty)");
  }

  const version = tag.slice(1);
  for (const file of ["package.json", "ui/package.json", "desktop/package.json"]) {
    const actual = packageVersionAt(repoRoot, commit, file);
    if (actual !== version) {
      throw new ReleaseTagError("version-mismatch", `${file}: ${actual} != ${version}`);
    }
  }

  const mainRefName = mainRef ?? "refs/remotes/origin/main";
  let mainCommit;
  try {
    mainCommit = git(repoRoot, ["rev-parse", "--verify", `${mainRefName}^{commit}`]);
  } catch {
    throw new ReleaseTagError("main-ref-missing", mainRefName);
  }
  if (eventName === "push") {
    if (commit !== mainCommit) {
      throw new ReleaseTagError("tag-not-main-head", `${commit} != ${mainCommit}`);
    }
  } else {
    const ancestry = gitResult(repoRoot, ["merge-base", "--is-ancestor", commit, mainCommit]);
    if (ancestry.status === 1) throw new ReleaseTagError("tag-not-on-main", commit);
    if (ancestry.status !== 0) {
      throw new ReleaseTagError("git-failed", (ancestry.stderr || ancestry.stdout || "").trim());
    }
  }

  return { tag, version, commit, prerelease: tag.includes("-beta.") };
}

function writeOutputs(result) {
  const output = process.env.GITHUB_OUTPUT;
  if (!output) return;
  fs.appendFileSync(
    output,
    `tag=${result.tag}\nversion=${result.version}\ncommit=${result.commit}\nprerelease=${result.prerelease}\n`,
  );
}

function main() {
  const repoRoot = path.resolve(process.env.RELEASE_REPO_ROOT ?? process.cwd());
  try {
    const result = resolveRelease({
      repoRoot,
      eventName: process.env.RELEASE_EVENT_NAME,
      refType: process.env.RELEASE_REF_TYPE,
      refName: process.env.RELEASE_REF_NAME,
      inputTag: process.env.RELEASE_INPUT_TAG,
    });
    writeOutputs(result);
    console.log(JSON.stringify(result));
  } catch (error) {
    if (error instanceof ReleaseTagError) {
      console.error(`[check-release-tag] FAIL: ${error.code}${error.detail ? ` (${error.detail})` : ""}`);
      process.exit(1);
    }
    console.error(`[check-release-tag] FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(1);
  }
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : "";
if (invokedPath === fileURLToPath(import.meta.url)) main();

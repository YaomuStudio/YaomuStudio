import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const workflowPath = path.join(repoRoot, ".github", "workflows", "release.yml");
const workflow = yaml.load(fs.readFileSync(workflowPath, "utf8"));

function jobSteps(job) {
  return (workflow.jobs?.[job]?.steps ?? []).map((step) => String(step.run ?? ""));
}

test("release only builds on v* tags and manual tag reruns", () => {
  assert.deepEqual(workflow.on?.push?.tags, ["v*"]);
  assert.equal(workflow.on?.push?.branches, undefined);
  assert.equal(workflow.on?.workflow_dispatch?.inputs?.tag?.required, true);
});

test("release matrix covers Windows, macOS and Linux", () => {
  assert.equal(workflow.jobs?.["build-windows"]?.["runs-on"], "windows-latest");
  assert.equal(workflow.jobs?.["build-macos"]?.["runs-on"], "macos-15");
  assert.equal(workflow.jobs?.["build-linux"]?.["runs-on"], "ubuntu-24.04");
});

test("all installers are built without publishing from the build jobs", () => {
  const runs = [
    ...jobSteps("build-windows"),
    ...jobSteps("build-macos"),
    ...jobSteps("build-linux"),
  ];
  const buildCommands = runs.filter((run) => run.includes("electron-builder"));
  assert.equal(buildCommands.length, 3);
  for (const command of buildCommands) assert.match(command, /--publish never/);
});

test("macOS builds both architectures in one invocation", () => {
  const runs = jobSteps("build-macos").join("\n");
  assert.match(runs, /--mac dmg zip --arm64 --x64/);
  assert.match(runs, /build-core\.sh --arch arm64/);
  assert.match(runs, /build-core\.sh --arch x64/);
});

test("publish waits for every build and has write permission", () => {
  const publish = workflow.jobs?.publish;
  assert.deepEqual(publish?.needs, ["validate", "build-windows", "build-macos", "build-linux"]);
  assert.equal(publish?.permissions?.contents, "write");
  const runs = jobSteps("publish").join("\n");
  assert.match(runs, /verify-release-assets\.mjs/);
  assert.match(runs, /gh release create/);
  assert.match(runs, /--verify-tag/);
  assert.doesNotMatch(runs, /--clobber/);
  assert.doesNotMatch(runs, /--draft/);
  assert.doesNotMatch(runs, /RELEASE_APPROVED/);
});

test("validate job checks tag against package versions", () => {
  const runs = jobSteps("validate").join("\n");
  assert.match(runs, /check-release-tag\.mjs/);
});

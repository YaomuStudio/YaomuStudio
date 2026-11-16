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

test("release matrix covers Windows and Linux; macOS temporarily excluded", () => {
  assert.equal(workflow.jobs?.["build-windows"]?.["runs-on"], "windows-latest");
  assert.equal(workflow.jobs?.["build-linux"]?.["runs-on"], "ubuntu-24.04");
  assert.equal(workflow.jobs?.["build-macos"], undefined);
});

test("all installers are built without publishing from the build jobs", () => {
  const runs = [
    ...jobSteps("build-windows"),
    ...jobSteps("build-linux"),
  ];
  const buildCommands = runs.filter((run) => run.includes("electron-builder"));
  assert.equal(buildCommands.length, 2);
  for (const command of buildCommands) assert.match(command, /--publish never/);
});

test("macOS release build is temporarily excluded", () => {
  assert.equal(workflow.jobs?.["build-macos"], undefined);
  assert.doesNotMatch(jobSteps("publish").join("\n"), /--platform all/);
});

test("publish waits for every build and has write permission", () => {
  const publish = workflow.jobs?.publish;
  assert.deepEqual(publish?.needs, ["validate", "build-windows", "build-linux"]);
  assert.equal(publish?.permissions?.contents, "write");
  const runs = jobSteps("publish").join("\n");
  assert.match(runs, /verify-release-assets\.mjs/);
  assert.match(runs, /--platform win,linux/);
  assert.equal(runs.match(/verify-release-assets\.mjs/g)?.length, 1);
  assert.match(runs, /gh release create/);
  assert.match(runs, /--verify-tag/);
  assert.doesNotMatch(runs, /--clobber/);
  assert.doesNotMatch(runs, /--draft/);
  assert.doesNotMatch(runs, /RELEASE_APPROVED/);
});

test("publish uploads immutable OBS artifacts first and flips update manifests last", () => {
  const steps = workflow.jobs?.publish?.steps ?? [];
  const runs = steps.map((step) => String(step.run ?? ""));
  const artifactUpload = runs.findIndex(
    (run) => run.includes("publish-obs.mjs") && run.includes("--stage artifacts"),
  );
  const releaseCreate = runs.findIndex((run) => run.includes("gh release create"));
  const releaseUpload = runs.findIndex((run) => run.includes("gh release upload"));
  const manifestUpload = runs.findIndex(
    (run) => run.includes("publish-obs.mjs") && run.includes("--stage manifests"),
  );
  assert.ok(artifactUpload >= 0, "artifacts upload step missing");
  assert.ok(manifestUpload >= 0, "manifests upload step missing");
  assert.ok(artifactUpload < releaseCreate, "artifacts must be uploaded before GitHub release");
  assert.ok(manifestUpload > releaseUpload, "manifests must be published after GitHub archive");
  for (const index of [artifactUpload, manifestUpload]) {
    const env = steps[index].env ?? {};
    assert.equal(env.OBS_ENDPOINT, "${{ vars.OBS_ENDPOINT }}");
    assert.equal(env.OBS_BUCKET, "${{ vars.OBS_BUCKET }}");
    assert.equal(env.OBS_FEED_BASE_URL, "${{ vars.OBS_FEED_BASE_URL }}");
    assert.equal(env.OBS_ACCESS_KEY_ID, "${{ secrets.OBS_ACCESS_KEY_ID }}");
    assert.equal(env.OBS_SECRET_ACCESS_KEY, "${{ secrets.OBS_SECRET_ACCESS_KEY }}");
  }
});

test("validate job checks tag against package versions", () => {
  const runs = jobSteps("validate").join("\n");
  assert.match(runs, /check-release-tag\.mjs/);
});

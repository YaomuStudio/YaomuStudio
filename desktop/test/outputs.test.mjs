import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";

const require = createRequire(import.meta.url);
const { resolveProgramDir, resolveOutputsDir } = require("../dist/outputs.js");

test("development outputs resolve to the repository root", () => {
  const repo = path.resolve("tmp", "yaomustudio");
  const input = {
    packaged: false,
    appPath: path.join(repo, "desktop"),
    execPath: path.join(repo, "node_modules", "electron", "dist", "electron"),
  };
  assert.equal(resolveProgramDir(input), repo);
  assert.equal(resolveOutputsDir(input), path.join(repo, "outputs"));
});

test("packaged outputs sit next to the application executable", () => {
  const install = path.resolve("tmp", "YaomuStudio");
  const input = {
    packaged: true,
    appPath: path.join(install, "resources", "app.asar"),
    execPath: path.join(install, "YaomuStudio.exe"),
  };
  assert.equal(resolveProgramDir(input), install);
  assert.equal(resolveOutputsDir(input), path.join(install, "outputs"));
});

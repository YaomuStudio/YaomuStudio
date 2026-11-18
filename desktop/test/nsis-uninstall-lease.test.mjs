import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");

test("nsis uninstall releases only the runner lease", () => {
  const builder = readFileSync(path.join(root, "electron-builder.yml"), "utf8");
  const nsis = builder.slice(builder.indexOf("nsis:"), builder.indexOf("mac:"));
  assert.match(nsis, /^ {2}include:\s*nsis\/uninstall-lease\.nsh\s*$/m);

  const script = readFileSync(path.join(root, "nsis", "uninstall-lease.nsh"), "utf8");
  assert.match(script, /!macro customUnInstall/);
  assert.match(
    script,
    /RMDir \/r "\$APPDATA\\@yaomustudio\\desktop\\core-data\\runtime\\runner-lease"/,
  );
  assert.doesNotMatch(script, /RMDir\s+\/r\s+"\$APPDATA\\@yaomustudio\\desktop"/);
  assert.doesNotMatch(script, /wsl\.exe|--unregister/i);
});

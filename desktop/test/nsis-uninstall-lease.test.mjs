import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");

test("nsis uninstall keeps data by default and deletes only after explicit confirmation", () => {
  const builder = readFileSync(path.join(root, "electron-builder.yml"), "utf8");
  const nsis = builder.slice(builder.indexOf("nsis:"), builder.indexOf("mac:"));
  assert.match(nsis, /^ {2}include:\s*nsis\/uninstall-lease\.nsh\s*$/m);

  const script = readFileSync(path.join(root, "nsis", "uninstall-lease.nsh"), "utf8");
  assert.match(script, /!macro customUnInit/);
  assert.match(script, /IfSilent yaomu_uninstall_init_done/);
  assert.match(script, /MB_DEFBUTTON2/);
  assert.match(script, /StrCpy \$YaomuDeleteData "0"/);
  assert.match(script, /StrCpy \$YaomuDeleteData "1"/);
  assert.match(script, /RMDir \/r "\$APPDATA\\@yaomustudio\\desktop\\core-data\\runtime\\runner-lease"/);

  // 项目数据删除只能发生在显式确认分支；WSL 运行环境始终保留。
  const keepStart = script.indexOf("yaomu_uninstall_keep_data:");
  const deleteStart = script.indexOf("yaomu_uninstall_delete_data:");
  const done = script.indexOf("yaomu_uninstall_lease_done:");
  assert.ok(keepStart > 0 && deleteStart > keepStart && done > deleteStart);
  assert.match(
    script.slice(deleteStart, done),
    /RMDir \/r "\$APPDATA\\@yaomustudio\\desktop\\core-data"/,
  );
  assert.doesNotMatch(script.slice(deleteStart, done), /wsl\.exe --unregister/i);
  assert.doesNotMatch(script.slice(deleteStart, done), /RMDir \/r "\$APPDATA\\@yaomustudio\\desktop"/);
  assert.match(script.slice(deleteStart, done), /RMDir \/r "\$INSTDIR\\outputs"/);
  assert.match(
    script.slice(keepStart, deleteStart),
    /CopyFiles \/SILENT "\$INSTDIR\\outputs\\\*\.\*" "\$DOCUMENTS\\YaomuStudio Exports"/,
  );
});

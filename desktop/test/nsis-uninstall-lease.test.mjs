import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");

function read(...parts) {
  return readFileSync(path.join(root, ...parts), "utf8");
}

const installer = read("nsis", "installer.nsh");
const uninstaller = read("nsis", "uninstall-lease.nsh");
const builder = read("electron-builder.yml");

function between(source, startText, endText) {
  const start = source.indexOf(startText);
  assert.ok(start >= 0, `missing ${startText}`);
  const end = source.indexOf(endText, start);
  assert.ok(end > start, `missing ${endText} after ${startText}`);
  return source.slice(start, end);
}

test("nsis include chain keeps the helper entry and disables template app-data deletion", () => {
  const nsis = builder.slice(builder.indexOf("nsis:"), builder.indexOf("mac:"));
  assert.match(nsis, /^ {2}include:\s*nsis\/uninstall-lease\.nsh\s*$/m);
  assert.match(nsis, /^ {2}deleteAppDataOnUninstall:\s*false\s*$/m);
  assert.match(uninstaller, /!include "\$\{__FILEDIR__\}\\installer\.nsh"/);
  assert.doesNotMatch(installer, /!macro customUnInstall/);
  assert.match(uninstaller, /!macro customUnInstall/);
});

test("uninstall never terminates or unregisters WSL", () => {
  for (const source of [installer, uninstaller]) {
    assert.doesNotMatch(source, /wsl\.exe/i);
    assert.doesNotMatch(source, /--terminate|--unregister|--shutdown/i);
  }
});

test("uninstall never removes the whole userData root or bare APPDATA", () => {
  for (const source of [installer, uninstaller]) {
    assert.doesNotMatch(source, /RMDir\s+\/r\s+"\$APPDATA"(?!\\)/);
    assert.doesNotMatch(
      source,
      /RMDir\s+\/r\s+"\$APPDATA\\@yaomustudio\\desktop"\s*$/m,
    );
  }
});

test("uninstall retains the runner lease without a confirmed resource exit", () => {
  assert.doesNotMatch(uninstaller, /RMDir\s+\/r\s+"\$APPDATA\\@yaomustudio\\desktop\\core-data\\runtime\\runner-lease"/);
});

test("customUnInit rejects the stock --delete-app-data bypass", () => {
  const macro = between(installer, "!macro customUnInit", "!macroend");
  assert.match(macro, /--delete-app-data/);
  assert.match(macro, /\$\{If\} \$\{Silent\}[\s\S]*?SetErrorLevel 2[\s\S]*?Quit/);
  assert.match(macro, /Quit/);
});

test("page has two independent default-off choices and model state", () => {
  const page = between(
    installer,
    "Function un.yaomuRunInventory",
    "Function un.yaomuDataPageLeave",
  );
  assert.match(page, /删除个人数据和设置/);
  assert.match(page, /Delete personal data and settings/);
  assert.match(page, /同时删除已下载模型/);
  assert.match(page, /Also delete downloaded models/);
  assert.match(page, /\$\{NSD_CreateCheckbox\}/);
  assert.match(page, /EnableWindow \$yaomuModelsCheckbox 0/);
  assert.match(page, /\$yaomuPersonalRequested == "1"/);
  assert.match(page, /\$yaomuModelsRequested == "1"/);
  assert.match(page, /models_size_display/);
  assert.match(page, /暂时无法确认哪些模型可以安全删除/);
  assert.match(page, /\$\{NSD_OnClick\} \$yaomuPersonalCheckbox un\.yaomuPersonalClick/);
  assert.match(page, /\$\{NSD_OnClick\} \$yaomuModelsCheckbox un\.yaomuModelsClick/);
  assert.match(installer, /Function un\.yaomuPersonalClick/);
  assert.match(installer, /Function un\.yaomuModelsClick/);
});

test("page calls the read-only helper and skips automatic updates", () => {
  const page = between(
    installer,
    "Function un.yaomuRunInventory",
    "Function un.yaomuDataPageLeave",
  );
  assert.match(page, /\$\{If\} \$\{isUpdated\}[\s\S]*?Abort/);
  assert.match(page, /yaomu-uninstall-helper\.exe" inventory/);
  assert.match(page, /--user-data/);
  assert.match(page, /--install-dir/);
  assert.match(page, /--documents/);
});

test("uninstall confirms once with Enter/No default and calls the helper", () => {
  const macro = between(uninstaller, "!macro customUnInstall", "!macroend");
  assert.match(macro, /MessageBox MB_YESNO\|MB_ICONEXCLAMATION\|MB_DEFBUTTON2/);
  assert.match(macro, /\/SD IDNO/);
  assert.match(macro, /yaomu-uninstall-helper\.exe" apply/);
  assert.match(macro, /--personal/);
  assert.match(macro, /--models/);
  assert.match(
    macro,
    /\$\{If\} \$yaomuPersonalRequested == "1"\s*\n\s*\$\{OrIf\} \$yaomuModelsRequested == "1"\s*\n\s*StrCpy \$R0 "\$R0 --snapshot \$yaomuModelsSnapshot"/,
  );
  assert.match(macro, /SetDetailsPrint none/);
  assert.match(macro, /SetDetailsPrint lastused/);
  assert.match(macro, /\n  SetDetailsPrint none/);
  assert.doesNotMatch(macro, /\n    SetDetailsPrint none/);
});

test("helper failure stops before the template removes the program directory", () => {
  const macro = between(uninstaller, "!macro customUnInstall", "!macroend");
  assert.match(macro, /\$\{If\} \$0 != 0[\s\S]*?Quit/);
  assert.match(macro, /uninstall-error-backup-failed/);
  assert.match(macro, /uninstall-error-snapshot-stale/);
  assert.match(macro, /uninstall-error-app-running/);
  assert.match(macro, /uninstall-error-models-unknown/);
});

test("helper failure messages stay localized and do not expose paths", () => {
  const macro = between(uninstaller, "!macro customUnInstall", "!macroend");
  assert.match(macro, /导出文件备份失败/);
  assert.match(macro, /Backing up exported files failed/);
});

test("installer resets onboarding only for non-update fresh installs", () => {
  const macro = between(installer, "!macro customInstall", "!macroend");
  assert.match(macro, /\$\{IfNot\}\s+\$\{isUpdated\}/);
  assert.match(macro, /\$\{If\}\s+\$yaomuHadExistingInstall != "1"/);
  assert.match(macro, /onboarding\.json/);
  assert.match(macro, /\{"schema_version":1,"completed":false\}/);
});

test("installer detects an existing installation before upgrading", () => {
  assert.match(installer, /!ifndef BUILD_UNINSTALLER\s+Var yaomuHadExistingInstall\s+!endif/);
  const macro = between(installer, "!macro customInit", "!macroend");
  assert.match(macro, /StrCpy \$yaomuHadExistingInstall "0"/);
  assert.match(macro, /IfFileExists "\$INSTDIR\\\$\{APP_EXECUTABLE_FILENAME\}"/);
  assert.match(macro, /StrCpy \$yaomuHadExistingInstall "1"/);
});

test("uninstaller normalizes per-machine context", () => {
  const macro = between(uninstaller, "!macro customUnInstall", "!macroend");
  const current = macro.indexOf('SetShellVarContext current');
  const restore = macro.lastIndexOf('SetShellVarContext all');
  assert.ok(current >= 0 && restore > current);
});

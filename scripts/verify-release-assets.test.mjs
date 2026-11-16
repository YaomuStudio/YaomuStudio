import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import fs, { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { verifyReleaseAssets, ReleaseAssetError } from "./verify-release-assets.mjs";

function sha512(content) {
  return createHash("sha512").update(content).digest("base64");
}

function writeAsset(dir, name, content) {
  writeFileSync(path.join(dir, name), content);
  return { url: name, size: Buffer.byteLength(content), sha512: sha512(content) };
}

function writeManifest(dir, name, files, extra = {}) {
  const lines = ["version: 0.1.0", "files:"];
  for (const file of files) {
    lines.push(`  - url: ${file.url}`, `    sha512: ${file.sha512}`, `    size: ${file.size}`);
  }
  if (files[0]) {
    lines.push(`path: ${extra.path ?? files[0].url}`);
    lines.push(`sha512: ${extra.sha512 ?? files[0].sha512}`);
  }
  writeFileSync(path.join(dir, name), `${lines.join("\n")}\n`);
}

function tempDir(prefix) {
  return mkdtempSync(path.join(os.tmpdir(), prefix));
}

function windowsSet() {
  const dir = tempDir("yaomu-verify-win-");
  const exe = writeAsset(dir, "YaoMuStudio-0.1.0-win-x64.exe", "installer");
  writeAsset(dir, "YaoMuStudio-0.1.0-win-x64.exe.blockmap", "blockmap");
  writeManifest(dir, "latest.yml", [exe]);
  return dir;
}

function addLinuxSet(dir) {
  const appimage = writeAsset(dir, "YaoMuStudio-0.1.0-linux-x64.AppImage", "linux-appimage");
  writeAsset(dir, "YaoMuStudio-0.1.0-linux-x64.AppImage.blockmap", "linux-blockmap");
  writeManifest(dir, "latest-linux.yml", [appimage]);
}

test("accepts a valid Windows asset set", async () => {
  const dir = windowsSet();
  try {
    const result = await verifyReleaseAssets({ dir, version: "0.1.0", platform: "win" });
    assert.equal(result.ok, true);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects a manifest hash mismatch", async () => {
  const dir = windowsSet();
  try {
    const manifest = path.join(dir, "latest.yml");
    const text = fs.readFileSync(manifest, "utf8").replace(/sha512: .+/, "sha512: AAAA");
    writeFileSync(manifest, text);
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "win" }),
      (error) => error instanceof ReleaseAssetError && error.code === "manifest-sha512-mismatch",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects unexpected files", async () => {
  const dir = windowsSet();
  try {
    writeFileSync(path.join(dir, "extra.exe"), "extra");
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "win" }),
      (error) => error instanceof ReleaseAssetError && error.code === "asset-unexpected",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("requires both macOS architectures in latest-mac.yml", async () => {
  const dir = tempDir("yaomu-verify-mac-");
  try {
    const base = "YaoMuStudio-0.1.0-mac-arm64";
    const zip = writeAsset(dir, `${base}.zip`, "arm-zip");
    writeAsset(dir, `${base}.zip.blockmap`, "arm-zip-blockmap");
    const dmg = writeAsset(dir, `${base}.dmg`, "arm-dmg");
    writeAsset(dir, `${base}.dmg.blockmap`, "arm-dmg-blockmap");
    writeManifest(dir, "latest-mac.yml", [zip, dmg]);
    // 补齐 x64 资产，但清单只写 arm64。
    for (const suffix of [".zip", ".zip.blockmap", ".dmg", ".dmg.blockmap"]) {
      writeFileSync(path.join(dir, `YaoMuStudio-0.1.0-mac-x64${suffix}`), "x64");
    }
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "mac" }),
      (error) => error instanceof ReleaseAssetError && error.code === "manifest-mac-x64-missing",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects manifest URLs that escape the asset directory", async () => {
  const dir = windowsSet();
  try {
    const manifest = path.join(dir, "latest.yml");
    const text = fs.readFileSync(manifest, "utf8").replace(
      "url: YaoMuStudio-0.1.0-win-x64.exe",
      "url: ../YaoMuStudio-0.1.0-win-x64.exe",
    );
    writeFileSync(manifest, text);
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "win" }),
      (error) => error instanceof ReleaseAssetError && error.code === "manifest-url-invalid",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("accepts merged Windows and Linux assets when both platforms are declared", async () => {
  const dir = windowsSet();
  try {
    addLinuxSet(dir);
    const result = await verifyReleaseAssets({ dir, version: "0.1.0", platform: "win,linux" });
    assert.equal(result.ok, true);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects assets from a platform not included in the merged verification set", async () => {
  const dir = windowsSet();
  try {
    addLinuxSet(dir);
    writeFileSync(path.join(dir, "YaoMuStudio-0.1.0-mac-x64.dmg"), "mac");
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "win,linux" }),
      (error) => error instanceof ReleaseAssetError && error.code === "asset-unexpected",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects invalid platform lists", async () => {
  const dir = windowsSet();
  try {
    await assert.rejects(
      verifyReleaseAssets({ dir, version: "0.1.0", platform: "win,android" }),
      (error) => error instanceof ReleaseAssetError && error.code === "platform-invalid",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("accepts a complete all-platform asset set", async () => {
  const dir = tempDir("yaomu-verify-all-");
  try {
    const win = writeAsset(dir, "YaoMuStudio-0.1.0-win-x64.exe", "win-installer");
    writeAsset(dir, "YaoMuStudio-0.1.0-win-x64.exe.blockmap", "win-blockmap");
    writeManifest(dir, "latest.yml", [win]);

    const macFiles = [];
    for (const arch of ["arm64", "x64"]) {
      for (const ext of ["zip", "dmg"]) {
        const name = `YaoMuStudio-0.1.0-mac-${arch}.${ext}`;
        macFiles.push(writeAsset(dir, name, `${arch}-${ext}`));
        writeAsset(dir, `${name}.blockmap`, `${arch}-${ext}-blockmap`);
      }
    }
    const macManifest = [
      macFiles.find((file) => file.url.endsWith("mac-arm64.zip")),
      macFiles.find((file) => file.url.endsWith("mac-x64.zip")),
      macFiles.find((file) => file.url.endsWith("mac-arm64.dmg")),
      macFiles.find((file) => file.url.endsWith("mac-x64.dmg")),
    ];
    writeManifest(dir, "latest-mac.yml", macManifest);

    const linux = writeAsset(dir, "YaoMuStudio-0.1.0-linux-x64.AppImage", "linux-appimage");
    writeAsset(dir, "YaoMuStudio-0.1.0-linux-x64.AppImage.blockmap", "linux-blockmap");
    writeManifest(dir, "latest-linux.yml", [linux]);

    const result = await verifyReleaseAssets({ dir, version: "0.1.0", platform: "all" });
    assert.equal(result.ok, true);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

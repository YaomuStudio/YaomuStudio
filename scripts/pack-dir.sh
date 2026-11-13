#!/usr/bin/env bash
# 一键产出可分发目录（未签名）：构建 ui + desktop + Core/Runner，再执行 electron-builder --dir。
# 用法：npm run pack:dir（Node 入口会解析 Git Bash，Windows 下不会命中 WSL bash）
#       或在 Git Bash 内直接执行 bash scripts/pack-dir.sh
#
# 说明：
# - macOS 签名 + 公证、Windows 代码签名是公开发布前置（开发/内测可用未签名构建），electron-builder.yml 中以注释留位；
#   本脚本只产出可在本机隐藏冒烟的未签名目录。
# - Windows 可在 Git Bash 下执行；build-core.sh 会生成 yaomu-core.exe 与 yaomu-runner.cmd，
#   主进程按平台注入 Runner。FFmpeg/ffprobe 可通过 FFMPEG_BIN/FFPROBE_BIN 显式暂存。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "[pack:dir] typecheck + build (ui, desktop)"
npm run typecheck
npm run build

echo "[pack:dir] build core + runner resources"
case "$(uname -m)" in
  arm64 | aarch64) core_arch="arm64" ;;
  x86_64 | amd64) core_arch="x64" ;;
  *)
    echo "[pack:dir] unsupported host arch: $(uname -m)" >&2
    exit 2
    ;;
esac
bash scripts/build-core.sh --arch "$core_arch" --out "$root/desktop/build/core-$core_arch"

echo "[pack:dir] electron-builder --dir"
npm run pack -w desktop

# macOS 本地无发行证书时 electron-builder 不签名；ad-hoc 签名是 packaged smoke 的
# 可运行前提。正式 Developer ID/公证仍在发布流水线完成，不把 ad-hoc 当发布签名。
if [ "$(uname -s)" = "Darwin" ]; then
  app="$root/desktop/dist-app/mac-arm64/YaoMu Studio.app"
  if [ ! -d "$app" ]; then
    app=$(find "$root/desktop/dist-app" -maxdepth 2 -type d -name "YaoMu Studio.app" | head -n 1)
  fi
  if [ -n "${app:-}" ] && [ -d "$app" ]; then
    echo "[pack:dir] ad-hoc sign for local packaged smoke"
    codesign --force --deep --sign - "$app"
  else
    echo "[pack:dir] skip ad-hoc sign: app bundle not found"
  fi
fi

echo "[pack:dir] output: $root/desktop/dist-app"

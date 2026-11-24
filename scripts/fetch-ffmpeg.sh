#!/usr/bin/env bash
# 下载并解压固定版本的 Windows FFmpeg/ffprobe（Gyan 9.0.2 full build，GPLv3）。
# 用法：bash scripts/fetch-ffmpeg.sh [out_dir]
# 环境变量：
#   FFMPEG_ARCHIVE_ZIP  使用本地 7z（离线构建）
#   FFMPEG_RUNTIME_OUT  输出目录（默认 desktop/build/core）
#   FFMPEG_ARCHIVE_URL / FFMPEG_ARCHIVE_SHA256  覆盖内置固定值（仅测试用）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
version="9.0.2"
url="${FFMPEG_ARCHIVE_URL:-https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-${version}-full_build.7z}"
sha256="${FFMPEG_ARCHIVE_SHA256:-f0e46253c70dfe902bac915dfb4224f0cf1b7c6eeab9da2ccc9a5581f9a71b13}"
out="${1:-${FFMPEG_RUNTIME_OUT:-$root/desktop/build/core}}"
cache_dir="$root/desktop/build/.cache/ffmpeg"
archive="${FFMPEG_ARCHIVE_ZIP:-$cache_dir/ffmpeg-release-full.7z}"
extract_dir="$cache_dir/extract"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    python3 - "$1" <<'PY'
import hashlib, sys
h = hashlib.sha256()
with open(sys.argv[1], "rb") as f:
    for chunk in iter(lambda: f.read(1024 * 1024), b""):
        h.update(chunk)
print(h.hexdigest())
PY
  fi
}

mkdir -p "$cache_dir" "$out"
if [ ! -f "$archive" ]; then
  echo "[ffmpeg] download $url"
  curl -fL --retry 3 --connect-timeout 20 "$url" -o "$archive"
fi

actual=$(hash_file "$archive")
if [ "$actual" != "$sha256" ]; then
  echo "[ffmpeg] SHA-256 mismatch: $actual != $sha256" >&2
  exit 1
fi

rm -rf "$extract_dir"
mkdir -p "$extract_dir"

case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*)
    tar_bin="${TAR_BIN:-/c/Windows/System32/tar.exe}"
    ;;
  *)
    tar_bin="${TAR_BIN:-tar}"
    ;;
esac
if [ ! -x "$tar_bin" ] && ! command -v "$tar_bin" >/dev/null 2>&1; then
  echo "[ffmpeg] 找不到可用的 tar（需要支持 7z；Windows 使用 System32\\tar.exe）" >&2
  exit 1
fi

echo "[ffmpeg] extract -> $extract_dir"
"$tar_bin" -xf "$archive" -C "$extract_dir"

find_tool() {
  find "$extract_dir" -type f -name "$1" -print -quit
}
ffmpeg_src=$(find_tool "ffmpeg.exe")
ffprobe_src=$(find_tool "ffprobe.exe")
if [ -z "$ffmpeg_src" ] || [ -z "$ffprobe_src" ]; then
  ffmpeg_src=$(find_tool "ffmpeg")
  ffprobe_src=$(find_tool "ffprobe")
fi
if [ -z "$ffmpeg_src" ] || [ -z "$ffprobe_src" ]; then
  echo "[ffmpeg] 解压后未找到 ffmpeg/ffprobe" >&2
  exit 1
fi

cp -f "$ffmpeg_src" "$out/ffmpeg.exe"
cp -f "$ffprobe_src" "$out/ffprobe.exe"
chmod +x "$out/ffmpeg.exe" "$out/ffprobe.exe" 2>/dev/null || true

# 保留许可/来源信息，便于分发时展示 GPLv3 义务。
license_src=$(find "$extract_dir" -maxdepth 4 -type f \( -iname 'LICENSE.txt' -o -iname 'LICENSE' \) -print -quit)
if [ -n "$license_src" ]; then
  cp -f "$license_src" "$out/FFMPEG-LICENSE.txt"
fi

cat >"$out/FFMPEG-NOTICE.txt" <<EOF
FFmpeg ${version} full build (Gyan.dev)
Source: ${url}
SHA-256: ${sha256}
License: GPLv3 (build configured with --enable-gpl --enable-version3)
FFmpeg is a separate executable; see FFmpeg source and FFMPEG-LICENSE.txt.
EOF

echo "[ffmpeg] ready: $out/ffmpeg.exe, $out/ffprobe.exe"

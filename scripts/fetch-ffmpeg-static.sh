#!/usr/bin/env bash
# 下载固定版本的 macOS/Linux FFmpeg/ffprobe 静态构建（eugeneware/ffmpeg-static b6.1.1）。
# 用法：bash scripts/fetch-ffmpeg-static.sh --target <darwin-arm64|darwin-x64|linux-x64> [--out <dir>]
# 环境变量：
#   FFMPEG_STATIC_BASE_URL  覆盖下载源前缀（仅测试用）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
target=""
out="$root/desktop/build/core"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) target="${2:-}"; shift 2 ;;
    --out) out="${2:-}"; shift 2 ;;
    -h|--help)
      echo "usage: $0 --target <darwin-arm64|darwin-x64|linux-x64> [--out <dir>]"
      exit 0
      ;;
    *) echo "[ffmpeg-static] unknown argument: $1" >&2; exit 2 ;;
  esac
done

tag="b6.1.1"
base="${FFMPEG_STATIC_BASE_URL:-https://github.com/eugeneware/ffmpeg-static/releases/download/${tag}}"

case "$target" in
  darwin-arm64)
    ffmpeg_asset="ffmpeg-darwin-arm64"; ffmpeg_sha="a90e3db6a3fd35f6074b013f948b1aa45b31c6375489d39e572bea3f18336584"
    ffprobe_asset="ffprobe-darwin-arm64"; ffprobe_sha="bb2db6f5d8cef919da12fbf592119a987202a8c060a886f3cab091f9cab90b64"
    license_asset="darwin-arm64.LICENSE"; license_sha="cb48bf09a11f5fb576cddb0431c8f5ed0a60157a9ec942adffc13907cbe083f2"
    platform="darwin-arm64"
    ;;
  darwin-x64)
    ffmpeg_asset="ffmpeg-darwin-x64"; ffmpeg_sha="ebdddc936f61e14049a2d4b549a412b8a40deeff6540e58a9f2a2da9e6b18894"
    ffprobe_asset="ffprobe-darwin-x64"; ffprobe_sha="fa3add0ce901f7241abe0dfc0155d958fc834aca3f8ce61f87cc712ae669c1e0"
    license_asset="darwin-x64.LICENSE"; license_sha="2e1d16c72fd74e12063776371da757322f8b77589386532f4fd8634bde7de1af"
    platform="darwin-x64"
    ;;
  linux-x64)
    ffmpeg_asset="ffmpeg-linux-x64"; ffmpeg_sha="e7e7fb30477f717e6f55f9180a70386c62677ef8a4d4d1a5d948f4098aa3eb99"
    ffprobe_asset="ffprobe-linux-x64"; ffprobe_sha="4f231a1960d83e403d08f7971e271707bec278a9ae18e21b8b5b03186668450d"
    license_asset="linux-x64.LICENSE"; license_sha="8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903"
    platform="linux-x64"
    ;;
  *)
    echo "[ffmpeg-static] unsupported target: ${target:-<empty>}" >&2
    exit 2
    ;;
esac

cache_dir="$root/desktop/build/.cache/ffmpeg-static"
mkdir -p "$cache_dir" "$out"

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

fetch_asset() {
  asset="$1"; expected="$2"; dest="$3"
  cache="$cache_dir/$asset"
  if [ ! -f "$cache" ]; then
    echo "[ffmpeg-static] download $base/$asset"
    curl -fL --retry 3 --connect-timeout 20 "$base/$asset" -o "$cache"
  fi
  actual=$(hash_file "$cache")
  if [ "$actual" != "$expected" ]; then
    echo "[ffmpeg-static] SHA-256 mismatch for $asset: $actual != $expected" >&2
    exit 1
  fi
  cp -f "$cache" "$dest"
  chmod +x "$dest" 2>/dev/null || true
}

fetch_asset "$ffmpeg_asset" "$ffmpeg_sha" "$out/ffmpeg"
fetch_asset "$ffprobe_asset" "$ffprobe_sha" "$out/ffprobe"
fetch_asset "$license_asset" "$license_sha" "$out/FFMPEG-LICENSE.txt"

if grep -qi "Lesser General Public" "$out/FFMPEG-LICENSE.txt"; then
  license="LGPL v2.1+"
elif grep -q "GNU GENERAL PUBLIC LICENSE" "$out/FFMPEG-LICENSE.txt"; then
  license="GPL v3"
else
  license="see FFMPEG-LICENSE.txt"
fi

cat >"$out/FFMPEG-NOTICE.txt" <<EOF
FFmpeg static build (eugeneware/ffmpeg-static release ${tag}, ${platform})
Source: ${base}
ffmpeg SHA-256: ${ffmpeg_sha}
ffprobe SHA-256: ${ffprobe_sha}
License: ${license}
The release asset is pinned as b6.1.1; run "ffmpeg -version" on the target platform for the binary's reported version. FFmpeg is a separate executable; see the FFmpeg source and FFMPEG-LICENSE.txt.
EOF

echo "[ffmpeg-static] ready: $out/ffmpeg, $out/ffprobe"

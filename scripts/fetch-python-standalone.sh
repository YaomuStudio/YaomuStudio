#!/usr/bin/env bash
# 下载并解压固定版本的 python-build-standalone（macOS / Linux 随包 Python）。
# 用法：bash scripts/fetch-python-standalone.sh --target <darwin-arm64|darwin-x64|linux-x64> [--out <dir>]
# 环境变量：
#   PYTHON_STANDALONE_ARCHIVE  使用本地 tar.gz（离线构建）
#   PYTHON_STANDALONE_URL / PYTHON_STANDALONE_SHA256  覆盖内置固定值（仅测试用）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
target=""
out="$root/desktop/build/core/python"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) target="${2:-}"; shift 2 ;;
    --out) out="${2:-}"; shift 2 ;;
    -h|--help)
      echo "usage: $0 --target <darwin-arm64|darwin-x64|linux-x64> [--out <dir>]"
      exit 0
      ;;
    *) echo "[python-standalone] unknown argument: $1" >&2; exit 2 ;;
  esac
done

release="20261110"
version="3.12.16"
base="https://github.com/astral-sh/python-build-standalone/releases/download/${release}"

case "$target" in
  darwin-arm64)
    asset="cpython-${version}+${release}-aarch64-apple-darwin-install_only_stripped.tar.gz"
    sha256="2f11b8aa3726bdbd87dd9eb76568412faddf4f47e26913fa45d6a2e424972acd"
    ;;
  darwin-x64)
    asset="cpython-${version}+${release}-x86_64-apple-darwin-install_only_stripped.tar.gz"
    sha256="b78a4c479f1964ec484880bee567568266f68da102269fb93a05fe8716b45f91"
    ;;
  linux-x64)
    asset="cpython-${version}+${release}-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
    sha256="6a9e62dad033790aef722dbd370e0ce76ce17b73627ba3ae679f537049350b39"
    ;;
  *)
    echo "[python-standalone] unsupported target: ${target:-<empty>}" >&2
    exit 2
    ;;
esac

url="${PYTHON_STANDALONE_URL:-${base}/${asset}}"
expected="${PYTHON_STANDALONE_SHA256:-$sha256}"
cache_dir="$root/desktop/build/.cache/python-standalone"
archive="${PYTHON_STANDALONE_ARCHIVE:-$cache_dir/$asset}"

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

mkdir -p "$cache_dir" "$(dirname "$out")"
if [ ! -f "$archive" ]; then
  echo "[python-standalone] download $url"
  curl -fL --retry 3 --connect-timeout 20 "$url" -o "$archive"
fi

actual=$(hash_file "$archive")
if [ "$actual" != "$expected" ]; then
  echo "[python-standalone] SHA-256 mismatch: $actual != $expected" >&2
  exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/yaomu-python-standalone.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

echo "[python-standalone] extract $asset"
tar -xzf "$archive" -C "$tmp"
if [ ! -x "$tmp/python/bin/python3" ]; then
  echo "[python-standalone] archive does not contain python/bin/python3" >&2
  exit 1
fi

rm -rf "$out"
mv "$tmp/python" "$out"
chmod +x "$out/bin/python3" 2>/dev/null || true

license=""
if [ -f "$out/LICENSE" ]; then
  license="LICENSE"
fi
cat >"$out/RUNTIME-SOURCE.txt" <<EOF
Python ${version} (python-build-standalone ${release}, ${target})
Source: ${url}
SHA-256: ${sha256}
License: Python Software Foundation License${license:+ (see ${license})}${license:- (https://docs.python.org/3/license.html)}
EOF

echo "[python-standalone] ready: $out/bin/python3"

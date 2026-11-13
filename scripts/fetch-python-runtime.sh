#!/usr/bin/env bash
# 下载并解压固定版本的 Windows embeddable Python，供打包内 Runner 使用。
# 用法：bash scripts/fetch-python-runtime.sh [out_dir]
# 环境变量：
#   PYTHON_RUNTIME_ZIP  使用本地 zip（离线构建）
#   PYTHON_RUNTIME_OUT  输出目录（默认 desktop/build/core/python）
#   PYTHON_RUNTIME_URL / PYTHON_RUNTIME_SHA256  覆盖内置固定值（仅测试用）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
version="3.12.10"
url="${PYTHON_RUNTIME_URL:-https://www.python.org/ftp/python/${version}/python-${version}-embed-amd64.zip}"
sha256="${PYTHON_RUNTIME_SHA256:-4acbed6dd1c744b0376e3b1cf57ce906f9dc9e95e68824584c8099a63025a3c3}"
out="${1:-${PYTHON_RUNTIME_OUT:-$root/desktop/build/core/python}}"
cache_dir="$root/desktop/build/.cache/python"
zip="${PYTHON_RUNTIME_ZIP:-$cache_dir/python-${version}-embed-amd64.zip}"

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
if [ ! -f "$zip" ]; then
  echo "[python-runtime] download $url"
  curl -fL --retry 3 --connect-timeout 20 "$url" -o "$zip"
fi

actual=$(hash_file "$zip")
if [ "$actual" != "$sha256" ]; then
  echo "[python-runtime] SHA-256 mismatch: $actual != $sha256" >&2
  exit 1
fi

rm -rf "$out"
mkdir -p "$out"
if command -v unzip >/dev/null 2>&1; then
  unzip -q "$zip" -d "$out"
elif command -v python >/dev/null 2>&1; then
  python -m zipfile -e "$zip" "$out"
elif command -v python3 >/dev/null 2>&1; then
  python3 -m zipfile -e "$zip" "$out"
else
  echo "[python-runtime] 需要 unzip 或 python 来解压 $zip" >&2
  exit 1
fi

# embeddable Python 使用 python*._pth 控制 sys.path；把包内 runner_src 加进去。
# ._pth 相对 python.exe 所在目录，因此 ..\runner_src 指向 core/runner_src。
pth=$(find "$out" -maxdepth 1 -name 'python*._pth' -print -quit)
if [ -n "$pth" ]; then
  if ! grep -qE '(^|[\\/])runner_src$' "$pth"; then
    printf '\r\n..\\runner_src\r\n' >>"$pth"
  fi
else
  echo "[python-runtime] WARN: 未找到 python*._pth，Runner 可能无法 import" >&2
fi

if [ ! -f "$out/LICENSE.txt" ]; then
  echo "[python-runtime] WARN: embeddable zip 中未找到 LICENSE.txt" >&2
fi

cat >"$out/RUNTIME-SOURCE.txt" <<EOF
Python ${version} embeddable (Windows amd64)
Source: ${url}
SHA-256: ${sha256}
License: Python Software Foundation License (see LICENSE.txt)
EOF

echo "[python-runtime] ready: $out"

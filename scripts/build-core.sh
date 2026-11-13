#!/usr/bin/env bash
# 构建打包所需的本机资源：Core 二进制 + Runner 源码/启动器 + 随包运行时。
# 用法：bash scripts/build-core.sh [--out <dir>] [--arch arm64|x64]
#
# - Windows 宿主：生成 yaomu-core.exe、yaomu-runner.cmd，默认随包 Windows
#   embeddable Python 3.12.10 与 Gyan FFmpeg 9.0.2。
# - macOS/Linux 宿主：生成 yaomu-core、POSIX yaomu-runner，默认随包
#   python-build-standalone 3.12.16 与 ffmpeg-static 6.1.1。
# - --arch 允许在 Apple Silicon 上交叉暂存 x64 资源；目标 OS 仍为宿主 OS。
# - 显式 FFMPEG_BIN/FFPROBE_BIN 优先；BUNDLE_PYTHON=0/BUNDLE_FFMPEG=0 可关闭随包。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
app_server="$root/app-server"
out="$root/desktop/build/core"
arch=""

while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="${2:-}"; shift 2 ;;
    --arch) arch="${2:-}"; shift 2 ;;
    -h|--help)
      echo "usage: $0 [--out <dir>] [--arch arm64|x64]"
      exit 0
      ;;
    *) echo "[build-core] unknown argument: $1" >&2; exit 2 ;;
  esac
done

mkdir -p "$(dirname "$out")"
out="$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"

case "$(uname -s)" in
  Darwin) target_os="darwin" ;;
  Linux) target_os="linux" ;;
  MINGW* | MSYS* | CYGWIN*) target_os="win32" ;;
  *) echo "[build-core] unsupported host OS: $(uname -s)" >&2; exit 2 ;;
esac

host_arch=""
case "$(uname -m)" in
  arm64 | aarch64) host_arch="arm64" ;;
  x86_64 | amd64) host_arch="x64" ;;
  *) echo "[build-core] unsupported host arch: $(uname -m)" >&2; exit 2 ;;
esac
arch="${arch:-$host_arch}"

case "$target_os/$arch" in
  win32/x64 | darwin/arm64 | darwin/x64 | linux/x64) ;;
  *)
    echo "[build-core] unsupported target: $target_os/$arch (supported: win32/x64, darwin/arm64, darwin/x64, linux/x64)" >&2
    exit 2
    ;;
esac

goos="$target_os"
goarch="amd64"
core_name="yaomu-core"
if [ "$arch" = "arm64" ]; then
  goarch="arm64"
fi
if [ "$target_os" = "win32" ]; then
  goos="windows"
  core_name="yaomu-core.exe"
fi

echo "[build-core] target=$target_os/$arch out=$out"
echo "[build-core] go build -> $out/$core_name"
mkdir -p "$out"
(cd "$app_server" && GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "$out/$core_name" ./cmd/server)

echo "[build-core] copy runner source -> $out/runner_src"
rm -rf "$out/runner_src"
mkdir -p "$out/runner_src"
cp -R "$root/runner/src/." "$out/runner_src/"
# 本地 editable 安装会生成 egg-info，不属于随包源码；manifest 会拒绝未知顶层项。
rm -rf "$out/runner_src"/*.egg-info
# 部署树不得携带字节码缓存：-B 只禁止写入，已有 .pyc 仍可能被执行。
find "$out/runner_src" -type d -iname '__pycache__' -prune -exec rm -rf {} + 2>/dev/null || true
find "$out/runner_src" -type f \( -iname '*.pyc' -o -iname '*.pyo' \) -delete 2>/dev/null || true

# 随包 Runner 资源清单：版本 + 逐文件 SHA-256，供 Main 异步校验。
rm -f "$out/runner-manifest.json"
echo "[build-core] generate runner manifest -> $out/runner-manifest.json"
node "$root/scripts/generate-runner-manifest.mjs" \
  --runner-src "$out/runner_src" \
  --pyproject "$root/runner/pyproject.toml" \
  --out "$out/runner-manifest.json"

# 先清理上次暂存的媒体工具，避免本次未提供/未下载时把旧二进制带进包。
rm -f "$out/ffmpeg" "$out/ffprobe" "$out/ffmpeg.exe" "$out/ffprobe.exe"

# POSIX Runner 启动器：优先随包 Python，其次显式 PYTHON_BIN，最后系统 python3。
launcher="$out/yaomu-runner"
cat >"$launcher" <<'LAUNCHER'
#!/bin/sh
# 曜幕 Runner 启动器（由 scripts/build-core.sh 生成）：以源码形式运行受控执行适配器。
# -B / PYTHONDONTWRITEBYTECODE：不把 .pyc 写进 App 包内资源；签名后写入会破坏封装签名。
# -P 阻止 cwd 遮蔽，-s 排除用户 site。
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHONDONTWRITEBYTECODE=1
PYTHONPATH="$here/runner_src${PYTHONPATH:+:$PYTHONPATH}"
export PYTHONDONTWRITEBYTECODE PYTHONPATH
if [ -x "$here/python/bin/python3" ]; then
  exec "$here/python/bin/python3" -B -P -s -m yaomustudio_runner "$@"
fi
if [ -n "${PYTHON_BIN:-}" ] && [ -x "$PYTHON_BIN" ]; then
  exec "$PYTHON_BIN" -B -P -s -m yaomustudio_runner "$@"
fi
exec python3 -B -P -s -m yaomustudio_runner "$@"
LAUNCHER
chmod +x "$launcher" 2>/dev/null || true

# Windows 启动器：Go 的 os/exec 在 Windows 可直接执行 .cmd；Runner 本身拒绝命令行参数。
# 使用 CRLF 行尾，兼容 cmd.exe；优先使用随包 Python，其次 PYTHON_BIN，最后系统 python。
launcher_cmd="$out/yaomu-runner.cmd"
{
  printf '@echo off\r\n'
  printf 'setlocal\r\n'
  printf 'set "PYTHONDONTWRITEBYTECODE=1"\r\n'
  printf 'set "PYTHONPATH=%%~dp0runner_src;%%PYTHONPATH%%"\r\n'
  printf 'if exist "%%~dp0python\\python.exe" (\r\n'
  printf '  "%%~dp0python\\python.exe" -B -P -s -m yaomustudio_runner %%*\r\n'
  printf ') else (\r\n'
  printf '  if defined PYTHON_BIN (\r\n'
  printf '    "%%PYTHON_BIN%%" -B -P -s -m yaomustudio_runner %%*\r\n'
  printf '  ) else (\r\n'
  printf '    python -B -P -s -m yaomustudio_runner %%*\r\n'
  printf '  )\r\n'
  printf ')\r\n'
  printf 'exit /b %%ERRORLEVEL%%\r\n'
} >"$launcher_cmd"

# 可选：把调用方明确指定的固定 FFmpeg/ffprobe 暂存到打包资源。
stage_tool() {
  env_name="$1"
  src="$2"
  dest_name="$3"
  if [ -z "$src" ]; then
    return 0
  fi
  if [ ! -f "$src" ]; then
    echo "[build-core] WARN: $env_name=$src 不存在，跳过暂存" >&2
    return 0
  fi
  cp -L "$src" "$out/$dest_name"
  chmod +x "$out/$dest_name" 2>/dev/null || true
  echo "[build-core] staged $env_name -> $out/$dest_name"
}

is_windows=0
if [ "$target_os" = "win32" ]; then
  is_windows=1
fi
bundle_python="${BUNDLE_PYTHON:-1}"
bundle_ffmpeg="${BUNDLE_FFMPEG:-1}"
explicit_media=0
if [ -n "${FFMPEG_BIN:-}" ] || [ -n "${FFPROBE_BIN:-}" ]; then
  explicit_media=1
fi

if [ "$is_windows" = "1" ]; then
  stage_tool "FFMPEG_BIN" "${FFMPEG_BIN:-}" "ffmpeg.exe"
  stage_tool "FFPROBE_BIN" "${FFPROBE_BIN:-}" "ffprobe.exe"
  if [ "$bundle_ffmpeg" = "1" ] && [ "$explicit_media" = "0" ]; then
    FFMPEG_RUNTIME_OUT="$out" bash "$root/scripts/fetch-ffmpeg.sh"
  fi
  if [ "$bundle_python" = "1" ]; then
    PYTHON_RUNTIME_OUT="$out/python" bash "$root/scripts/fetch-python-runtime.sh"
  else
    rm -rf "$out/python"
  fi
else
  stage_tool "FFMPEG_BIN" "${FFMPEG_BIN:-}" "ffmpeg"
  stage_tool "FFPROBE_BIN" "${FFPROBE_BIN:-}" "ffprobe"
  if [ "$bundle_ffmpeg" = "1" ] && [ "$explicit_media" = "0" ]; then
    bash "$root/scripts/fetch-ffmpeg-static.sh" --target "$target_os-$arch" --out "$out"
  fi
  if [ "$bundle_python" = "1" ]; then
    bash "$root/scripts/fetch-python-standalone.sh" --target "$target_os-$arch" --out "$out/python"
  else
    rm -rf "$out/python"
  fi
fi

echo "[build-core] done:"
ls -l "$out"

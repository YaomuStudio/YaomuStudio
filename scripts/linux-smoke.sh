#!/usr/bin/env bash
# Linux 桌面验证入口：开发态隐藏窗口 smoke → 打包目录 → 打包态 smoke → AppImage 构建。
#
# 用法：bash scripts/linux-smoke.sh [--skip-packaged] [--skip-appimage]
#
# 前置：
# - 已执行 npm ci（node_modules 与 Electron 已就绪）；
# - 已安装 Electron 运行时依赖：bash scripts/install-linux-electron-deps.sh；
# - 依赖 xvfb-run/node/go/python3；缺一即失败并打印安装提示。
#
# 环境变量：
# - SMOKE_ELECTRON_ARGS：测试专用额外 Electron/打包二进制参数（按空白拆分），
#   容器 root 或缺少 user namespace 时传 `--no-sandbox`；不进入生产启动路径。
# - SMOKE_TIMEOUT_MS：单个 smoke 场景超时（毫秒）。
#
# 产物：.design-build/linux-smoke/<UTC 时间戳>/（已 gitignore）
# - <步骤>.log：完整输出（同时打印到控制台，不吞日志）
# - env.log：系统与工具版本
# - screenshots/：冒烟截图/PDF 副本（按阶段加前缀）
# - appimage.txt：AppImage 路径、大小、SHA-256
#
# 覆盖边界：无 GPU、无真实桌面会话；AppImage 仅构建，不验证 FUSE 挂载体验。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

skip_packaged=0
skip_appimage=0
for arg in "$@"; do
  case "$arg" in
    --skip-packaged) skip_packaged=1 ;;
    --skip-appimage) skip_appimage=1 ;;
    -h | --help)
      sed -n '2,22p' "$0"
      exit 0
      ;;
    *)
      echo "未知参数: $arg（使用 --help 查看用法）" >&2
      exit 2
      ;;
  esac
done

ts=$(date -u +%Y%m%dT%H%M%SZ)
log_dir="$root/.design-build/linux-smoke/$ts"
mkdir -p "$log_dir/screenshots"

# 未设置时不导出任何额外参数，保持与普通启动一致。
export SMOKE_ELECTRON_ARGS="${SMOKE_ELECTRON_ARGS:-}"

echo "[linux-smoke] 日志目录: $log_dir"
{
  echo "timestamp: $(date -u +%FT%TZ)"
  uname -a
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    echo "os: ${PRETTY_NAME:-unknown}"
  fi
  echo "node: $(node -v 2>/dev/null || echo missing)"
  echo "npm: $(npm -v 2>/dev/null || echo missing)"
  echo "go: $(go version 2>/dev/null || echo missing)"
  echo "python3: $(python3 -V 2>&1 || echo missing)"
  echo "ffmpeg: $(ffmpeg -version 2>/dev/null | head -n 1 || echo missing)"
  echo "ffprobe: $(ffprobe -version 2>/dev/null | head -n 1 || echo missing)"
  echo "electron: $(node -p "require('./node_modules/electron/package.json').version" 2>/dev/null || echo missing)"
  echo "xvfb-run: $(command -v xvfb-run 2>/dev/null || echo missing)"
  echo "SMOKE_ELECTRON_ARGS: ${SMOKE_ELECTRON_ARGS:-<empty>}"
  echo "SMOKE_TIMEOUT_MS: ${SMOKE_TIMEOUT_MS:-<default>}"
} | tee "$log_dir/env.log"

preflight_failed=0
require_cmd() {
  local cmd="$1"
  local hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "[linux-smoke] 缺少命令: $cmd（$hint）" >&2
    preflight_failed=1
  fi
}

require_cmd xvfb-run "apt-get install -y xvfb"
require_cmd node "安装 Node 22"
require_cmd npm "随 Node 安装"
require_cmd go "安装 Go 1.23+（见 app-server/go.mod）"
require_cmd python3 "apt-get install -y python3（Runner 需要 >= 3.11）"
require_cmd sha256sum "coreutils"

if [ ! -x "$root/node_modules/.bin/electron" ]; then
  echo "[linux-smoke] 未找到 Electron：先运行 npm ci" >&2
  preflight_failed=1
fi
if [ ! -x "$root/node_modules/.bin/electron-builder" ]; then
  echo "[linux-smoke] 未找到 electron-builder：先运行 npm ci" >&2
  preflight_failed=1
fi
if [ "$preflight_failed" -ne 0 ]; then
  echo "[linux-smoke] 预检失败，未执行任何步骤" >&2
  exit 1
fi

step_index=0
run_step() {
  local name="$1"
  shift
  step_index=$((step_index + 1))
  local log="$log_dir/$(printf '%02d' "$step_index")-$name.log"
  local start end status
  echo "[linux-smoke] ==> $name"
  start=$(date +%s)
  # 保留命令本身的退出码（PIPESTATUS[0]），同时把完整输出写入日志。
  set +e
  "$@" 2>&1 | tee "$log"
  local -a statuses=("${PIPESTATUS[@]}")
  set -e
  status="${statuses[0]}"
  end=$(date +%s)
  echo "[linux-smoke] <== $name exit=$status duration=$((end - start))s log=$log"
  if [ "$status" -ne 0 ]; then
    echo "[linux-smoke] FAIL: $name 退出码 $status" >&2
    exit "$status"
  fi
}

# Electron 44 起二进制改为首次调用时下载（npm ci 不再执行 postinstall），
# 先显式准备，避免把下载耗时计入 smoke 场景超时。
if [ ! -f "$root/node_modules/electron/dist/version" ]; then
  echo "[linux-smoke] Electron 二进制未就绪，执行 install.js"
  run_step electron-install node node_modules/electron/install.js
fi

# 冒烟截图落在 /tmp/yaomu-smoke（smoke.ts）；按阶段复制到日志目录，避免被下一阶段覆盖。
capture_screenshots() {
  local stage="$1"
  local src="/tmp/yaomu-smoke"
  local copied=0
  if [ ! -d "$src" ]; then
    echo "[linux-smoke] note: $src 不存在，无可复制截图"
    return 0
  fi
  for file in "$src"/*.png "$src"/*.pdf; do
    [ -f "$file" ] || continue
    cp -f "$file" "$log_dir/screenshots/$stage-$(basename "$file")"
    copied=$((copied + 1))
  done
  echo "[linux-smoke] $stage 截图副本: $copied -> $log_dir/screenshots"
}

rm -rf /tmp/yaomu-smoke
run_step dev-smoke xvfb-run -a node desktop/scripts/smoke.mjs
capture_screenshots dev

# 打包目录同时是打包态 smoke 与 AppImage 的共同前置；两个后续步骤都跳过时无需打包。
if [ "$skip_packaged" -eq 0 ] || [ "$skip_appimage" -eq 0 ]; then
  run_step pack-dir bash scripts/pack-dir.sh
  run_step bundled-runtime bash -c '
    core="$1/desktop/build/core-x64"
    for file in "$core/yaomu-core" "$core/yaomu-runner" "$core/python/bin/python3" "$core/ffmpeg" "$core/ffprobe"; do
      [ -x "$file" ] || { echo "missing or not executable: $file" >&2; exit 1; }
    done
    "$core/python/bin/python3" -V
    "$core/ffmpeg" -version | head -n 1
    "$core/ffprobe" -version | head -n 1
  ' _ "$root"
else
  echo "[linux-smoke] 跳过 pack-dir（--skip-packaged --skip-appimage）"
fi

if [ "$skip_packaged" -eq 0 ]; then
  rm -rf /tmp/yaomu-smoke
  run_step packaged-smoke xvfb-run -a node desktop/scripts/smoke-packaged.mjs
  capture_screenshots packaged
else
  echo "[linux-smoke] 跳过 packaged-smoke（--skip-packaged）"
fi

if [ "$skip_appimage" -eq 0 ]; then
  # executableName 已在 desktop/electron-builder.yml 固定为 yaomustudio；
  # 不再用 CLI 覆盖，验证产品配置本身能产出 AppImage。
  run_step appimage-build bash -c 'cd desktop && ../node_modules/.bin/electron-builder --linux AppImage --publish never'
  shopt -s nullglob
  appimages=("$root"/desktop/dist-app/*.AppImage)
  shopt -u nullglob
  if [ "${#appimages[@]}" -eq 0 ]; then
    echo "[linux-smoke] FAIL: electron-builder 未产出 AppImage" >&2
    exit 1
  fi
  : >"$log_dir/appimage.txt"
  for appimage in "${appimages[@]}"; do
    size=$(stat -c %s "$appimage")
    sha=$(sha256sum "$appimage" | cut -d' ' -f1)
    printf 'appimage: %s\nsize: %s bytes\nsha256: %s\n' "$appimage" "$size" "$sha" |
      tee -a "$log_dir/appimage.txt"
  done
else
  echo "[linux-smoke] 跳过 AppImage 构建（--skip-appimage）"
fi

echo "[linux-smoke] PASS"
echo "[linux-smoke] 未覆盖：GPU/模型执行、真实桌面会话、Wayland、FUSE 挂载、安装器/签名。"

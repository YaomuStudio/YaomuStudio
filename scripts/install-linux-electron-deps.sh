#!/usr/bin/env bash
# 安装 Linux 验证环境所需的 Electron/Chromium 运行时依赖与 Xvfb/FFmpeg。
#
# 用法：bash scripts/install-linux-electron-deps.sh
#
# 范围：
# - 只服务测试/CI 环境（隐藏窗口冒烟、打包态冒烟、AppImage 构建），不进入产品运行时。
# - 只支持 apt（Ubuntu/Debian）；基线为 Ubuntu 24.04（t64 包名）。
# - 幂等：已安装的包跳过；非 root 时经 sudo 执行。
#
# 注意：这里是验证环境依赖，不是产品分发包的依赖声明；真实发行版依赖仍属后续工作
set -euo pipefail

if [ "$(uname -s)" != "Linux" ]; then
  echo "[install-linux-electron-deps] 仅支持 Linux（当前: $(uname -s)）" >&2
  exit 1
fi

if ! command -v apt-get >/dev/null 2>&1; then
  echo "[install-linux-electron-deps] 未找到 apt-get；本脚本只支持 Ubuntu/Debian" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "[install-linux-electron-deps] 非 root 且缺少 sudo；请以 root 运行或安装 sudo" >&2
    exit 1
  fi
  sudo_cmd=(sudo)
else
  sudo_cmd=()
fi

# Ubuntu 24.04 (noble) 的 t64 包名；Electron/Chromium 启动所需共享库。
packages=(
  xvfb
  libnss3
  libatk1.0-0t64
  libatk-bridge2.0-0t64
  libcups2t64
  libdrm2
  libgbm1
  libgtk-3-0t64
  libasound2t64
  libxss1
  libxshmfence1
  libxkbcommon0
  libxcomposite1
  libxdamage1
  libxfixes3
  libxrandr2
  libatspi2.0-0t64
  libpango-1.0-0
  libcairo2
  ffmpeg
  # appimage-12.0.1 的 linux-arm64 runtime 声明 NEEDED libz.so（无版本号），
  # Ubuntu 只有 zlib1g 时缺该符号链接；仅用于可选的 AppImage 运行验证。
  # x64 runtime 声明的是 libz.so.1，不需要本包。
  zlib1g-dev
)

missing=()
for package in "${packages[@]}"; do
  if ! dpkg -s "$package" >/dev/null 2>&1; then
    missing+=("$package")
  fi
done

if [ "${#missing[@]}" -eq 0 ]; then
  echo "[install-linux-electron-deps] 依赖已全部安装，跳过 apt"
else
  echo "[install-linux-electron-deps] 安装: ${missing[*]}"
  "${sudo_cmd[@]}" apt-get update
  DEBIAN_FRONTEND=noninteractive "${sudo_cmd[@]}" apt-get install -y --no-install-recommends "${missing[@]}"
fi

echo "[install-linux-electron-deps] 完成"
for tool in xvfb-run ffmpeg; do
  if command -v "$tool" >/dev/null 2>&1; then
    echo "  $tool: $(command -v "$tool")"
  else
    echo "  $tool: MISSING" >&2
    exit 1
  fi
done

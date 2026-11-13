#!/usr/bin/env bash
# 媒体链路测试入口（ffprobe 必需）。
#
# 用法：
#   bash scripts/test-media.sh        # 或 npm run test:media
#
# 行为：
# - 检查 go / ffmpeg / ffprobe / python3；缺失时打印 BLOCKED、安装提示与日志路径，
#   退出 78（非零）。不把缺失前置当成通过。
# - 设置 FFPROBE_REQUIRED=1 运行 TestExportExecutionIntegration（复用现有集成用例，
#   不重复造探测测试），要求真实执行 ffprobe 媒体断言。
# - 日志写入 .design-build/test-media/<UTC 时间戳>/media.log（已 gitignore）。
#
# 可选环境变量：FFMPEG_BIN / FFPROBE_BIN / PYTHON_BIN 指向具体可执行文件；
# 未设置时从 PATH 解析，并以解析结果显式传给测试。
#
# 约束：不联网安装、不修改系统环境；不记录媒体/字幕正文、密钥或完整环境变量。
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
ts=$(date -u +%Y%m%dT%H%M%SZ)
log_dir="$root/.design-build/test-media/$ts"
log="$log_dir/media.log"
mkdir -p "$log_dir"

resolve_tool() {
  local env_key="$1" name="$2" candidate
  candidate="$(printenv "$env_key" 2>/dev/null || true)"
  if [ -n "$candidate" ]; then
    if [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
    return 1
  fi
  command -v "$name" 2>/dev/null
}

install_hint() {
  cat <<'EOF'
安装提示（安装后重跑本脚本）：
  macOS:         brew install ffmpeg        # 含 ffprobe
  Ubuntu/Debian: sudo apt-get install -y --no-install-recommends ffmpeg
  其他平台:       安装 FFmpeg 发行版，或用 FFMPEG_BIN/FFPROBE_BIN 指定可执行文件
EOF
}

ffmpeg="$(resolve_tool FFMPEG_BIN ffmpeg || true)"
ffprobe="$(resolve_tool FFPROBE_BIN ffprobe || true)"
python="$(resolve_tool PYTHON_BIN python3 || true)"
go_bin="$(command -v go 2>/dev/null || true)"

missing=""
for entry in "go:$go_bin" "ffmpeg:$ffmpeg" "ffprobe:$ffprobe" "python3:$python"; do
  name="${entry%%:*}"
  path="${entry#*:}"
  if [ -z "$path" ]; then
    missing="$missing $name"
  fi
done

if [ -n "$missing" ]; then
  {
    echo "timestamp: $(date -u +%FT%TZ)"
    echo "platform: $(uname -s) $(uname -m)"
    echo "result: BLOCKED"
    echo "missing:$missing"
  } >"$log" 2>&1
  echo "===== 媒体链路测试 ====="
  echo "结果: BLOCKED（缺少:${missing}）"
  install_hint
  echo "日志: $log"
  exit 78
fi

{
  echo "timestamp: $(date -u +%FT%TZ)"
  echo "platform: $(uname -s) $(uname -m)"
  echo "go: $("$go_bin" version 2>&1)"
  echo "ffmpeg: $("$ffmpeg" -version 2>/dev/null | head -1)"
  echo "ffprobe: $("$ffprobe" -version 2>/dev/null | head -1)"
  echo "python3: $("$python" -V 2>&1)"
} >"$log" 2>&1
cat "$log"
echo

echo "== TestExportExecutionIntegration (FFPROBE_REQUIRED=1；ffprobe 断言必须真实执行) =="
(
  cd "$root/app-server" &&
    FFPROBE_REQUIRED=1 \
      FFMPEG_BIN="$ffmpeg" \
      FFPROBE_BIN="$ffprobe" \
      PYTHON_BIN="$python" \
      "$go_bin" test -run '^TestExportExecutionIntegration$' ./internal/scheduler -count=1 -v
) >>"$log" 2>&1
code=$?

echo "--- 日志末尾（完整日志: ${log}）---"
tail -40 "$log"
echo "---"

if [ "$code" -ne 0 ]; then
  echo "结果: FAILED（退出码 ${code}；日志: ${log}）"
  exit 1
fi

if grep -Eq 'FFPROBE_UNAVAILABLE|FFMPEG_NOT_AVAILABLE|PYTHON_NOT_AVAILABLE|MEDIA_HELPER_UNSUPPORTED|^[[:space:]]*--- SKIP:' "$log"; then
  echo "结果: FAILED（测试被跳过或降级，未真实执行 ffprobe 断言；日志: ${log}）"
  exit 1
fi

if ! grep -q 'ffprobe .*format=' "$log"; then
  echo "结果: FAILED（未观察到 ffprobe 媒体断言输出；日志: ${log}）"
  exit 1
fi

echo "结果: PASS（ffprobe 断言已实际执行；日志: ${log}）"
exit 0

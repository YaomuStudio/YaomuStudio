#!/usr/bin/env bash
# macOS 自动化测试基线入口。
#
# 用法：
#   bash scripts/test-macos.sh [--allow-blocked] [--skip-go] [--skip-runner] \
#     [--skip-build] [--skip-smoke] [--skip-pack] [--skip-packaged]
#
# 步骤：环境预检 → Go(vet + test -race) → Runner(pytest) → UI/Desktop 构建 →
#       开发态隐藏窗口 smoke → 打包目录 → 打包态 smoke。
#
# 结果：
# - 每步日志与 results.json 写入 .design-build/test-macos/<UTC 时间戳>/（已 gitignore）。
# - fail 或 blocked 时退出 1；blocked 表示缺前置（如 .venv/pytest），不等于通过。
#   --allow-blocked 可把 blocked 视为可接受（仍打印与记录）。
# - --skip-* 为显式跳过，状态记 skipped，不计为失败。
#
# Runner 前置（不在本脚本内联网安装）：
#   uv venv .venv && uv pip install --python .venv/bin/python -e "runner[dev]"
#
# 约束：不修改用户默认环境；不记录媒体/字幕正文、密钥或完整环境变量；不关闭 Gatekeeper。
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
ts=$(date -u +%Y%m%dT%H%M%SZ)
log_dir="$root/.design-build/test-macos/$ts"
summary="$log_dir/steps.tsv"
mkdir -p "$log_dir"
: >"$summary"

allow_blocked=0
skip_go=0
skip_runner=0
skip_build=0
skip_smoke=0
skip_pack=0
skip_packaged=0

for arg in "$@"; do
  case "$arg" in
    --allow-blocked) allow_blocked=1 ;;
    --skip-go) skip_go=1 ;;
    --skip-runner) skip_runner=1 ;;
    --skip-build) skip_build=1 ;;
    --skip-smoke) skip_smoke=1 ;;
    --skip-pack) skip_pack=1 ;;
    --skip-packaged) skip_packaged=1 ;;
    -h | --help)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    *)
      echo "未知参数: $arg（使用 --help 查看用法）" >&2
      exit 2
      ;;
  esac
done

platform="$(sw_vers -productName 2>/dev/null || uname -s) $(sw_vers -productVersion 2>/dev/null || true) $(uname -m)"

block_reason=""
blocked() {
  block_reason="$1"
  return 78
}

record_env() {
  {
    echo "timestamp: $(date -u +%FT%TZ)"
    echo "platform: $platform"
    sw_vers 2>/dev/null || true
    uname -m
    echo "go: $(go version 2>/dev/null || echo missing)"
    echo "node: $(node -v 2>/dev/null || echo missing)"
    echo "npm: $(npm -v 2>/dev/null || echo missing)"
    echo "python3: $(python3 -V 2>&1 || echo missing)"
    if [ -x "$root/.venv/bin/python" ]; then
      echo "venv-python: $("$root/.venv/bin/python" -V 2>&1)"
      echo "pytest: $("$root/.venv/bin/python" -m pytest --version 2>&1 | head -1)"
    else
      echo "venv-python: missing"
      echo "pytest: missing (.venv not found)"
    fi
    echo "ffmpeg: $(ffmpeg -version 2>/dev/null | head -1 || echo missing)"
    echo "ffprobe: $(ffprobe -version 2>/dev/null | head -1 || echo missing)"
    if [ -x "$root/node_modules/.bin/electron" ]; then
      echo "electron: present"
    else
      echo "electron: missing"
    fi
  } >"$log_dir/env.log" 2>&1
}

step_env() {
  record_env
}

step_go() {
  command -v go >/dev/null 2>&1 || {
    blocked "go 不在 PATH"
    return $?
  }
  local flags="-race"
  if [ "$(go env CGO_ENABLED 2>/dev/null)" != "1" ] || ! command -v cc >/dev/null 2>&1; then
    flags=""
    echo "note: CGO/cc 不可用，降级为不带 -race"
  fi
  (
    cd "$root/app-server" &&
      go vet ./... &&
      go test $flags ./... -count=1
  )
}

step_runner() {
  local py="$root/.venv/bin/python"
  if [ ! -x "$py" ]; then
    blocked "缺少 .venv；运行: uv venv .venv && uv pip install --python .venv/bin/python -e 'runner[dev]'"
    return $?
  fi
  if ! "$py" -m pytest --version >/dev/null 2>&1; then
    blocked ".venv 中未安装 pytest；运行: uv pip install --python .venv/bin/python -e 'runner[dev]'"
    return $?
  fi
  (
    cd "$root" &&
      "$py" -m pytest runner/tests -q -rs
  )
}

step_build() {
  command -v npm >/dev/null 2>&1 || {
    blocked "npm 不在 PATH"
    return $?
  }
  (
    cd "$root" &&
      npm run typecheck &&
      npm run build
  )
}

step_smoke() {
  if [ ! -x "$root/node_modules/.bin/electron" ]; then
    blocked "Electron 未安装（node_modules/.bin/electron）"
    return $?
  fi
  (
    cd "$root" &&
      node desktop/scripts/smoke.mjs
  )
}

step_pack() {
  command -v npm >/dev/null 2>&1 || {
    blocked "npm 不在 PATH"
    return $?
  }
  (
    cd "$root" &&
      bash scripts/pack-dir.sh
  )
}

step_packaged() {
  if [ ! -x "$root/node_modules/.bin/electron" ]; then
    blocked "Electron 未安装（node_modules/.bin/electron）"
    return $?
  fi
  if [ ! -d "$root/desktop/dist-app" ]; then
    blocked "缺少 desktop/dist-app；先运行 bash scripts/pack-dir.sh（或去掉 --skip-pack）"
    return $?
  fi
  (
    cd "$root" &&
      node desktop/scripts/smoke-packaged.mjs
  )
}

fail_count=0
blocked_count=0
skipped_count=0
pass_count=0

run_step() {
  local name="$1" fn="$2" skip="$3"
  local log="$log_dir/$name.log"
  local start end duration code status note
  block_reason=""

  if [ "$skip" = "1" ]; then
    printf '%s\tskipped\t-\t0\t%s\n' "$name" "显式 --skip 跳过" >>"$summary"
    skipped_count=$((skipped_count + 1))
    printf '[skip] %s（显式跳过）\n' "$name"
    return 0
  fi

  printf '[run ] %s\n' "$name"
  start=$(date +%s)
  "$fn" >"$log" 2>&1
  code=$?
  end=$(date +%s)
  duration=$((end - start))

  if [ "$code" -eq 0 ]; then
    status=pass
    note=""
    pass_count=$((pass_count + 1))
  elif [ "$code" -eq 78 ]; then
    status=blocked
    note="$block_reason"
    blocked_count=$((blocked_count + 1))
  else
    status=fail
    note="退出码 $code"
    fail_count=$((fail_count + 1))
  fi

  printf '%s\t%s\t%s\t%s\t%s\n' "$name" "$status" "$code" "$duration" "$note" >>"$summary"
  printf '[%s] %s（%ss）\n' "$status" "$name" "$duration"
  if [ "$status" = "fail" ]; then
    echo "--- $name 日志末尾 ---"
    tail -20 "$log"
    echo "--- 日志: $log ---"
  elif [ "$status" = "blocked" ]; then
    echo "      原因: $note"
  fi
}

run_step env step_env 0
run_step go step_go "$skip_go"
run_step runner step_runner "$skip_runner"
run_step build step_build "$skip_build"
run_step smoke step_smoke "$skip_smoke"
run_step pack step_pack "$skip_pack"
run_step packaged step_packaged "$skip_packaged"

if command -v python3 >/dev/null 2>&1; then
  python3 - "$summary" "$log_dir" "$platform" "$ts" "$allow_blocked" <<'PY'
import json
import pathlib
import sys

summary, log_dir, platform, ts, allow_blocked = sys.argv[1:6]
steps = []
for line in pathlib.Path(summary).read_text(encoding="utf-8").splitlines():
    parts = line.split("\t")
    if len(parts) < 5:
        continue
    name, status, code, duration, note = parts[:5]
    steps.append({
        "name": name,
        "status": status,
        "exit": None if code == "-" else int(code),
        "durationSeconds": int(duration),
        "note": note,
    })
doc = {
    "timestampUtc": ts,
    "platform": platform,
    "allowBlocked": allow_blocked == "1",
    "logs": log_dir,
    "steps": steps,
}
pathlib.Path(log_dir, "results.json").write_text(
    json.dumps(doc, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
)
PY
else
  echo "warning: python3 不可用，未生成 results.json（steps.tsv 已保留）"
fi

echo
echo "===== macOS 测试基线汇总 ====="
echo "平台: $platform"
echo "日志: $log_dir"
printf '%-10s %-8s %-6s %s\n' "STEP" "STATUS" "EXIT" "SECONDS"
while IFS=$'\t' read -r name status code duration note; do
  printf '%-10s %-8s %-6s %s\n' "$name" "$status" "$code" "$duration"
done <"$summary"
echo "pass=$pass_count blocked=$blocked_count fail=$fail_count skipped=$skipped_count"
echo "未覆盖（不在本脚本范围）：真实模型/FlashCLI、ffprobe 依赖用例、签名/公证、UI E2E、CI。"

if [ "$fail_count" -gt 0 ]; then
  echo "结果: FAILED"
  exit 1
fi
if [ "$blocked_count" -gt 0 ] && [ "$allow_blocked" -ne 1 ]; then
  echo "结果: BLOCKED（如需接受阻断，使用 --allow-blocked）"
  exit 1
fi
if [ "$blocked_count" -gt 0 ] && [ "$allow_blocked" -eq 1 ]; then
  echo "结果: PASS（含 $blocked_count 个 blocked，已显式接受）"
  exit 0
fi
echo "结果: PASS"
exit 0

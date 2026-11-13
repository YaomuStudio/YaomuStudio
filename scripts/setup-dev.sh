#!/bin/sh
# 本地开发环境初始化：启用 git hooks。
# 用法：./scripts/setup-dev.sh

set -e

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

git config core.hooksPath .githooks
chmod +x .githooks/* 2>/dev/null || true
echo "✅ 已启用 .githooks（pre-commit 拦截密钥 / commit-msg 校验提交信息）"
echo ""
echo "完成。提交前请确认：git status / git diff --staged"

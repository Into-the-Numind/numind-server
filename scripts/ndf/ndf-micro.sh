#!/usr/bin/env bash
# ndf-micro: Micro 档启动器 (ndf-start micro 的友好封装)
#
# Usage:
#   ndf-micro <name> [--repos repo1,...]
#
# 提醒 Micro 档边界，然后调 ndf-start micro。
# 真正的边界检查在 ndf-done 时跑（看实际改动的文件路径）。

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ $# -lt 1 ]]; then
  cat <<'EOF'
Usage: ndf-micro <name> [--repos repo1,...]

Micro 档（5-15 min 极快通道）适用：
  ✓ 改文案 / 改样式（颜色、字号、间距、margin）
  ✓ rename 变量 / 函数名
  ✓ 加注释 / 删死代码
  ✓ 改文档（README.md, CLAUDE.md 等）
  ✓ 配套测试（_test.go）

Micro 不允许：
  ✗ 动数据库 schema（migrations/）
  ✗ 新增 API 端点（改 router.go）
  ✗ 改业务逻辑（biz/ 下文件）
  ✗ 加新业务代码文件（除测试外）

不确定？升 Hotfix:  ndf-start hotfix <name>

Examples:
  ndf-micro update-claude-date
  ndf-micro rename-getUserById --repos numind-server
  ndf-micro fix-button-color --repos numind-web-v3
EOF
  exit 1
fi

exec "$SCRIPT_DIR/ndf-start.sh" micro "$@"

#!/usr/bin/env bash
# ndf-install-hooks: 一键安装/卸载 NDF git hooks 到 3 个仓库
#
# Usage:
#   ndf-install-hooks            # 安装到 numind-server / numind-web-v3 / numind-admin-web
#   ndf-install-hooks --uninstall # 卸载（恢复 .bak 备份）
#   ndf-install-hooks --status   # 检查每个仓库的安装状态

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"
HOOK_SOURCE="$SCRIPT_DIR/hooks/pre-push.sample"

REPOS=("numind-server" "numind-web-v3" "numind-admin-web")
ACTION="install"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --uninstall) ACTION="uninstall"; shift ;;
    --status)    ACTION="status"; shift ;;
    -h|--help)
      cat <<EOF
Usage: ndf-install-hooks [--uninstall|--status]

Actions:
  (default)     安装 pre-push hook 到 3 个仓库
  --uninstall   恢复备份（如果有），删除 NDF hook
  --status      显示每个仓库的安装状态
EOF
      exit 0
      ;;
    *) echo "Error: unknown arg: $1" >&2; exit 1 ;;
  esac
done

# Check source hook exists
if [[ ! -f "$HOOK_SOURCE" ]]; then
  echo "Error: source hook not found: $HOOK_SOURCE" >&2
  exit 1
fi

# 用 hook 头部的标识识别"NDF hook"
NDF_MARKER="NDF v2 pre-push hook"

print_status_line() {
  local REPO="$1"
  local HOOK_PATH="$NDF_CODES_ROOT/$REPO/.git/hooks/pre-push"
  if [[ ! -f "$HOOK_PATH" ]]; then
    echo "  $REPO: 未安装"
    return
  fi
  if grep -q "$NDF_MARKER" "$HOOK_PATH" 2>/dev/null; then
    echo "  $REPO: ✓ NDF hook 已安装"
  else
    echo "  $REPO: ⚠️  存在 pre-push 但非 NDF（不会自动覆盖）"
  fi
}

case "$ACTION" in
  status)
    echo "─── NDF pre-push hook 状态 ───"
    for REPO in "${REPOS[@]}"; do
      print_status_line "$REPO"
    done
    exit 0
    ;;
  install)
    INSTALLED=0
    for REPO in "${REPOS[@]}"; do
      REPO_PATH="$NDF_CODES_ROOT/$REPO"
      HOOK_PATH="$REPO_PATH/.git/hooks/pre-push"

      if [[ ! -d "$REPO_PATH/.git" ]]; then
        echo "⚠️  $REPO: 不是 git repo, 跳过"
        continue
      fi

      mkdir -p "$REPO_PATH/.git/hooks"

      # 如果已有 hook
      if [[ -f "$HOOK_PATH" ]]; then
        if grep -q "$NDF_MARKER" "$HOOK_PATH" 2>/dev/null; then
          echo "ℹ️  $REPO: NDF hook 已存在, 覆盖更新"
        else
          # 非 NDF hook, 备份
          BACKUP="$HOOK_PATH.bak.$(date +%s)"
          mv "$HOOK_PATH" "$BACKUP"
          echo "ℹ️  $REPO: 已备份原 pre-push 到 $BACKUP"
        fi
      fi

      cp "$HOOK_SOURCE" "$HOOK_PATH"
      chmod +x "$HOOK_PATH"
      echo "✓ $REPO: 安装到 $HOOK_PATH"
      INSTALLED=$((INSTALLED + 1))
    done
    echo
    echo "─── 完成: $INSTALLED/${#REPOS[@]} 个仓库已安装 ───"
    ;;
  uninstall)
    UNINSTALLED=0
    for REPO in "${REPOS[@]}"; do
      REPO_PATH="$NDF_CODES_ROOT/$REPO"
      HOOK_PATH="$REPO_PATH/.git/hooks/pre-push"

      if [[ ! -f "$HOOK_PATH" ]]; then
        echo "ℹ️  $REPO: 没装 hook, 跳过"
        continue
      fi

      if ! grep -q "$NDF_MARKER" "$HOOK_PATH" 2>/dev/null; then
        echo "⚠️  $REPO: pre-push 不是 NDF 的, 不动"
        continue
      fi

      rm "$HOOK_PATH"
      echo "✓ $REPO: NDF hook 已移除"

      # 找最新的 .bak 恢复
      LATEST_BAK=$(ls -t "$REPO_PATH/.git/hooks/pre-push.bak."* 2>/dev/null | head -1)
      if [[ -n "$LATEST_BAK" ]]; then
        mv "$LATEST_BAK" "$HOOK_PATH"
        chmod +x "$HOOK_PATH"
        echo "  ↳ 已恢复备份: $LATEST_BAK"
      fi
      UNINSTALLED=$((UNINSTALLED + 1))
    done
    echo
    echo "─── 完成: $UNINSTALLED 个仓库已卸载 ───"
    ;;
esac

#!/usr/bin/env bash
# ndf-done (v3): 完成一个 NDF feature 的本地工作
#
# v3 改动：从 cwd 向上找 .ndf-active 文件，不再读中央 state.json。
# 必须在 worktree 内运行，否则报错。
#
# Usage:
#   ndf-done [--message "commit message"] [--no-push] [--keep-worktree]
#
# 原子化操作（任一步失败立刻停下，不留 orphan）：
#   1. 从 cwd 向上找 .ndf-active 文件 → 读 feature 元数据
#   2. 校验每个 worktree 干净（无未 commit 改动；若提供 --message 则自动 commit）
#   3. 每个 repo: 在主 checkout cd → checkout develop → pull → merge --no-ff → push
#                 → git worktree remove <wt_path>
#                 → git branch -D <branch>
#
# 注意：
#   - 这只做"本地完成"——merge develop + push + 清理。
#   - 不动 manifest.yaml 的 stage 字段（让 AI 决定 stage 是 S6/H3/completed）
#   - 不做 dev/prod 验证（那是后续步骤）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Resolve repo root via git --git-common-dir so this works from worktrees too.
# (When the script is invoked from a copy in a worktree, the naive ../.. would
# give us the worktree path, not the main checkout.)
if _gc=$(git -C "$SCRIPT_DIR" rev-parse --git-common-dir 2>/dev/null); then
  if [[ "$_gc" = /* ]]; then
    NDF_REPO_ROOT="$(cd "$_gc/.." && pwd)"
  else
    NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/$_gc/.." && pwd)"
  fi
else
  NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
fi
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"

MESSAGE=""
DO_PUSH=1
KEEP_WORKTREE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --message|-m)
      MESSAGE="$2"
      shift 2
      ;;
    --no-push)
      DO_PUSH=0
      shift
      ;;
    --keep-worktree)
      # 罕见：用于半自动恢复场景，merge + push 后保留 worktree（用户后手清理）
      KEEP_WORKTREE=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: ndf-done [--message "commit message"] [--no-push] [--keep-worktree]

Options:
  --message <msg>     作为 final commit 的 message（如果 worktree 有未 commit 改动）
  --no-push           跳过 push origin/develop（测试/离线用）
  --keep-worktree     完成 merge 后保留 worktree（罕见，调试用）

Behavior (v3):
  从 cwd 向上找 .ndf-active → 解析 feature → merge develop → push → 清理
EOF
      exit 0
      ;;
    *)
      echo "Error: unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

# 1. 从 cwd 向上找 .ndf-active
find_ndf_active() {
  local d="$1"
  while [[ "$d" != "/" && -n "$d" ]]; do
    if [[ -f "$d/.ndf-active" ]]; then
      printf '%s\n' "$d/.ndf-active"
      return 0
    fi
    d=$(dirname "$d")
  done
  return 1
}

NDF_ACTIVE_FILE=""
if NDF_ACTIVE_FILE=$(find_ndf_active "$(pwd)"); then
  :
else
  echo "Error: 当前 cwd 不在 NDF v3 worktree 内（向上未找到 .ndf-active 文件）" >&2
  echo "  → cwd: $(pwd)" >&2
  echo "  → 必须在 ndf-start 创建的 worktree 内（或其子目录）跑 ndf-done。" >&2
  exit 1
fi

# 解析 active — 读到内存里（重要：worktree 删除后文件没了，必须 cache）
NDF_ACTIVE_JSON=$(cat "$NDF_ACTIVE_FILE")
if ! ACTIVE_ID=$(jq -r '.id // empty' <<<"$NDF_ACTIVE_JSON") || [[ -z "$ACTIVE_ID" ]]; then
  echo "Error: $NDF_ACTIVE_FILE 缺少 id 字段（或不是 JSON）" >&2
  exit 1
fi
TRACK=$(jq -r '.track' <<<"$NDF_ACTIVE_JSON")
STAGE=$(jq -r '.stage' <<<"$NDF_ACTIVE_JSON")
REPOS=()
while IFS= read -r line; do
  REPOS+=("$line")
done < <(jq -r '.repos[]' <<<"$NDF_ACTIVE_JSON")

echo "─── ndf-done v3: $ACTIVE_ID ($TRACK / stage=$STAGE) ───"
echo "Repos: ${REPOS[*]}"

# 2. 判断 cwd 是否在即将被删的 worktree 内 → 提前 cd 到主 repo
CURRENT_CWD="$(pwd)"
CWD_IN_WT=0
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".worktrees[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")
  if [[ "$CURRENT_CWD" == "$WT_PATH"* ]]; then
    CWD_IN_WT=1
    cd "$NDF_CODES_ROOT/$REPO"
    break
  fi
done

# 3. 每个 repo: 校验 worktree 干净 + 处理未 commit 改动
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".worktrees[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")
  BRANCH=$(jq -r ".branches[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")

  if [[ ! -d "$WT_PATH" ]]; then
    echo "Error: worktree not found at $WT_PATH (repo $REPO)" >&2
    echo "  → .ndf-active out of sync（worktree 可能已被外部删除）。" >&2
    exit 1
  fi

  # 检查未 commit 改动（忽略 untracked .ndf-active）
  PORCELAIN=$(git -C "$WT_PATH" status --porcelain --untracked-files=no)
  if [[ -n "$PORCELAIN" ]]; then
    if [[ -z "$MESSAGE" ]]; then
      echo "Error: $REPO worktree has uncommitted changes:" >&2
      git -C "$WT_PATH" status --short >&2
      echo "  → 提供 --message 让 ndf-done 自动 commit，或先手动 commit" >&2
      exit 1
    fi
    echo "→ Auto-committing pending changes in $REPO with message: $MESSAGE"
    git -C "$WT_PATH" add -u
    git -C "$WT_PATH" commit -m "$MESSAGE"
  fi

  # 校验当前 worktree 在正确 branch
  ACTUAL_BRANCH=$(git -C "$WT_PATH" rev-parse --abbrev-ref HEAD)
  if [[ "$ACTUAL_BRANCH" != "$BRANCH" ]]; then
    echo "Error: $REPO worktree on branch $ACTUAL_BRANCH but .ndf-active expects $BRANCH" >&2
    exit 1
  fi
done

# 3.5. 如果是 Micro 档，校验改动没越界
if [[ "$TRACK" == "micro" ]]; then
  echo "─── Micro 边界检查 ───"
  VIOLATIONS=""
  for REPO in "${REPOS[@]}"; do
    WT_PATH=$(jq -r ".worktrees[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")
    DIFF_FILES=$(git -C "$WT_PATH" diff --name-only develop...HEAD 2>/dev/null || git -C "$WT_PATH" diff --name-only develop 2>/dev/null || echo "")
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      case "$f" in
        */migrations/*|migrations/*)
          VIOLATIONS+="  ✗ $REPO/$f (DB migration — Micro 禁止动 schema)\n" ;;
        */biz/*.go|biz/*.go)
          VIOLATIONS+="  ✗ $REPO/$f (biz 业务逻辑 — Micro 禁止)\n" ;;
        */store/*.go|store/*.go)
          VIOLATIONS+="  ✗ $REPO/$f (store 数据层 — Micro 禁止)\n" ;;
        */router.go|*/admin_router.go|router.go|admin_router.go)
          VIOLATIONS+="  ✗ $REPO/$f (新增 API 端点 — Micro 禁止)\n" ;;
        */src/api/*.ts|src/api/*.ts)
          VIOLATIONS+="  ✗ $REPO/$f (前端 API 调用层 — Micro 禁止)\n" ;;
      esac
    done <<< "$DIFF_FILES"
  done
  if [[ -n "$VIOLATIONS" ]]; then
    echo "❌ Micro 边界违反——以下改动超出 Micro 范围：" >&2
    printf "%b" "$VIOLATIONS" >&2
    echo "" >&2
    echo "→ 应升 Hotfix。改 .ndf-active 内 track=hotfix + branches.* 重命名为 fix/..." >&2
    exit 3
  fi
  echo "✓ Micro 边界 OK"
fi

# 4. 每个 repo: merge to develop + push + 清理
ANY_FAIL=0
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".worktrees[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")
  BRANCH=$(jq -r ".branches[\"$REPO\"]" <<<"$NDF_ACTIVE_JSON")
  REPO_PATH="$NDF_CODES_ROOT/$REPO"

  echo "─── 处理 $REPO ($BRANCH) ───"

  # 切到 develop（在主 checkout）
  if ! git -C "$REPO_PATH" checkout develop 2>&1; then
    echo "Error: $REPO 切 develop 失败（主 checkout 可能 detached HEAD 或 dirty）" >&2
    ANY_FAIL=1
    break
  fi

  # 先 pull 最新（避免 push 时拒绝）— 离线场景允许失败
  if [[ $DO_PUSH -eq 1 ]]; then
    if ! git -C "$REPO_PATH" pull --ff-only origin develop 2>&1; then
      echo "Error: $REPO pull develop 失败（可能有冲突或网络问题）" >&2
      ANY_FAIL=1
      break
    fi
  fi

  # merge --no-ff
  if ! git -C "$REPO_PATH" merge --no-ff "$BRANCH" -m "Merge branch '$BRANCH' into develop"; then
    echo "Error: $REPO merge $BRANCH 到 develop 失败（冲突？）" >&2
    echo "  → 手动解决冲突 + commit + push 后用 --keep-worktree 重跑 ndf-done 完成清理" >&2
    ANY_FAIL=1
    break
  fi

  # push develop
  if [[ $DO_PUSH -eq 1 ]]; then
    if ! git -C "$REPO_PATH" push origin develop 2>&1; then
      echo "Error: $REPO push develop 失败" >&2
      echo "  → 解决 push 问题后重跑 ndf-done" >&2
      ANY_FAIL=1
      break
    fi
  fi

  echo "✓ $REPO: $BRANCH merged → develop pushed"

  # 删除 worktree
  if [[ $KEEP_WORKTREE -eq 0 ]]; then
    if ! git -C "$REPO_PATH" worktree remove "$WT_PATH" 2>&1; then
      echo "⚠️  worktree remove 失败，尝试 --force"
      git -C "$REPO_PATH" worktree remove --force "$WT_PATH" 2>&1 || {
        echo "  → 手动删除 $WT_PATH 后跑 ndf-done --keep-worktree" >&2
        ANY_FAIL=1
        break
      }
    fi
    echo "✓ $REPO: worktree removed ($WT_PATH)"

    if ! git -C "$REPO_PATH" branch -D "$BRANCH" 2>&1; then
      echo "⚠️  本地分支 $BRANCH 删除失败（可能已不存在）"
    else
      echo "✓ $REPO: local branch $BRANCH deleted"
    fi
  fi
done

if [[ $ANY_FAIL -eq 1 ]]; then
  echo
  echo "❌ ndf-done 中途失败。worktree/branch 保持原样，便于排查重试。" >&2
  exit 2
fi

# 5. 提示 cwd 失效
if [[ $CWD_IN_WT -eq 1 && $KEEP_WORKTREE -eq 0 ]]; then
  echo
  echo "ℹ️  你之前在 worktree 里跑了 ndf-done。worktree 已删，shell cwd 失效。"
  echo "    建议运行: cd $NDF_CODES_ROOT/${REPOS[0]}"
fi

PUSH_NOTE=""
[[ $DO_PUSH -eq 0 ]] && PUSH_NOTE=" (跳过 - --no-push)"
WT_NOTE_1="│   ✓ Worktree 已删除"
WT_NOTE_2="│   ✓ 本地分支已删除"
if [[ $KEEP_WORKTREE -eq 1 ]]; then
  WT_NOTE_1="│   ↻ Worktree 保留 (--keep-worktree)"
  WT_NOTE_2="│   ↻ 本地分支保留 (--keep-worktree)"
fi

cat <<EOF

╭─────────────────────────────────────────────────────────────
│ ✓ ndf-done 完成
│
│   Feature:  $ACTIVE_ID ($TRACK)
│   Repos:    ${REPOS[*]}
│
│   ✓ 已 merge 到 develop
│   ✓ 已 push origin/develop$PUSH_NOTE
$WT_NOTE_1
$WT_NOTE_2
│
│   下一步：
│     micro:    完成
│     hotfix:   等 dev 部署 → 你点几下确认 → 我帮你 tag prod
│     standard: 等 dev 部署 → 验收 → tag prod → 写 Obsidian 笔记
╰─────────────────────────────────────────────────────────────
EOF

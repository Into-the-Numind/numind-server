#!/usr/bin/env bash
# ndf-done: 完成一个 NDF feature 的本地工作
#
# Usage:
#   ndf-done [--message "commit message"] [--keep-state]
#
# 原子化操作（任一步失败立刻停下，不留 orphan）：
#   1. 校验每个 worktree 干净（无未 commit 改动）
#   2. 每个 repo: 在 worktree 里 commit any pending changes
#                 → cd 到主 repo path
#                 → git merge --no-ff <branch> develop
#                 → git push origin develop
#                 → git worktree remove <wt_path>
#                 → git branch -D <branch>
#   3. 清空 state.json active 字段
#
# 注意：
#   - 这只做"本地完成"——merge develop + push + 清理。
#   - 不动 manifest.yaml 的 stage 字段（让 AI 决定 stage 是 S6/H3/completed）
#   - 不做 dev/prod 验证（那是后续步骤）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"
NDF_STATE_FILE="$NDF_REPO_ROOT/.ndf/state.json"

MESSAGE=""
KEEP_STATE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --message|-m)
      MESSAGE="$2"
      shift 2
      ;;
    --keep-state)
      KEEP_STATE=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: ndf-done [--message "commit message"] [--keep-state]

Options:
  --message <msg>   作为 final commit 的 message（如果 worktree 有未 commit 改动）
  --keep-state      完成清理但保留 state.json active 字段（罕见，仅在并行 session 场景用）

Behavior:
  原子化完成：worktree 干净 → merge 到 develop → push → 删 worktree → 删本地分支 → 清 state
EOF
      exit 0
      ;;
    *)
      echo "Error: unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

# 1. 读 state.json
if [[ ! -f "$NDF_STATE_FILE" ]]; then
  echo "Error: state file not found: $NDF_STATE_FILE" >&2
  echo "→ Did you forget to run ndf-start?" >&2
  exit 1
fi

ACTIVE_ID=$(jq -r '.active_feature // empty' "$NDF_STATE_FILE")
if [[ -z "$ACTIVE_ID" ]]; then
  echo "Error: no active feature in state.json" >&2
  echo "→ Nothing to finish" >&2
  exit 1
fi

TRACK=$(jq -r '.active.track' "$NDF_STATE_FILE")
STAGE=$(jq -r '.active.stage' "$NDF_STATE_FILE")
REPOS=($(jq -r '.active.repos[]' "$NDF_STATE_FILE"))

echo "─── ndf-done: $ACTIVE_ID ($TRACK / stage=$STAGE) ───"
echo "Repos: ${REPOS[*]}"

# 2. 警告：当前 cwd 是否在即将被删的 worktree 里
CURRENT_CWD="$(pwd)"
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$NDF_STATE_FILE")
  if [[ "$CURRENT_CWD" == "$WT_PATH"* ]]; then
    echo "⚠️  你当前在 worktree 里 ($CURRENT_CWD)。" >&2
    echo "   worktree 删除后你的 shell cwd 会失效。" >&2
    echo "   建议：cd 到 $NDF_CODES_ROOT/$REPO 后重跑 ndf-done" >&2
    exit 1
  fi
done

# 3. 每个 repo: 校验 worktree 干净 + 处理未 commit 改动
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$NDF_STATE_FILE")
  BRANCH=$(jq -r ".active.branches[\"$REPO\"]" "$NDF_STATE_FILE")

  if [[ ! -d "$WT_PATH" ]]; then
    echo "Error: worktree not found at $WT_PATH (repo $REPO)" >&2
    echo "  → state.json out of sync. Manual cleanup needed." >&2
    exit 1
  fi

  cd "$WT_PATH"

  # 检查未 commit 改动
  if [[ -n "$(git status --porcelain)" ]]; then
    if [[ -z "$MESSAGE" ]]; then
      echo "Error: $REPO worktree has uncommitted changes:" >&2
      git status --short >&2
      echo "  → 提供 --message 让 ndf-done 自动 commit，或先手动 commit" >&2
      exit 1
    fi
    echo "→ Auto-committing pending changes in $REPO with message: $MESSAGE"
    git add -A
    git commit -m "$MESSAGE"
  fi

  # 校验当前在正确 branch
  ACTUAL_BRANCH=$(git rev-parse --abbrev-ref HEAD)
  if [[ "$ACTUAL_BRANCH" != "$BRANCH" ]]; then
    echo "Error: $REPO worktree on branch $ACTUAL_BRANCH but state expects $BRANCH" >&2
    exit 1
  fi
done

cd "$NDF_CODES_ROOT"

# 3.5. 如果是 Micro 档，校验改动没越界
if [[ "$TRACK" == "micro" ]]; then
  echo "─── Micro 边界检查 ───"
  VIOLATIONS=""
  for REPO in "${REPOS[@]}"; do
    WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$NDF_STATE_FILE")
    cd "$WT_PATH"
    # 跟 develop 的 diff 找改动文件
    DIFF_FILES=$(git diff --name-only develop...HEAD 2>/dev/null || git diff --name-only develop 2>/dev/null || echo "")
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      case "$f" in
        */migrations/*)
          VIOLATIONS+="  ✗ $REPO/$f (DB migration — Micro 禁止动 schema)\n" ;;
        */biz/*.go|biz/*.go)
          VIOLATIONS+="  ✗ $REPO/$f (biz 业务逻辑 — Micro 禁止)\n" ;;
        */store/*.go|store/*.go)
          VIOLATIONS+="  ✗ $REPO/$f (store 数据层 — Micro 禁止)\n" ;;
        */router.go|*/admin_router.go)
          VIOLATIONS+="  ✗ $REPO/$f (新增 API 端点 — Micro 禁止)\n" ;;
        */src/api/*.ts)
          VIOLATIONS+="  ✗ $REPO/$f (前端 API 调用层 — Micro 禁止)\n" ;;
      esac
    done <<< "$DIFF_FILES"
  done
  if [[ -n "$VIOLATIONS" ]]; then
    echo "❌ Micro 边界违反——以下改动超出 Micro 范围：" >&2
    printf "$VIOLATIONS" >&2
    echo "" >&2
    echo "→ 应升 Hotfix。修法：" >&2
    BRANCH=$(jq -r ".active.branches | to_entries | .[0].value" "$NDF_STATE_FILE")
    FIRST_REPO=$(jq -r ".active.repos[0]" "$NDF_STATE_FILE")
    NEW_BRANCH="${BRANCH/micro\//fix\/}"
    echo "  1. cd $NDF_CODES_ROOT/$FIRST_REPO" >&2
    echo "  2. 每个 repo: git -C <worktree> branch -m $BRANCH $NEW_BRANCH" >&2
    echo "  3. 改 state.json: track=hotfix, branches.*=$NEW_BRANCH" >&2
    echo "  4. 重跑 ndf-done" >&2
    exit 3
  fi
  echo "✓ Micro 边界 OK"
  cd "$NDF_CODES_ROOT"
fi

# 4. 每个 repo: merge to develop + push + 清理
ANY_FAIL=0
for REPO in "${REPOS[@]}"; do
  WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$NDF_STATE_FILE")
  BRANCH=$(jq -r ".active.branches[\"$REPO\"]" "$NDF_STATE_FILE")
  REPO_PATH="$NDF_CODES_ROOT/$REPO"

  echo "─── 处理 $REPO ($BRANCH) ───"

  cd "$REPO_PATH"

  # 切到 develop
  if ! git checkout develop 2>&1; then
    echo "Error: $REPO 切 develop 失败" >&2
    ANY_FAIL=1
    break
  fi

  # 先 pull 最新（避免 push 时拒绝）
  if ! git pull --ff-only origin develop 2>&1; then
    echo "Error: $REPO pull develop 失败（可能有冲突或网络问题）" >&2
    ANY_FAIL=1
    break
  fi

  # merge --no-ff (保留 merge commit, 跟现有 /commit-merge-push 风格一致)
  if ! git merge --no-ff "$BRANCH" -m "Merge branch '$BRANCH' into develop"; then
    echo "Error: $REPO merge $BRANCH 到 develop 失败（冲突？）" >&2
    echo "  → 手动解决冲突后 git push, 然后用 --keep-state 重跑 ndf-done 完成清理" >&2
    ANY_FAIL=1
    break
  fi

  # push develop
  if ! git push origin develop 2>&1; then
    echo "Error: $REPO push develop 失败" >&2
    echo "  → 解决 push 问题后重跑 ndf-done" >&2
    ANY_FAIL=1
    break
  fi

  echo "✓ $REPO: $BRANCH merged → develop pushed"

  # 删除 worktree
  if ! git worktree remove "$WT_PATH" 2>&1; then
    # 如果失败用 force
    echo "⚠️  worktree remove 失败，尝试 --force"
    git worktree remove --force "$WT_PATH" 2>&1 || {
      echo "  → 手动删除 $WT_PATH 后跑 ndf-done --keep-state" >&2
      ANY_FAIL=1
      break
    }
  fi
  echo "✓ $REPO: worktree removed ($WT_PATH)"

  # 删除本地分支
  if ! git branch -D "$BRANCH" 2>&1; then
    echo "⚠️  本地分支 $BRANCH 删除失败（可能已不存在）"
  else
    echo "✓ $REPO: local branch $BRANCH deleted"
  fi
done

if [[ $ANY_FAIL -eq 1 ]]; then
  echo
  echo "❌ ndf-done 中途失败。state.json 保持原样，便于排查重试。" >&2
  exit 2
fi

# 5. 清 state.json active 字段
if [[ $KEEP_STATE -eq 0 ]]; then
  jq '.active_feature = null | .active = null' "$NDF_STATE_FILE" > "$NDF_STATE_FILE.tmp"
  mv "$NDF_STATE_FILE.tmp" "$NDF_STATE_FILE"
  echo "✓ state.json active 字段已清空"
fi

cat <<EOF

╭─────────────────────────────────────────────────────────────
│ ✓ ndf-done 完成
│
│   Feature:  $ACTIVE_ID ($TRACK)
│   Repos:    ${REPOS[*]}
│
│   ✓ 已 merge 到 develop
│   ✓ 已 push origin/develop
│   ✓ Worktree 已删除
│   ✓ 本地分支已删除
│   ✓ state.json 已清空
│
│   下一步：
│     micro:    完成
│     hotfix:   等 dev 部署 → 你点几下确认 → 我帮你 tag prod
│     standard: 等 dev 部署 → 验收 → tag prod → 写 Obsidian 笔记
╰─────────────────────────────────────────────────────────────
EOF

#!/usr/bin/env bash
# ndf-start (v3): 启动一个新的 NDF feature
#
# v3 架构（per-worktree state）：每个 worktree 自带 .ndf-active 文件，无中央 state.json。
# 多个 feature 可以同时活跃，互不干扰。
#
# Usage:
#   ndf-start <track> <name> [--repos repo1,repo2,...]
#
# Tracks:
#   micro     快速改动 (≤15 min, 不动 DB/API/biz)
#   hotfix    小 bug 修复或小功能 (H1→H2→H3)
#   standard  完整 feature (S0→S7)
#
# Behavior:
#   - 自动建 worktree 到 /private/tmp/wt-{slug}-{repo}/
#   - 自动建 branch micro/{slug} (or fix/{slug}, feature/{slug})
#   - 写 .ndf-active 文件到每个 worktree 根目录
#   - 如果目标 worktree 路径已存在 → 报错退出（绝不擦未提交改动）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"

usage() {
  cat <<EOF
Usage: ndf-start <track> <name> [--repos repo1,repo2,...]

Tracks:
  micro     快速改动 (≤15 min, 不动 DB/API/biz)
  hotfix    小 bug 修复或小功能 (3 stages)
  standard  完整 feature (8 stages)

Examples:
  ndf-start micro update-claude-date
  ndf-start hotfix login-icp-footer --repos numind-web-v3
  ndf-start standard child-membership --repos numind-server,numind-web-v3

v3 改动：
  - 状态分散到每个 worktree 的 .ndf-active 文件
  - 多个 feature 可以同时活跃（不再有"互斥 active slot"）
  - 目标 worktree 路径已存在 → 报错退出（不再 --force 擦除）
EOF
  exit 1
}

[[ $# -lt 2 ]] && usage

TRACK="$1"
NAME="$2"
shift 2

REPOS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repos)
      REPOS="$2"
      shift 2
      ;;
    --force)
      echo "Error: --force was removed in NDF v3." >&2
      echo "  → v3 worktree 已存在意味着真有未完工的 work，绝不 silent 擦除。" >&2
      echo "  → 如果你确实要丢弃，手动跑: git worktree remove --force <path> && git branch -D <branch>" >&2
      exit 1
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Error: unknown arg: $1" >&2
      usage
      ;;
  esac
done

# Track validation
case "$TRACK" in
  micro|hotfix|standard) ;;
  *)
    echo "Error: track must be micro/hotfix/standard, got: $TRACK" >&2
    exit 1
    ;;
esac

# Slug normalize
SLUG=$(echo "$NAME" | tr ' ' '-' | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]//g')
if [[ -z "$SLUG" ]]; then
  echo "Error: name produces empty slug after normalization: $NAME" >&2
  exit 1
fi

# Branch prefix by track
case "$TRACK" in
  micro)    BRANCH_PREFIX="micro" ;;
  hotfix)   BRANCH_PREFIX="fix" ;;
  standard) BRANCH_PREFIX="feature" ;;
esac
BRANCH="$BRANCH_PREFIX/$SLUG"

# Repos default: detect from cwd
if [[ -z "$REPOS" ]]; then
  CWD="$(pwd)"
  for r in numind-server numind-web-v3 numind-admin-web; do
    if [[ "$CWD" == "$NDF_CODES_ROOT/$r"* ]]; then
      REPOS="$r"
      break
    fi
  done
  if [[ -z "$REPOS" ]]; then
    echo "Error: --repos not given and cwd is not in a recognized repo" >&2
    echo "Specify --repos with one or more of: numind-server, numind-web-v3, numind-admin-web" >&2
    exit 1
  fi
fi

# Initial stage / review policy
case "$TRACK" in
  micro)    INITIAL_STAGE="M1" ;;
  hotfix)   INITIAL_STAGE="H1" ;;
  standard) INITIAL_STAGE="S0" ;;
esac

case "$TRACK" in
  micro)    REVIEW_POLICY="none" ;;
  hotfix)   REVIEW_POLICY="single" ;;
  standard) REVIEW_POLICY="dual-parallel" ;;
esac

NOW=$(date +"%Y-%m-%dT%H:%M:%S%z")

# Pre-check: ensure no target worktree path exists. Fail fast — never auto-clean.
REPO_LIST=$(echo "$REPOS" | tr ',' ' ')
for REPO in $REPO_LIST; do
  REPO_PATH="$NDF_CODES_ROOT/$REPO"
  if [[ ! -d "$REPO_PATH/.git" ]]; then
    echo "Error: repo not found or not a git repo: $REPO_PATH" >&2
    exit 1
  fi
  WT_PATH="/private/tmp/wt-$SLUG-$REPO"
  if [[ -e "$WT_PATH" ]]; then
    echo "❌ Error: worktree path already exists: $WT_PATH" >&2
    echo "  → 这通常意味着有人在那里工作过（可能未 commit！）。" >&2
    echo "  → 排查：" >&2
    echo "      1. cd $WT_PATH && git status         # 看是否有未提交改动" >&2
    echo "      2. 如果干净 → git worktree remove $WT_PATH && git branch -D $BRANCH 后重试" >&2
    echo "      3. 如果有未提交改动 → 先 commit 或 stash，再清理" >&2
    echo "  → NDF v3 拒绝任何 silent 擦除（这就是 v2 老 --force 干的事故根因）。" >&2
    exit 1
  fi
done

# Build worktrees JSON parts (will be combined for each repo's .ndf-active later)
WORKTREES_JSON="{"
BRANCHES_JSON="{"
FIRST=1
FIRST_WT=""
for REPO in $REPO_LIST; do
  WT_PATH="/private/tmp/wt-$SLUG-$REPO"
  if [[ $FIRST -eq 0 ]]; then
    WORKTREES_JSON+=","
    BRANCHES_JSON+=","
  fi
  WORKTREES_JSON+="\"$REPO\":\"$WT_PATH\""
  BRANCHES_JSON+="\"$REPO\":\"$BRANCH\""
  [[ -z "$FIRST_WT" ]] && FIRST_WT="$WT_PATH"
  FIRST=0
done
WORKTREES_JSON+="}"
BRANCHES_JSON+="}"

# REPOS JSON array
REPOS_JSON_ARR="["
FIRST=1
for REPO in $REPO_LIST; do
  if [[ $FIRST -eq 0 ]]; then REPOS_JSON_ARR+=","; fi
  REPOS_JSON_ARR+="\"$REPO\""
  FIRST=0
done
REPOS_JSON_ARR+="]"

# Build the .ndf-active payload (same for every worktree of this feature)
NDF_ACTIVE_JSON=$(jq -n \
  --arg id "$SLUG" \
  --arg track "$TRACK" \
  --arg stage "$INITIAL_STAGE" \
  --arg created_at "$NOW" \
  --arg review_policy "$REVIEW_POLICY" \
  --argjson worktrees "$WORKTREES_JSON" \
  --argjson branches "$BRANCHES_JSON" \
  --argjson repos "$REPOS_JSON_ARR" \
  '{
    version: "ndf-v3",
    id: $id,
    track: $track,
    stage: $stage,
    created_at: $created_at,
    repos: $repos,
    worktrees: $worktrees,
    branches: $branches,
    review_policy: $review_policy,
    blockers: []
  }')

# Now create the worktree for each repo
for REPO in $REPO_LIST; do
  REPO_PATH="$NDF_CODES_ROOT/$REPO"
  WT_PATH="/private/tmp/wt-$SLUG-$REPO"

  # Create branch + worktree (reuse branch if exists)
  if git -C "$REPO_PATH" rev-parse --verify "refs/heads/$BRANCH" >/dev/null 2>&1; then
    echo "ℹ️  Branch $BRANCH already exists in $REPO, reusing"
    git -C "$REPO_PATH" worktree add "$WT_PATH" "$BRANCH"
  else
    # Try to base off origin/develop (after fetch) so we're not behind
    git -C "$REPO_PATH" fetch origin develop >/dev/null 2>&1 || true
    if git -C "$REPO_PATH" rev-parse --verify "refs/heads/develop" >/dev/null 2>&1; then
      BASE_REF="develop"
    else
      BASE_REF="origin/develop"
    fi
    git -C "$REPO_PATH" worktree add "$WT_PATH" -b "$BRANCH" "$BASE_REF"
  fi

  # Write .ndf-active in this worktree's root
  printf '%s\n' "$NDF_ACTIVE_JSON" > "$WT_PATH/.ndf-active"

  # Add .ndf-active to this worktree's git exclude (per-worktree info/exclude)
  # The repo's shared .git/info/exclude also gets it so status remains clean even if
  # .gitignore wasn't updated yet (defensive — for repos that haven't received the .gitignore change).
  GIT_FILE="$WT_PATH/.git"
  if [[ -f "$GIT_FILE" ]]; then
    WT_GITDIR=$(sed -n 's/^gitdir: //p' "$GIT_FILE")
    if [[ -n "$WT_GITDIR" ]]; then
      mkdir -p "$WT_GITDIR/info"
      if [[ ! -f "$WT_GITDIR/info/exclude" ]] || ! grep -qxF '.ndf-active' "$WT_GITDIR/info/exclude" 2>/dev/null; then
        echo '.ndf-active' >> "$WT_GITDIR/info/exclude"
      fi
    fi
  fi
  # Shared exclude (defensive — works for any worktree of this repo)
  if [[ -d "$REPO_PATH/.git/info" ]]; then
    if [[ ! -f "$REPO_PATH/.git/info/exclude" ]] || ! grep -qxF '.ndf-active' "$REPO_PATH/.git/info/exclude" 2>/dev/null; then
      echo '.ndf-active' >> "$REPO_PATH/.git/info/exclude"
    fi
  fi

  echo "✓ Worktree created: $WT_PATH (branch $BRANCH in $REPO)"
done

# Summary
cat <<EOF

╭─────────────────────────────────────────────────────────────
│ ✓ NDF v3 feature 启动成功
│
│   Track:     $TRACK
│   Slug:      $SLUG
│   Stage:     $INITIAL_STAGE
│   Branch:    $BRANCH
│   Repos:     $REPOS
│   Worktree:  $FIRST_WT
│
│   .ndf-active 已写入每个 worktree 根目录。
│   多个并发 feature 互不干扰（v3 改动）。
│
│   下一步：cd "$FIRST_WT"
│   完成后：ndf-done（在 worktree 内运行）
╰─────────────────────────────────────────────────────────────
EOF

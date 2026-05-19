#!/usr/bin/env bash
# ndf-start: 启动一个新的 NDF feature
#
# Usage:
#   ndf-start <track> <name> [--repos repo1,repo2,...] [--force]
#
# Tracks:
#   micro     快速改动 (≤15 min, 不动 DB/API/biz)
#   hotfix    小 bug 修复或小功能 (H1→H2→H3)
#   standard  完整 feature (S0→S7)
#
# Behavior:
#   - 自动建 worktree 到 /private/tmp/wt-{slug}-{repo}/
#   - 自动建 branch micro/{slug} (or fix/{slug}, feature/{slug})
#   - 更新 .ndf/state.json active 字段
#   - 不允许同时有 2 个 active feature（用 --force 覆盖）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"
NDF_STATE_FILE="$NDF_REPO_ROOT/.ndf/state.json"

usage() {
  cat <<EOF
Usage: ndf-start <track> <name> [--repos repo1,repo2,...] [--force]

Tracks:
  micro     快速改动 (≤15 min, 不动 DB/API/biz)
  hotfix    小 bug 修复或小功能 (3 stages)
  standard  完整 feature (8 stages)

Examples:
  ndf-start micro update-claude-date
  ndf-start hotfix login-icp-footer --repos numind-web-v3
  ndf-start standard child-membership --repos numind-server,numind-web-v3

State file: $NDF_STATE_FILE
EOF
  exit 1
}

[[ $# -lt 2 ]] && usage

TRACK="$1"
NAME="$2"
shift 2

REPOS=""
FORCE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repos)
      REPOS="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
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

# Init state file if missing
if [[ ! -f "$NDF_STATE_FILE" ]]; then
  mkdir -p "$(dirname "$NDF_STATE_FILE")"
  echo '{"version":"ndf-v2","active_feature":null,"active":null}' > "$NDF_STATE_FILE"
fi

# Check existing active feature
EXISTING_ACTIVE=$(jq -r '.active_feature // empty' "$NDF_STATE_FILE")
if [[ -n "$EXISTING_ACTIVE" && $FORCE -eq 0 ]]; then
  echo "Error: existing active feature: $EXISTING_ACTIVE" >&2
  echo "  Either: ndf-done it first" >&2
  echo "  Or:     ndf-start ... --force (replaces active without cleanup — only do this for parallel sessions)" >&2
  exit 1
fi

# Create worktree in each repo
WORKTREES_JSON="{"
BRANCHES_JSON="{"
FIRST=1
FIRST_WT=""
for REPO in $(echo "$REPOS" | tr ',' ' '); do
  REPO_PATH="$NDF_CODES_ROOT/$REPO"
  if [[ ! -d "$REPO_PATH/.git" ]]; then
    echo "Error: repo not found or not a git repo: $REPO_PATH" >&2
    exit 1
  fi
  WT_PATH="/private/tmp/wt-$SLUG-$REPO"

  # If worktree path already exists, try clean removal
  if [[ -e "$WT_PATH" ]]; then
    echo "⚠️  Worktree path exists: $WT_PATH — removing"
    git -C "$REPO_PATH" worktree remove --force "$WT_PATH" 2>/dev/null || rm -rf "$WT_PATH"
  fi

  # Create branch + worktree (reuse branch if exists)
  if git -C "$REPO_PATH" rev-parse --verify "refs/heads/$BRANCH" >/dev/null 2>&1; then
    echo "ℹ️  Branch $BRANCH already exists in $REPO, reusing"
    git -C "$REPO_PATH" worktree add "$WT_PATH" "$BRANCH"
  else
    git -C "$REPO_PATH" worktree add "$WT_PATH" -b "$BRANCH" develop
  fi

  echo "✓ Worktree created: $WT_PATH (branch $BRANCH in $REPO)"

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

# Initial stage
case "$TRACK" in
  micro)    INITIAL_STAGE="M1" ;;
  hotfix)   INITIAL_STAGE="H1" ;;
  standard) INITIAL_STAGE="S0" ;;
esac

# Review policy
case "$TRACK" in
  micro)    REVIEW_POLICY="none" ;;
  hotfix)   REVIEW_POLICY="single" ;;
  standard) REVIEW_POLICY="dual-parallel" ;;
esac

# Update state.json
NOW=$(date +"%Y-%m-%dT%H:%M:%S%z")
NEW_STATE=$(jq -n \
  --arg id "$SLUG" \
  --arg track "$TRACK" \
  --arg stage "$INITIAL_STAGE" \
  --arg created_at "$NOW" \
  --arg review_policy "$REVIEW_POLICY" \
  --argjson worktrees "$WORKTREES_JSON" \
  --argjson branches "$BRANCHES_JSON" \
  '{
    version: "ndf-v2",
    active_feature: $id,
    active: {
      id: $id,
      track: $track,
      stage: $stage,
      created_at: $created_at,
      repos: ($worktrees | keys),
      worktrees: $worktrees,
      branches: $branches,
      review_policy: $review_policy,
      blockers: []
    }
  }')
echo "$NEW_STATE" > "$NDF_STATE_FILE"

# Summary
cat <<EOF

╭─────────────────────────────────────────────────────────────
│ ✓ NDF feature 启动成功
│
│   Track:     $TRACK
│   Slug:      $SLUG
│   Stage:     $INITIAL_STAGE
│   Branch:    $BRANCH
│   Repos:     $REPOS
│   Worktree:  $FIRST_WT
│
│   下一步：cd "$FIRST_WT"
│   完成后：ndf-done
╰─────────────────────────────────────────────────────────────
EOF

#!/usr/bin/env bash
# ndf-migrate-v3.sh: 将 NDF v2 中央 state.json 迁移到 v3 per-worktree .ndf-active
#
# Usage:
#   ndf-migrate-v3.sh [--dry-run] [--state-file PATH]
#
# 行为：
#   1. 读 numind-server/.ndf/state.json（或 --state-file 指定）
#   2. 如果 .active 存在 → 为 .active.worktrees 里每个 repo 写一个 .ndf-active
#   3. 备份 state.json → state.json.v2-archive-YYYYMMDD.json
#   4. 删 state.json
#   5. 打印 summary
#
# 安全保证：
#   - 不动 worktree 内的代码文件
#   - 如果某个 worktree 路径不存在 → 跳过 + 记录在 summary
#   - 如果某个 worktree 已经有 .ndf-active → 跳过（不覆盖）
#   - --dry-run 只打印计划，不写任何文件

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_STATE_FILE_DEFAULT="$NDF_REPO_ROOT/.ndf/state.json"

DRY_RUN=0
STATE_FILE="$NDF_STATE_FILE_DEFAULT"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --state-file) STATE_FILE="$2"; shift 2 ;;
    -h|--help)
      cat <<EOF
Usage: ndf-migrate-v3.sh [--dry-run] [--state-file PATH]

Migrate NDF v2 central state.json → per-worktree .ndf-active (v3).

Options:
  --dry-run         Print plan only, write no files
  --state-file PATH Path to v2 state.json (default: $NDF_STATE_FILE_DEFAULT)
EOF
      exit 0
      ;;
    *) echo "Error: unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [[ ! -f "$STATE_FILE" ]]; then
  echo "ℹ️  no state file at $STATE_FILE — nothing to migrate."
  exit 0
fi

# 提取 active 字段
HAS_ACTIVE=$(jq -r '.active != null' "$STATE_FILE")
if [[ "$HAS_ACTIVE" != "true" ]]; then
  echo "ℹ️  state.json 存在但 .active 为 null — 没有需要迁移的 active feature。"
  echo "→ 仍将归档 state.json 以完成 v2→v3 切换..."
fi

PLAN_WRITES=()
PLAN_SKIPS=()

if [[ "$HAS_ACTIVE" == "true" ]]; then
  ACTIVE_JSON=$(jq -c '.active' "$STATE_FILE")
  ACTIVE_ID=$(jq -r '.id' <<<"$ACTIVE_JSON")
  ACTIVE_TRACK=$(jq -r '.track' <<<"$ACTIVE_JSON")
  ACTIVE_STAGE=$(jq -r '.stage' <<<"$ACTIVE_JSON")
  echo "Found active v2 feature: $ACTIVE_ID ($ACTIVE_TRACK / $ACTIVE_STAGE)"
  echo "Worktrees in state:"
  jq -r '.active.worktrees | to_entries[] | "  - " + .key + " → " + .value' "$STATE_FILE"

  # 给每个 worktree 写 .ndf-active
  REPOS=()
  while IFS= read -r line; do
    REPOS+=("$line")
  done < <(jq -r '.active.repos[]' "$STATE_FILE")

  for REPO in "${REPOS[@]}"; do
    WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$STATE_FILE")
    if [[ -z "$WT_PATH" || "$WT_PATH" == "null" ]]; then
      PLAN_SKIPS+=("$REPO: state.json 缺 worktree 路径")
      continue
    fi
    if [[ ! -d "$WT_PATH" ]]; then
      PLAN_SKIPS+=("$REPO: worktree 不存在 ($WT_PATH) — 跳过")
      continue
    fi
    if [[ -f "$WT_PATH/.ndf-active" ]]; then
      EXISTING_ID=$(jq -r '.id // empty' "$WT_PATH/.ndf-active" 2>/dev/null || echo "")
      PLAN_SKIPS+=("$REPO: $WT_PATH/.ndf-active 已存在 (id=$EXISTING_ID) — 跳过")
      continue
    fi
    PLAN_WRITES+=("$REPO|$WT_PATH")
  done
fi

# 生成 v3 内容
build_v3_payload() {
  jq -n \
    --argjson active "$ACTIVE_JSON" \
    '{
      version: "ndf-v3",
      id: $active.id,
      track: $active.track,
      stage: $active.stage,
      created_at: $active.created_at,
      repos: $active.repos,
      worktrees: $active.worktrees,
      branches: $active.branches,
      review_policy: $active.review_policy,
      blockers: ($active.blockers // [])
    }'
}

# 打印计划
echo
echo "─── 迁移计划 ───"
echo "Writes (${#PLAN_WRITES[@]}):"
for w in "${PLAN_WRITES[@]}"; do
  echo "  + ${w%|*} → ${w#*|}/.ndf-active"
done
echo "Skips (${#PLAN_SKIPS[@]}):"
for s in "${PLAN_SKIPS[@]}"; do
  echo "  - $s"
done
echo

if [[ $DRY_RUN -eq 1 ]]; then
  echo "(dry-run) — 退出，未写任何文件。"
  exit 0
fi

# 执行写入
if [[ "$HAS_ACTIVE" == "true" && ${#PLAN_WRITES[@]} -gt 0 ]]; then
  V3_PAYLOAD=$(build_v3_payload)
  for w in "${PLAN_WRITES[@]}"; do
    REPO="${w%|*}"
    WT_PATH="${w#*|}"
    printf '%s\n' "$V3_PAYLOAD" > "$WT_PATH/.ndf-active"
    echo "✓ wrote $WT_PATH/.ndf-active"

    # 防御性 exclude：同 ndf-start 的逻辑
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
    REPO_PATH="$NDF_REPO_ROOT/../$REPO"
    if [[ -d "$REPO_PATH/.git/info" ]]; then
      if [[ ! -f "$REPO_PATH/.git/info/exclude" ]] || ! grep -qxF '.ndf-active' "$REPO_PATH/.git/info/exclude" 2>/dev/null; then
        echo '.ndf-active' >> "$REPO_PATH/.git/info/exclude"
      fi
    fi
  done
fi

# 归档 state.json
ARCHIVE_DATE=$(date +%Y%m%d)
ARCHIVE_PATH="$STATE_FILE.v2-archive-$ARCHIVE_DATE.json"
# 如果已经存在同名归档，追加 -HHMMSS
if [[ -e "$ARCHIVE_PATH" ]]; then
  ARCHIVE_PATH="$STATE_FILE.v2-archive-$ARCHIVE_DATE-$(date +%H%M%S).json"
fi
cp "$STATE_FILE" "$ARCHIVE_PATH"
echo "✓ archived $STATE_FILE → $ARCHIVE_PATH"

rm "$STATE_FILE"
echo "✓ removed $STATE_FILE"

echo
echo "─── 迁移完成 ───"
echo "  写入 .ndf-active 数: ${#PLAN_WRITES[@]}"
echo "  跳过: ${#PLAN_SKIPS[@]}"
echo "  state.json 归档为: $ARCHIVE_PATH"
echo
echo "下一步：跑 ndf-status 验证活跃 feature 已全部识别。"

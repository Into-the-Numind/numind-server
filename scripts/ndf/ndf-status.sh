#!/usr/bin/env bash
# ndf-status: 显示当前 NDF 状态
#
# Usage:
#   ndf-status            # 人类可读输出
#   ndf-status --md       # markdown 格式（截图分享用）
#   ndf-status --json     # 原始 JSON（脚本调用）
#
# 信息：active feature / stage / branch / worktree / HEAD 一致性 / blockers / next step

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"
NDF_STATE_FILE="$NDF_REPO_ROOT/.ndf/state.json"

FORMAT="text"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --md|--markdown) FORMAT="md"; shift ;;
    --json)          FORMAT="json"; shift ;;
    -h|--help)
      echo "Usage: ndf-status [--md|--json]"
      exit 0
      ;;
    *)
      echo "Error: unknown arg: $1" >&2; exit 1
      ;;
  esac
done

# State file 不存在 = 完全没用过 NDF
if [[ ! -f "$NDF_STATE_FILE" ]]; then
  case "$FORMAT" in
    json) echo '{"version":"ndf-v2","active_feature":null,"active":null}' ;;
    md)   echo "**NDF Status:** no state file (NDF v2 未初始化)" ;;
    text) echo "NDF Status: no state file (NDF v2 未初始化)" ;;
  esac
  exit 0
fi

# JSON 直接 dump
if [[ "$FORMAT" == "json" ]]; then
  cat "$NDF_STATE_FILE"
  exit 0
fi

ACTIVE_ID=$(jq -r '.active_feature // empty' "$NDF_STATE_FILE")

# 无 active feature
if [[ -z "$ACTIVE_ID" ]]; then
  case "$FORMAT" in
    md)   echo "**NDF Status:** no active feature" ;;
    text) echo "NDF Status: no active feature (可以 ndf-start 新 feature)" ;;
  esac
  exit 0
fi

# 有 active feature - 提取详情
TRACK=$(jq -r '.active.track' "$NDF_STATE_FILE")
STAGE=$(jq -r '.active.stage' "$NDF_STATE_FILE")
CREATED=$(jq -r '.active.created_at' "$NDF_STATE_FILE")
REVIEW_POLICY=$(jq -r '.active.review_policy' "$NDF_STATE_FILE")
REPOS=($(jq -r '.active.repos[]' "$NDF_STATE_FILE"))
BLOCKERS_COUNT=$(jq -r '.active.blockers | length' "$NDF_STATE_FILE")

# HEAD 一致性检查
HEAD_REPORT=""
HEAD_OK=1
for REPO in "${REPOS[@]}"; do
  EXPECTED_BRANCH=$(jq -r ".active.branches[\"$REPO\"]" "$NDF_STATE_FILE")
  WT_PATH=$(jq -r ".active.worktrees[\"$REPO\"]" "$NDF_STATE_FILE")
  if [[ ! -d "$WT_PATH" ]]; then
    HEAD_REPORT+="  ⚠️  $REPO: worktree 不存在 ($WT_PATH)\n"
    HEAD_OK=0
  else
    ACTUAL_BRANCH=$(git -C "$WT_PATH" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")
    if [[ "$ACTUAL_BRANCH" == "$EXPECTED_BRANCH" ]]; then
      HEAD_REPORT+="  ✓ $REPO: worktree HEAD = $EXPECTED_BRANCH\n"
    else
      HEAD_REPORT+="  ⚠️  $REPO: worktree HEAD = $ACTUAL_BRANCH (expected $EXPECTED_BRANCH)\n"
      HEAD_OK=0
    fi
  fi
done

# next step 建议
NEXT_STEP=""
case "$STAGE" in
  M1)  NEXT_STEP="改完直接 ndf-done（merge develop + 清理）" ;;
  H1)  NEXT_STEP="实现修复 + 加测试，完成后 H2 reviewer" ;;
  H2)  NEXT_STEP="dispatch reviewer subagent，PASS 后 H3" ;;
  H3)  NEXT_STEP="ndf-done（merge develop + 清理）→ 等 dev 验证 → tag prod" ;;
  S0)  NEXT_STEP="填 requirement card 模板，确认后进 S1" ;;
  S1)  NEXT_STEP="写 proposal+PRD（office-hours 思考），客户确认后 S2" ;;
  S2)  NEXT_STEP="写 spec + dispatch reviewer，PASS 后 S3" ;;
  S3)  NEXT_STEP="拆 task plan + 验证策略 + plan reviewer，PASS 后 S4" ;;
  S4)  NEXT_STEP="逐 task TDD 实现 + 每 task 双 reviewer 并行" ;;
  S5)  NEXT_STEP="本地跑 Playwright E2E + gstack /qa，PASS 后 S6" ;;
  S6)  NEXT_STEP="ndf-done（merge develop + 清理）→ 等 dev 部署 → 你点几下验收" ;;
  S7)  NEXT_STEP="打 prod tag → 等部署 → 写 Obsidian 笔记" ;;
  *)   NEXT_STEP="（未知 stage：$STAGE）" ;;
esac

# 输出
if [[ "$FORMAT" == "md" ]]; then
  cat <<EOF
## NDF Status

| 字段 | 值 |
|------|---|
| Active feature | \`$ACTIVE_ID\` |
| Track | $TRACK |
| Stage | $STAGE |
| Created | $CREATED |
| Review policy | $REVIEW_POLICY |
| Open blockers | $BLOCKERS_COUNT |

**Worktrees / HEAD:**
$(echo -e "$HEAD_REPORT" | sed 's/^/- /')

**Next step:** $NEXT_STEP
EOF
else
  cat <<EOF

╭─────────────────────────────────────────────────────────────
│ NDF Status
│
│   Active feature:  $ACTIVE_ID
│   Track:           $TRACK
│   Stage:           $STAGE
│   Created:         $CREATED
│   Review policy:   $REVIEW_POLICY
│   Open blockers:   $BLOCKERS_COUNT
│
│   Worktrees / HEAD:
$(printf '%b' "$HEAD_REPORT" | sed '/^$/d' | sed 's/^/│   /')
│   Next step: $NEXT_STEP
╰─────────────────────────────────────────────────────────────
EOF
  if [[ $HEAD_OK -eq 0 ]]; then
    echo "⚠️  HEAD 不一致——并行 session 切换过分支，先解决再继续"
  fi
fi

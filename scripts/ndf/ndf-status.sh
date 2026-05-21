#!/usr/bin/env bash
# ndf-status (v3): 扫描所有 worktree 找 .ndf-active 文件，报告每个活跃 feature
#
# Usage:
#   ndf-status            # 人类可读输出（表格）
#   ndf-status --md       # markdown 格式
#   ndf-status --json     # JSON 数组（脚本调用）
#
# v3 改动：不再读中央 state.json。
# 扫描位置：
#   - /private/tmp/wt-*/    （ndf-start 默认创建位置）
#   - $CODES/<repo>/.claude/worktrees/*/（备用位置）
#   - 任何额外路径由 NDF_EXTRA_WORKTREE_DIRS 环境变量指定（冒号分隔）
# 还会扫 git worktree list（如果某个 worktree 没 .ndf-active，标 ⚠ orphan）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDF_REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NDF_CODES_ROOT="$(cd "$NDF_REPO_ROOT/.." && pwd)"

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

# 1. 收集 worktree 候选路径
CANDIDATE_DIRS=()
# /private/tmp/wt-*
for d in /private/tmp/wt-*; do
  [[ -d "$d" ]] && CANDIDATE_DIRS+=("$d")
done
# repo-local .claude/worktrees/*/
for repo in numind-server numind-web-v3 numind-admin-web; do
  if [[ -d "$NDF_CODES_ROOT/$repo/.claude/worktrees" ]]; then
    for d in "$NDF_CODES_ROOT/$repo/.claude/worktrees"/*; do
      [[ -d "$d" ]] && CANDIDATE_DIRS+=("$d")
    done
  fi
done
# 额外父目录（冒号分隔）— 每个目录里 glob wt-* 作为候选
if [[ -n "${NDF_EXTRA_WORKTREE_DIRS:-}" ]]; then
  OLD_IFS="$IFS"
  IFS=':'
  set -- $NDF_EXTRA_WORKTREE_DIRS
  IFS="$OLD_IFS"
  for parent in "$@"; do
    [[ -d "$parent" ]] || continue
    for d in "$parent"/wt-*; do
      [[ -d "$d" ]] && CANDIDATE_DIRS+=("$d")
    done
  done
fi

# 2. 收集所有 git worktree list 给的路径
GIT_WT_PATHS=()
# Canonicalize codes root for symlink comparison
NDF_CODES_ROOT_CANONICAL=$(cd "$NDF_CODES_ROOT" 2>/dev/null && pwd -P)
for repo in numind-server numind-web-v3 numind-admin-web; do
  if [[ -d "$NDF_CODES_ROOT/$repo/.git" ]]; then
    main_canonical=$(cd "$NDF_CODES_ROOT/$repo" 2>/dev/null && pwd -P)
    while IFS= read -r line; do
      # 跳过主 worktree（line 等于 repo 主 checkout 路径），只收集附加 worktree
      line_canonical=$(cd "$line" 2>/dev/null && pwd -P) || line_canonical="$line"
      if [[ "$line_canonical" != "$main_canonical" ]]; then
        GIT_WT_PATHS+=("$line_canonical")
      fi
    done < <(git -C "$NDF_CODES_ROOT/$repo" worktree list --porcelain 2>/dev/null | awk '/^worktree /{print substr($0,10)}')
  fi
done

# 3. 合并 + 去重 (bash 3.2 compatible — no assoc arrays).
# 注意：macOS /var 是 /private/var 的符号链接，git 返回的 worktree 路径常常是
# /private/var/... 而 /private/tmp/wt-* 又是 /var 的另一种写法。先 canonicalize 再去重。
canonicalize_path() {
  # cd + pwd -P 是 portable canonicalize（不需要 readlink -f，bsd-readlink 不支持）
  local p="$1"
  if [[ -d "$p" ]]; then
    (cd "$p" 2>/dev/null && pwd -P) || printf '%s' "$p"
  else
    printf '%s' "$p"
  fi
}

ALL_DIRS=()
SEEN_LIST=$'\n'
for d in "${CANDIDATE_DIRS[@]}" "${GIT_WT_PATHS[@]}"; do
  cd_canonical=$(canonicalize_path "$d")
  case "$SEEN_LIST" in
    *$'\n'"$cd_canonical"$'\n'*) continue ;;
  esac
  SEEN_LIST+="$cd_canonical"$'\n'
  ALL_DIRS+=("$cd_canonical")
done

# 4. 读每个目录的 .ndf-active，分类
declare -a FEATURE_JSON_ENTRIES   # 已绑定 feature 的 worktree
declare -a ORPHANS                # 有 worktree 但无 .ndf-active

for d in "${ALL_DIRS[@]}"; do
  if [[ -f "$d/.ndf-active" ]]; then
    # 验证可解析
    if jq -e '.id' "$d/.ndf-active" >/dev/null 2>&1; then
      ENTRY=$(jq -c --arg path "$d" '. + {worktree_path: $path}' "$d/.ndf-active")
      FEATURE_JSON_ENTRIES+=("$ENTRY")
    else
      ORPHANS+=("$d (.ndf-active 文件存在但 JSON 解析失败)")
    fi
  else
    ORPHANS+=("$d")
  fi
done

# 5. 输出
if [[ "$FORMAT" == "json" ]]; then
  printf '{"version":"ndf-v3","features":['
  FIRST=1
  for e in "${FEATURE_JSON_ENTRIES[@]}"; do
    [[ $FIRST -eq 0 ]] && printf ','
    printf '%s' "$e"
    FIRST=0
  done
  printf '],"orphans":['
  FIRST=1
  for o in "${ORPHANS[@]}"; do
    [[ $FIRST -eq 0 ]] && printf ','
    printf '%s' "$(jq -n --arg p "$o" '$p')"
    FIRST=0
  done
  printf ']}\n'
  exit 0
fi

# Human-readable
if [[ ${#FEATURE_JSON_ENTRIES[@]} -eq 0 && ${#ORPHANS[@]} -eq 0 ]]; then
  if [[ "$FORMAT" == "md" ]]; then
    echo "**NDF Status (v3):** 没有发现任何活跃 feature"
  else
    echo "NDF Status (v3): 没有发现任何活跃 feature"
    echo "  → 跑 ndf-start <track> <slug> 启动新 feature"
  fi
  exit 0
fi

# 用 jq 把每个 feature entry 输出为行
print_feature_text() {
  local entry="$1"
  local id track stage repos wt_path
  id=$(jq -r '.id' <<<"$entry")
  track=$(jq -r '.track' <<<"$entry")
  stage=$(jq -r '.stage' <<<"$entry")
  repos=$(jq -r '.repos | join(",")' <<<"$entry")
  wt_path=$(jq -r '.worktree_path' <<<"$entry")
  # HEAD 一致性
  local branch_expected actual_branch head_ok=1
  branch_expected=$(jq -r ".branches[\"$(jq -r '.repos[0]' <<<"$entry")\"]" <<<"$entry")
  actual_branch=$(git -C "$wt_path" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")
  if [[ "$actual_branch" != "$branch_expected" ]]; then
    head_ok=0
  fi
  if [[ $head_ok -eq 1 ]]; then
    printf '  ✓ %-40s  %-9s  %-5s  %-30s  %s\n' "$id" "$track" "$stage" "$repos" "$wt_path"
  else
    printf '  ⚠ %-40s  %-9s  %-5s  %-30s  %s (HEAD=%s)\n' "$id" "$track" "$stage" "$repos" "$wt_path" "$actual_branch"
  fi
}

if [[ "$FORMAT" == "md" ]]; then
  echo "## NDF Status (v3)"
  echo
  echo "### Active features (${#FEATURE_JSON_ENTRIES[@]})"
  echo
  if [[ ${#FEATURE_JSON_ENTRIES[@]} -gt 0 ]]; then
    echo "| Feature | Track | Stage | Repos | Worktree |"
    echo "|---------|-------|-------|-------|----------|"
    for e in "${FEATURE_JSON_ENTRIES[@]}"; do
      id=$(jq -r '.id' <<<"$e")
      track=$(jq -r '.track' <<<"$e")
      stage=$(jq -r '.stage' <<<"$e")
      repos=$(jq -r '.repos | join(",")' <<<"$e")
      wt_path=$(jq -r '.worktree_path' <<<"$e")
      echo "| \`$id\` | $track | $stage | $repos | \`$wt_path\` |"
    done
  fi
  if [[ ${#ORPHANS[@]} -gt 0 ]]; then
    echo
    echo "### Orphan worktrees (${#ORPHANS[@]})"
    echo
    for o in "${ORPHANS[@]}"; do
      echo "- \`$o\`"
    done
  fi
  exit 0
fi

# text format
cat <<EOF

╭─ NDF Status (v3, per-worktree state)
│
EOF
if [[ ${#FEATURE_JSON_ENTRIES[@]} -gt 0 ]]; then
  echo "│ Active features (${#FEATURE_JSON_ENTRIES[@]}):"
  echo "│"
  for e in "${FEATURE_JSON_ENTRIES[@]}"; do
    line=$(print_feature_text "$e")
    echo "│$line"
  done
fi
if [[ ${#ORPHANS[@]} -gt 0 ]]; then
  echo "│"
  echo "│ ⚠ Orphan worktrees (${#ORPHANS[@]}):"
  for o in "${ORPHANS[@]}"; do
    echo "│   - $o"
  done
  echo "│   → 这些 worktree 没有 .ndf-active 文件，可能是手动创建或迁移残留。"
  echo "│   → 排查：cd <path> && git status；干净则 git worktree remove 清理。"
fi
echo "╰─"

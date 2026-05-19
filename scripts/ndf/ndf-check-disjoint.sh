#!/usr/bin/env bash
# ndf-check-disjoint: 检查多组文件集合两两无交集
#
# 用途：Tier 3 并行 dispatch 前，主 session 验证将要派给不同 agent 的文件归属无重叠。
#
# Usage:
#   ndf-check-disjoint "fileA,fileB" "fileC,fileD"             # 两组
#   ndf-check-disjoint "set1" "set2" "set3"                     # 三组（pairwise）
#
# Exit codes:
#   0 = 全部组两两无交集，安全可并行
#   1 = 发现交集，禁止并行
#   2 = 参数错误

set -e

if [[ $# -lt 2 ]]; then
  cat <<EOF >&2
Usage: ndf-check-disjoint "set1_files" "set2_files" [...]

每组文件用逗号分隔。最少 2 组。

Examples:
  ndf-check-disjoint "internal/biz/sop/store.go,internal/biz/sop/list.go" "internal/controller/v1/sop.go"
  # → OK (set1=biz, set2=controller, 无交集)

  ndf-check-disjoint "a.go,b.go" "b.go,c.go"
  # → OVERLAP: b.go between set1 and set2
EOF
  exit 2
fi

# 把每个参数（逗号分隔的文件集合）转成行分隔的临时文件
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

I=1
for ARG in "$@"; do
  # 去空格 + 逗号转换行
  echo "$ARG" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$' | sort -u > "$TMP_DIR/set$I.txt"
  I=$((I + 1))
done

N=$#
ANY_OVERLAP=0
REPORT=""

# Pairwise 交集检查
I=1
while [[ $I -lt $N ]]; do
  J=$((I + 1))
  while [[ $J -le $N ]]; do
    OVERLAP=$(comm -12 "$TMP_DIR/set$I.txt" "$TMP_DIR/set$J.txt" 2>/dev/null || true)
    if [[ -n "$OVERLAP" ]]; then
      ANY_OVERLAP=1
      REPORT+="  set$I × set$J:\n"
      while IFS= read -r f; do
        [[ -n "$f" ]] && REPORT+="    - $f\n"
      done <<< "$OVERLAP"
    fi
    J=$((J + 1))
  done
  I=$((I + 1))
done

if [[ $ANY_OVERLAP -eq 1 ]]; then
  echo "❌ OVERLAP detected:" >&2
  printf "%b" "$REPORT" >&2
  echo "" >&2
  echo "→ Tier 3 并行禁止。拆 task 让文件集合不重叠，或改串行。" >&2
  exit 1
fi

echo "✓ OK: $N 组文件两两无交集，Tier 3 并行安全"
exit 0

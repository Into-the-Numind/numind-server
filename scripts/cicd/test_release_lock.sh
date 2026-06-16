#!/usr/bin/env bash
# 测试 release.sh acquire_release_lock 所依赖的 per-repo mkdir 锁算法。
# 验证：① 空闲时获取成功 ② 已持有时互斥(并发第二次失败) ③ 陈旧锁(持有进程已死)被识别并清理
# ④ 活锁(持有进程仍在)不被误抢。release.sh 非 sourceable(顶部 ENV=${1:?}),故此处独立复刻
# 同一算法做行为回归(deploy-rsync-lock, 2026-06-16)。
set -euo pipefail

PASS=0
FAIL=0
check() { # check <desc> <expected:0|1> <actual_exit>
  if [ "$2" = "$3" ]; then echo "  ✓ $1"; PASS=$((PASS + 1)); else echo "  ✗ $1 (expected exit=$2 got=$3)"; FAIL=$((FAIL + 1)); fi
}

LOCK="$(mktemp -d)/numind-release-test.lock"
rm -rf "$LOCK"

# ① 空闲时 mkdir 获取成功（rc 必须捕获 mkdir 本身的退出码，不能被后续 echo 覆盖）
mkdir "$LOCK" 2>/dev/null; rc=$?
if [ "$rc" -eq 0 ]; then echo $$ > "$LOCK/pid"; fi
check "空闲时获取锁成功" 0 "$rc"

# ② 已持有时再 mkdir 同一锁 → 失败(原子互斥)
if mkdir "$LOCK" 2>/dev/null; then rc=0; else rc=1; fi
check "已持有时并发获取被互斥(mkdir 失败)" 1 "$rc"

# ③ 陈旧锁判定：写一个已退出进程的 PID，stale 检测应判为 true(可清理)
DEAD_PID=999999
while kill -0 "$DEAD_PID" 2>/dev/null; do DEAD_PID=$((DEAD_PID + 1)); done
echo "$DEAD_PID" > "$LOCK/pid"
holder="$(cat "$LOCK/pid" 2>/dev/null || true)"
if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then rc=0; else rc=1; fi
check "持有进程已死 → 判定为陈旧锁(可清理)" 0 "$rc"

# ④ 活锁不被误判为陈旧(用当前 shell PID 作活进程)
echo "$$" > "$LOCK/pid"
holder="$(cat "$LOCK/pid" 2>/dev/null || true)"
if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then rc=0; else rc=1; fi
check "持有进程仍在 → 不被误判为陈旧锁" 1 "$rc"

# 清理后可重新获取(模拟 stale 清理 → 重试成功)
rm -rf "$LOCK"
mkdir "$LOCK" 2>/dev/null; rc=$?
check "清理陈旧锁后可重新获取" 0 "$rc"
rm -rf "$LOCK"

echo "----"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]

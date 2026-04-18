#!/usr/bin/env bash
# verify_track_a_schema.sh — Track A DDL migration smoke test
#
# 用途：
#   在本地 MySQL 8.0 test container 上执行 Track A 的 4 个 DDL migration
#   (100000-100300)，验证 apply 成功、schema 字段/索引符合 spec §2.2-§2.5，
#   然后反序执行 rollback 验证回滚干净。最后再 re-apply 一次，证明可重复。
#
# 前置条件：
#   - Docker daemon running
#   - mysql-client in PATH (或通过 MYSQL_BIN env 指定)
#
# 用法：
#   bash migrations/verify_track_a_schema.sh
#
# 退出码：
#   0 = 全部验证通过
#   非 0 = 任一步失败
#
# 此脚本不修改 Phase 0 冻结的 migration SQL 文件本身，只作为可重跑的回归
# 验证工件。

set -euo pipefail

MYSQL_BIN="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client/bin/mysql}"
CONTAINER="${CONTAINER:-credits-mysql-test}"
PORT="${PORT:-13306}"
DB="${DB:-numind_test}"
PASS="${PASS:-root}"

MYSQL="$MYSQL_BIN -h 127.0.0.1 -P $PORT -uroot -p$PASS $DB"

scriptdir="$(cd "$(dirname "$0")" && pwd)"
cd "$scriptdir"

start_container() {
    if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
        echo ">> starting fresh $CONTAINER"
        docker rm -f "$CONTAINER" 2>/dev/null || true
        docker run --rm -d --name "$CONTAINER" \
            -e MYSQL_ROOT_PASSWORD="$PASS" \
            -e MYSQL_DATABASE="$DB" \
            -p "${PORT}:3306" mysql:8.0 >/dev/null

        echo -n ">> waiting for MySQL ready"
        for _ in {1..60}; do
            if $MYSQL -e "SELECT 1" >/dev/null 2>&1; then
                echo " ready"
                return
            fi
            echo -n "."
            sleep 2
        done
        echo ""
        echo "!! MySQL did not become ready in time"
        exit 1
    fi
}

bootstrap_user_table() {
    # migration 100000 用 ALTER TABLE user，需要 user 表先存在。
    # 正式生产环境 user 表由主 migration 流创建；本脚本只建一个最小等价
    # schema 用于 DDL 验证。
    $MYSQL <<'SQL'
DROP TABLE IF EXISTS credit_reservation_item;
DROP TABLE IF EXISTS credit_reservation;
DROP TABLE IF EXISTS credit_estimation_coefficient;
DROP TABLE IF EXISTS `user`;
CREATE TABLE `user` (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    created_at DATETIME(3),
    updated_at DATETIME(3),
    deleted_at DATETIME(3),
    phone VARCHAR(20),
    nickname VARCHAR(100),
    avatar_url VARCHAR(512),
    parent_user_id INT UNSIGNED,
    total_sop_runs INT DEFAULT 0,
    monthly_sop_runs INT DEFAULT 0,
    monthly_reset_at DATETIME(3),
    user_tier VARCHAR(20) DEFAULT 'free',
    tier_expires DATETIME(3),
    username VARCHAR(50),
    password VARCHAR(255),
    is_admin TINYINT(1) DEFAULT 0,
    status INT DEFAULT 0,
    last_login DATETIME(3),
    INDEX idx_user_tier (user_tier),
    INDEX idx_tier_expires (tier_expires)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
SQL
}

apply_up() {
    for f in 20260419_100000_add_billing_mode_to_user.sql \
             20260419_100100_create_credit_estimation_coefficient.sql \
             20260419_100200_create_credit_reservation.sql \
             20260419_100300_create_credit_reservation_item.sql; do
        echo ">> apply $f"
        $MYSQL < "$f"
    done
}

apply_down() {
    # reverse order
    for f in 20260419_100300_create_credit_reservation_item_rollback.sql \
             20260419_100200_create_credit_reservation_rollback.sql \
             20260419_100100_create_credit_estimation_coefficient_rollback.sql \
             20260419_100000_add_billing_mode_to_user_rollback.sql; do
        echo ">> rollback $f"
        $MYSQL < "$f"
    done
}

assert_tables() {
    expected="$1"   # space-separated expected table names
    got=$($MYSQL -Nse "SHOW TABLES" | sort | xargs)
    want=$(echo "$expected" | xargs -n1 | sort | xargs)
    if [ "$got" != "$want" ]; then
        echo "!! tables mismatch: got='$got' want='$want'"
        exit 1
    fi
}

assert_column() {
    table="$1"; column="$2"
    count=$($MYSQL -Nse "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='$DB' AND table_name='$table' AND column_name='$column'")
    if [ "$count" != "1" ]; then
        echo "!! expected $table.$column to exist (found $count)"
        exit 1
    fi
}

assert_no_column() {
    table="$1"; column="$2"
    count=$($MYSQL -Nse "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='$DB' AND table_name='$table' AND column_name='$column'")
    if [ "$count" != "0" ]; then
        echo "!! expected $table.$column to be absent (found $count)"
        exit 1
    fi
}

verify_envsubst_whitelist() {
    # envsubst '${MIGRATION_CUTOFF}' 必须只替换该占位符，保留其他 $(...) 不动。
    local out
    out=$(MIGRATION_CUTOFF="2026-05-08 00:00:00" envsubst '${MIGRATION_CUTOFF}' < 20260419_100500_init_billing_mode_values.sql)

    if ! echo "$out" | grep -q "'2026-05-08 00:00:00'"; then
        echo "!! envsubst did not substitute MIGRATION_CUTOFF"
        exit 1
    fi
    # 注释里的 $(date -u ...) 应被保留（白名单模式只替换指定变量）
    if ! echo "$out" | grep -q '\$(date'; then
        echo "!! envsubst whitelist leaked — other \$ sequences were clobbered"
        exit 1
    fi
    echo ">> envsubst whitelist OK (MIGRATION_CUTOFF replaced, \$(date ...) preserved)"
}

main() {
    start_container
    bootstrap_user_table

    echo ""
    echo "== PHASE 1: apply 100000-100300 =="
    apply_up
    assert_tables "user credit_estimation_coefficient credit_reservation credit_reservation_item"
    assert_column "user" "billing_mode"
    assert_column "credit_estimation_coefficient" "safety_buffer_pct"
    assert_column "credit_reservation" "idempotency_key"
    assert_column "credit_reservation_item" "seq"

    echo ""
    echo "== PHASE 2: rollback in reverse =="
    apply_down
    assert_tables "user"
    assert_no_column "user" "billing_mode"

    echo ""
    echo "== PHASE 3: re-apply (idempotency) =="
    apply_up
    assert_tables "user credit_estimation_coefficient credit_reservation credit_reservation_item"

    echo ""
    echo "== PHASE 4: envsubst whitelist verification for 100500 =="
    verify_envsubst_whitelist

    echo ""
    echo "SUCCESS: migrations 100000-100300 apply + rollback + re-apply all clean;"
    echo "         envsubst whitelist substitution for 100500 verified."
}

main "$@"

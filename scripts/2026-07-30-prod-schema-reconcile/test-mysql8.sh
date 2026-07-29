#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MIGRATION="$REPO_ROOT/migrations/20260730_120000_prod_schema_reconcile.sql"
PREFLIGHT="$SCRIPT_DIR/00-preflight.sql"
VERIFY="$SCRIPT_DIR/02-verify.sql"
BASELINE="$SCRIPT_DIR/testdata/prod-partial-baseline.sql"

CONTAINER="numind-prod-reconcile-mysql8-$$"
DATABASE="numind_reconcile_test"
TEST_PASSWORD="reconcile-test-only"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

mysql_stdin() {
  docker exec -i "$CONTAINER" sh -c \
    'mysql -B -uroot -p"$MYSQL_ROOT_PASSWORD" --default-character-set=utf8mb4 "$MYSQL_DATABASE"'
}

mysql_query() {
  local query="$1"
  docker exec "$CONTAINER" sh -c \
    'mysql -N -B -uroot -p"$MYSQL_ROOT_PASSWORD" --default-character-set=utf8mb4 "$MYSQL_DATABASE" -e "$1"' \
    sh "$query"
}

assert_no_fail_rows() {
  local output="$1"
  local label="$2"
  if printf '%s\n' "$output" | grep -q $'\tFAIL\t'; then
    printf '%s\n' "$output"
    echo "ERROR: $label contains FAIL rows" >&2
    exit 1
  fi
}

subscription_projection_sql="
SELECT SHA2(
  GROUP_CONCAT(
    CONCAT_WS(
      '|', id, user_id,
      DATE_FORMAT(first_started_at, '%Y-%m-%d %H:%i:%s'),
      DATE_FORMAT(current_started_at, '%Y-%m-%d %H:%i:%s'),
      DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%s'),
      total_months_purchased, source, IFNULL(granter_user_id, '<NULL>'),
      DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s'),
      DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s')
    )
    ORDER BY id SEPARATOR '\n'
  ),
  256
) FROM subscription;
"

docker run -d \
  --name "$CONTAINER" \
  --tmpfs /var/lib/mysql:rw,noexec,nosuid,size=1g \
  -e "MYSQL_ROOT_PASSWORD=$TEST_PASSWORD" \
  -e "MYSQL_DATABASE=$DATABASE" \
  mysql:8.4 >/dev/null

for _ in $(seq 1 90); do
  if docker exec "$CONTAINER" mysql \
    -N -B -uroot -p"$TEST_PASSWORD" -e "SELECT 1" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! docker exec "$CONTAINER" mysql \
  -N -B -uroot -p"$TEST_PASSWORD" -e "SELECT 1" >/dev/null 2>&1; then
  echo "ERROR: isolated MySQL 8 did not become ready" >&2
  exit 1
fi

mysql_stdin < "$BASELINE"

preflight_output="$(mysql_stdin < "$PREFLIGHT")"
assert_no_fail_rows "$preflight_output" "preflight"

before_projection="$(mysql_query "$subscription_projection_sql")"
before_new_table_count="$(mysql_query "
SELECT COUNT(*) FROM information_schema.TABLES
WHERE TABLE_SCHEMA=DATABASE()
  AND TABLE_NAME IN (
    'document','user_third_party_account','feishu_cli_vault',
    'feishu_auth_session','feishu_operation',
    'feishu_operation_proof_consumption','feishu_operation_execution_gate'
  );
")"
if [ "$before_new_table_count" != "0" ]; then
  echo "ERROR: synthetic baseline unexpectedly contains rollout tables" >&2
  exit 1
fi

mysql_stdin < "$MIGRATION" >/dev/null
verify_first="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$verify_first" "first verify"
after_first_projection="$(mysql_query "$subscription_projection_sql")"
if [ "$after_first_projection" != "$before_projection" ]; then
  echo "ERROR: first apply changed protected subscription projection" >&2
  exit 1
fi

first_config_counts="$(mysql_query "
SELECT CONCAT_WS(
  '/',
  (SELECT COUNT(*) FROM ai_service WHERE model_key='qwen3.5-flash'),
  (SELECT COUNT(*) FROM task_profile WHERE task_id='attachment.vision_describe'),
  (SELECT COUNT(*) FROM pricing_rule
   WHERE service_type='llm_vision' AND provider='ali-dashscope' AND model='qwen3.5-flash')
);
")"

mysql_stdin < "$MIGRATION" >/dev/null
verify_second="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$verify_second" "second verify"
after_second_projection="$(mysql_query "$subscription_projection_sql")"
if [ "$after_second_projection" != "$before_projection" ]; then
  echo "ERROR: second apply changed protected subscription projection" >&2
  exit 1
fi

second_config_counts="$(mysql_query "
SELECT CONCAT_WS(
  '/',
  (SELECT COUNT(*) FROM ai_service WHERE model_key='qwen3.5-flash'),
  (SELECT COUNT(*) FROM task_profile WHERE task_id='attachment.vision_describe'),
  (SELECT COUNT(*) FROM pricing_rule
   WHERE service_type='llm_vision' AND provider='ali-dashscope' AND model='qwen3.5-flash')
);
")"
if [ "$first_config_counts" != "1/1/1" ] || [ "$second_config_counts" != "1/1/1" ]; then
  echo "ERROR: stable-key configuration rows duplicated: first=$first_config_counts second=$second_config_counts" >&2
  exit 1
fi

attachment_backfill="$(mysql_query "
SELECT CONCAT_WS(
  '/',
  COUNT(*),
  SUM(parsed_content='synthetic parsed text'),
  SUM(parsed_content_sha256 LIKE 'sha256:%'),
  SUM(parsed_content_byte_size=21)
)
FROM agent_attachment
WHERE id=1;
")"
if [ "$attachment_backfill" != "1/1/1/1" ]; then
  echo "ERROR: attachment canonical-content backfill mismatch: $attachment_backfill" >&2
  exit 1
fi

echo "PASS: MySQL 8 preflight, double apply, double verify, protected hash, and stable-key checks"

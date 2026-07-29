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

assert_equal() {
  local before="$1"
  local after="$2"
  local label="$3"
  if [ "$before" != "$after" ]; then
    echo "ERROR: $label changed" >&2
    echo "before=$before" >&2
    echo "after=$after" >&2
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

attachment_projection_sql="
SELECT CONCAT_WS(
  ':',
  COUNT(*),
  SHA2(
    GROUP_CONCAT(
      SHA2(CONCAT_WS(
        '|',
        id, user_id, COALESCE(url, '<NULL>'), COALESCE(filename, '<NULL>'),
        COALESCE(mime_type, '<NULL>'), COALESCE(CAST(size AS CHAR), '<NULL>'),
        COALESCE(modality, '<NULL>'), COALESCE(CAST(width AS CHAR), '<NULL>'),
        COALESCE(CAST(height AS CHAR), '<NULL>'), COALESCE(ocr_text, '<NULL>'),
        COALESCE(vision_description, '<NULL>'), COALESCE(text_fallback, '<NULL>'),
        COALESCE(CAST(fallback_ready AS CHAR), '<NULL>'), COALESCE(fallback_error, '<NULL>'),
        COALESCE(DATE_FORMAT(fallback_started_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>'),
        COALESCE(DATE_FORMAT(fallback_completed_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>'),
        COALESCE(CAST(retry_count AS CHAR), '<NULL>'),
        COALESCE(DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>')
      ), 256)
      ORDER BY id SEPARATOR '\n'
    ),
    256
  )
) FROM agent_attachment;
"

agent_run_projection_sql="
SELECT CONCAT_WS(
  ':',
  COUNT(*),
  SHA2(
    GROUP_CONCAT(
      SHA2(CONCAT_WS(
        '|',
        id, user_id, COALESCE(session_id, '<NULL>'), status,
        COALESCE(state_reason, '<NULL>'), COALESCE(CAST(terminal_metadata AS CHAR), '<NULL>'),
        CAST(messages AS CHAR), COALESCE(CAST(reservation_id AS CHAR), '<NULL>'),
        DATE_FORMAT(started_at, '%Y-%m-%d %H:%i:%s.%f'),
        COALESCE(DATE_FORMAT(ended_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>'),
        COALESCE(DATE_FORMAT(cancellation_requested_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>'),
        agent_definition_id, COALESCE(CAST(pending_question_json AS CHAR), '<NULL>'),
        COALESCE(DATE_FORMAT(pending_question_at, '%Y-%m-%d %H:%i:%s.%f'), '<NULL>'),
        DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'),
        DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s.%f'),
        use_compact_v2, is_pinned, session_name, is_deleted, is_test
      ), 256)
      ORDER BY id SEPARATOR '\n'
    ),
    256
  )
) FROM agent_run;
"

protected_checksum_sql="
CHECKSUM TABLE
  user, trial_grant, credit_account, credit_cycle, user_booster_balance,
  membership_event, credit_reservation,
  credit_reservation_item, credit_transaction, sop_run, sop_node_run,
  chatbot_session, chatbot_message, sales_session, sales_message
EXTENDED;
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
  docker logs "$CONTAINER" >&2 || true
  exit 1
fi

mysql_stdin < "$BASELINE"

preflight_output="$(mysql_stdin < "$PREFLIGHT")"
assert_no_fail_rows "$preflight_output" "preflight"

before_projection="$(mysql_query "$subscription_projection_sql")"
before_attachment_projection="$(mysql_query "$attachment_projection_sql")"
before_attachment_complete_projection="$(
  printf '%s\n' "$preflight_output" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
before_agent_run_projection="$(mysql_query "$agent_run_projection_sql")"
before_protected_checksums="$(mysql_query "$protected_checksum_sql")"
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
after_first_attachment_projection="$(mysql_query "$attachment_projection_sql")"
after_first_attachment_complete_projection="$(
  printf '%s\n' "$verify_first" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
after_first_agent_run_projection="$(mysql_query "$agent_run_projection_sql")"
after_first_protected_checksums="$(mysql_query "$protected_checksum_sql")"
assert_equal "$before_projection" "$after_first_projection" "first-apply subscription projection"
assert_equal "$before_attachment_projection" "$after_first_attachment_projection" "first-apply attachment projection"
assert_equal "$before_attachment_complete_projection" "$after_first_attachment_complete_projection" \
  "first-apply complete attachment projection"
assert_equal "$before_agent_run_projection" "$after_first_agent_run_projection" "first-apply agent-run projection"
assert_equal "$before_protected_checksums" "$after_first_protected_checksums" "first-apply protected checksums"

# Physical column order is not a runtime contract. Reorder one exact Feishu
# column and require both schema gates to remain green.
mysql_query "
ALTER TABLE feishu_auth_session
  MODIFY protocol_version TINYINT UNSIGNED NOT NULL DEFAULT 1 AFTER completed_at;
"
reordered_preflight="$(mysql_stdin < "$PREFLIGHT")"
assert_no_fail_rows "$reordered_preflight" "reordered-column preflight"
reordered_verify="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$reordered_verify" "reordered-column verify"

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
after_second_attachment_projection="$(mysql_query "$attachment_projection_sql")"
after_second_attachment_complete_projection="$(
  printf '%s\n' "$verify_second" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
after_second_agent_run_projection="$(mysql_query "$agent_run_projection_sql")"
after_second_protected_checksums="$(mysql_query "$protected_checksum_sql")"
assert_equal "$before_projection" "$after_second_projection" "second-apply subscription projection"
assert_equal "$before_attachment_projection" "$after_second_attachment_projection" "second-apply attachment projection"
assert_equal "$before_attachment_complete_projection" "$after_second_attachment_complete_projection" \
  "second-apply complete attachment projection"
assert_equal "$before_agent_run_projection" "$after_second_agent_run_projection" "second-apply agent-run projection"
assert_equal "$before_protected_checksums" "$after_second_protected_checksums" "second-apply protected checksums"

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

attachment_new_values="$(mysql_query "
SELECT COUNT(*)
FROM agent_attachment
WHERE parsed_content IS NOT NULL
   OR parsed_content_sha256 <> ''
   OR parsed_content_byte_size <> 0
   OR parsed_page_count <> 0
   OR parsed_at IS NOT NULL;
")"
if [ "$attachment_new_values" != "0" ]; then
  echo "ERROR: migration wrote new attachment values into historical rows" >&2
  exit 1
fi

mysql_query "UPDATE agent_run SET state_reason='external_resume_ready' WHERE id=1;"
mysql_query "UPDATE agent_run SET state_reason='ext_resume:synthetic' WHERE id=1;"
if mysql_query "UPDATE agent_run SET state_reason='extXresume:synthetic' WHERE id=1;" >/dev/null 2>&1; then
  echo "ERROR: agent_run CHECK treated '_' as a wildcard" >&2
  exit 1
fi
if mysql_query "UPDATE agent_run SET state_reason='not-a-runtime-state' WHERE id=1;" >/dev/null 2>&1; then
  echo "ERROR: agent_run CHECK accepted an unsupported state" >&2
  exit 1
fi
if mysql_query "UPDATE agent_run SET state_reason='', status='terminated' WHERE id=1;" >/dev/null 2>&1; then
  echo "ERROR: agent_run CHECK accepted an empty state outside a running row" >&2
  exit 1
fi
if mysql_query "
  UPDATE agent_run
  SET state_reason='zombie_cleanup_2026_05_28', is_deleted=0
  WHERE id=1;
" >/dev/null 2>&1; then
  echo "ERROR: agent_run CHECK accepted zombie cleanup on a visible row" >&2
  exit 1
fi
mysql_query "UPDATE agent_run SET state_reason='completed' WHERE id=1;"

# A same-name but overly broad CHECK must be detected and repaired exactly.
mysql_query "
ALTER TABLE agent_run
  DROP CHECK chk_ar_state_reason,
  ADD CONSTRAINT chk_ar_state_reason CHECK (state_reason IS NULL OR 1=1);
"
overbroad_verify="$(mysql_stdin < "$VERIFY")"
if ! printf '%s\n' "$overbroad_verify" | grep -q '^agent_state_reason_constraint'$'\tFAIL\t'; then
  echo "ERROR: verify accepted an overly broad agent state CHECK" >&2
  exit 1
fi
mysql_stdin < "$MIGRATION" >/dev/null
repaired_constraint_verify="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$repaired_constraint_verify" "repaired agent constraint verify"
if mysql_query "UPDATE agent_run SET state_reason='extXresume:synthetic' WHERE id=1;" >/dev/null 2>&1; then
  echo "ERROR: repaired agent_run CHECK accepted wildcard-like state" >&2
  exit 1
fi

# Simulate interruption after only some exact target tables were created.
mysql_query "DROP TABLE feishu_operation_proof_consumption;"
mysql_query "DROP TABLE feishu_auth_session;"
mysql_query "DROP TABLE document;"
partial_preflight="$(mysql_stdin < "$PREFLIGHT")"
assert_no_fail_rows "$partial_preflight" "partial-state preflight"
mysql_stdin < "$MIGRATION" >/dev/null
partial_verify="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$partial_verify" "partial-state verify"
assert_equal "$before_projection" "$(mysql_query "$subscription_projection_sql")" "partial-retry subscription projection"
assert_equal "$before_attachment_projection" "$(mysql_query "$attachment_projection_sql")" "partial-retry attachment projection"
partial_attachment_complete_projection="$(
  printf '%s\n' "$partial_verify" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
assert_equal "$before_attachment_complete_projection" "$partial_attachment_complete_projection" \
  "partial-retry complete attachment projection"
assert_equal "$before_agent_run_projection" "$(mysql_query "$agent_run_projection_sql")" "partial-retry agent-run projection"
assert_equal "$before_protected_checksums" "$(mysql_query "$protected_checksum_sql")" "partial-retry protected checksums"

# Dev historically has a complete nullable parsed-content schema. Accept that
# exact shape, preserve NULL values byte-for-byte, and never normalize it during
# this rollout.
mysql_query "
ALTER TABLE agent_attachment
  MODIFY parsed_content_sha256 VARCHAR(71) NULL DEFAULT NULL,
  MODIFY parsed_content_byte_size BIGINT NULL DEFAULT 0,
  MODIFY parsed_page_count BIGINT NULL DEFAULT 0;
UPDATE agent_attachment
SET parsed_content_sha256=NULL,
    parsed_content_byte_size=NULL,
    parsed_page_count=NULL
WHERE id=1;
"
legacy_preflight="$(mysql_stdin < "$PREFLIGHT")"
assert_no_fail_rows "$legacy_preflight" "legacy attachment preflight"
if ! printf '%s\n' "$legacy_preflight" |
  grep -q '^attachment_parsed_column_shapes'$'\tPASS\tlegacy_complete'; then
  echo "ERROR: exact Dev attachment schema was not identified as legacy_complete" >&2
  exit 1
fi
before_legacy_attachment_projection="$(
  printf '%s\n' "$legacy_preflight" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
mysql_stdin < "$MIGRATION" >/dev/null
legacy_verify="$(mysql_stdin < "$VERIFY")"
assert_no_fail_rows "$legacy_verify" "legacy attachment verify"
after_legacy_attachment_projection="$(
  printf '%s\n' "$legacy_verify" |
    awk -F '\t' '$1 == "agent_attachment_complete_projection" { print $2 ":" $3 }'
)"
assert_equal "$before_legacy_attachment_projection" "$after_legacy_attachment_projection" \
  "legacy complete attachment projection"
if [ "$(mysql_query "
  SELECT COUNT(*) FROM agent_attachment
  WHERE id=1
    AND parsed_content_sha256 IS NULL
    AND parsed_content_byte_size IS NULL
    AND parsed_page_count IS NULL;
")" != "1" ]; then
  echo "ERROR: migration rewrote legacy nullable attachment values" >&2
  exit 1
fi

# A mixed shape is neither the reviewed Dev legacy schema nor the final schema.
mysql_query "ALTER TABLE agent_attachment MODIFY parsed_page_count INT NULL DEFAULT 0;"
mixed_attachment_preflight="$(mysql_stdin < "$PREFLIGHT")"
if ! printf '%s\n' "$mixed_attachment_preflight" |
  grep -q '^attachment_parsed_column_shapes'$'\tFAIL\t'; then
  echo "ERROR: mixed attachment schema unexpectedly passed preflight" >&2
  exit 1
fi
mysql_query "ALTER TABLE agent_attachment MODIFY parsed_page_count BIGINT NULL DEFAULT 0;"

# Wrong table/index/FK/config contracts and duplicate notification pairs must fail before apply.
mysql_query "DROP TABLE document;"
mysql_query "CREATE TABLE document (id INT NOT NULL PRIMARY KEY) ENGINE=InnoDB;"
mysql_query "ALTER TABLE announcement_read ADD INDEX idx_annread_announcement (announcement_id), DROP INDEX uk_annread;"
mysql_query "CREATE INDEX uk_annread ON announcement_read (user_id, announcement_id);"
mysql_query "ALTER TABLE survey_response ADD INDEX idx_sr_user (user_id), DROP INDEX uk_sr;"
mysql_query "ALTER TABLE survey_answer DROP FOREIGN KEY fk_sa_question, MODIFY question_id BIGINT NOT NULL;"
mysql_query "ALTER TABLE ai_service DROP INDEX model_key;"
mysql_query "ALTER TABLE ai_service ADD UNIQUE INDEX model_key_prefix_only (model_key(10));"
mysql_query "
INSERT INTO announcement (id, title, content, created_by)
VALUES (1, 'synthetic', 'synthetic', 101);
INSERT INTO announcement_read (announcement_id, user_id, read_at)
VALUES (1, 101, NOW()), (1, 101, NOW());
INSERT INTO survey_response (announcement_id, user_id, submitted_at)
VALUES (1, 101, NOW()), (1, 101, NOW());
"
negative_preflight="$(mysql_stdin < "$PREFLIGHT")"
if ! printf '%s\n' "$negative_preflight" | grep -q $'\tFAIL\t'; then
  echo "ERROR: invalid partial schema unexpectedly passed preflight" >&2
  exit 1
fi
for expected_failure in \
  document_schema_contract \
  ai_service_model_key_unique_contract \
  fk_sa_question_column_compatibility \
  uk_annread_contract \
  duplicate_announcement_read_user_pair \
  duplicate_survey_response_user_pair; do
  if ! printf '%s\n' "$negative_preflight" | grep -q "^${expected_failure}"$'\tFAIL\t'; then
    echo "ERROR: preflight missed expected failure: $expected_failure" >&2
    exit 1
  fi
done
if [ "$(mysql_query "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='document';")" != "1" ]; then
  echo "ERROR: read-only preflight changed the invalid document table" >&2
  exit 1
fi

echo "PASS: MySQL 8 exact, partial, negative-preflight, double-apply, constraints, and protected-data checks"

-- Read-only verification after 20260730_120000_prod_schema_reconcile.sql.
-- Output columns: check_name, status, observed, expected.

SET SESSION group_concat_max_len = 16777216;

SELECT
  'subscription_plan_type_shape' AS check_name,
  IF(
    COUNT(*) = 1
    AND MAX(COLUMN_TYPE) = 'varchar(20)'
    AND MAX(IS_NULLABLE) = 'NO'
    AND MAX(COLUMN_DEFAULT) = 'monthly',
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)) AS observed,
  '1/varchar(20)/NO/monthly' AS expected
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'plan_type'
UNION ALL
SELECT
  'subscription_cycle_credits_shape',
  IF(
    COUNT(*) = 1
    AND MAX(COLUMN_TYPE) = 'int'
    AND MAX(IS_NULLABLE) = 'NO'
    AND MAX(COLUMN_DEFAULT) = '2000',
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)),
  '1/int/NO/2000'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'cycle_credits'
UNION ALL
SELECT
  'subscription_historical_metadata',
  IF(SUM(plan_type <> 'monthly' OR cycle_credits <> 2000) = 0, 'PASS', 'FAIL'),
  CONCAT('rows=', COUNT(*), ',unexpected=', SUM(plan_type <> 'monthly' OR cycle_credits <> 2000)),
  'all pre-rollout rows monthly/2000'
FROM subscription
UNION ALL
SELECT
  'attachment_parsed_column_shapes',
  IF(
    COUNT(*) = 5
      AND (
        COALESCE(SUM(
          CASE COLUMN_NAME
            WHEN 'parsed_content' THEN COLUMN_TYPE = 'longtext' AND IS_NULLABLE = 'YES'
            WHEN 'parsed_content_sha256' THEN
              COLUMN_TYPE = 'varchar(71)' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = ''
            WHEN 'parsed_content_byte_size' THEN
              COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = '0'
            WHEN 'parsed_page_count' THEN
              COLUMN_TYPE = 'int' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = '0'
            WHEN 'parsed_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
            ELSE FALSE
          END
        ), 0) = 5
        OR COALESCE(SUM(
          CASE COLUMN_NAME
            WHEN 'parsed_content' THEN COLUMN_TYPE = 'longtext' AND IS_NULLABLE = 'YES'
            WHEN 'parsed_content_sha256' THEN
              COLUMN_TYPE = 'varchar(71)' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT IS NULL
            WHEN 'parsed_content_byte_size' THEN
              COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT = '0'
            WHEN 'parsed_page_count' THEN
              COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT = '0'
            WHEN 'parsed_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
            ELSE FALSE
          END
        ), 0) = 5
      ),
    'PASS',
    'FAIL'
  ),
  CASE
    WHEN COUNT(*) = 5 AND COALESCE(SUM(
        CASE COLUMN_NAME
          WHEN 'parsed_content' THEN COLUMN_TYPE = 'longtext' AND IS_NULLABLE = 'YES'
          WHEN 'parsed_content_sha256' THEN
            COLUMN_TYPE = 'varchar(71)' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = ''
          WHEN 'parsed_content_byte_size' THEN
            COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = '0'
          WHEN 'parsed_page_count' THEN
            COLUMN_TYPE = 'int' AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT = '0'
          WHEN 'parsed_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
          ELSE FALSE
        END
      ), 0) = 5 THEN 'final_complete'
    WHEN COUNT(*) = 5 AND COALESCE(SUM(
      CASE COLUMN_NAME
        WHEN 'parsed_content' THEN COLUMN_TYPE = 'longtext' AND IS_NULLABLE = 'YES'
        WHEN 'parsed_content_sha256' THEN
          COLUMN_TYPE = 'varchar(71)' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT IS NULL
        WHEN 'parsed_content_byte_size' THEN
          COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT = '0'
        WHEN 'parsed_page_count' THEN
          COLUMN_TYPE = 'bigint' AND IS_NULLABLE = 'YES' AND COLUMN_DEFAULT = '0'
        WHEN 'parsed_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
        ELSE FALSE
      END
    ), 0) = 5 THEN 'legacy_complete'
    ELSE 'incompatible'
  END,
  'final_complete or legacy_complete'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_attachment'
  AND COLUMN_NAME IN (
    'parsed_content', 'parsed_content_sha256', 'parsed_content_byte_size',
    'parsed_page_count', 'parsed_at'
  )
UNION ALL
SELECT
  'attachment_page_count_shape',
  IF(
    COUNT(*) = 1
      AND MAX(COLUMN_DEFAULT) = '0'
      AND (
        (MAX(COLUMN_TYPE) = 'int' AND MAX(IS_NULLABLE) = 'NO')
        OR (MAX(COLUMN_TYPE) = 'bigint' AND MAX(IS_NULLABLE) = 'YES')
      ),
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)),
  '1/int/NO/0 or 1/bigint/YES/0'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_attachment'
  AND COLUMN_NAME = 'parsed_page_count'
UNION ALL
SELECT
  'document_table_required_columns',
  IF(COUNT(*) = 11, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '11'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'document'
  AND COLUMN_NAME IN (
    'id', 'user_id', 'parent_user_id', 'source_object_key', 'source_run_id',
    'source_mime', 'title', 'content_md', 'parse_method', 'created_at', 'updated_at'
  )
UNION ALL
SELECT
  'document_user_id_shape',
  IF(COLUMN_TYPE = 'bigint unsigned', 'PASS', 'FAIL'),
  COLUMN_TYPE,
  'bigint unsigned'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'document' AND COLUMN_NAME = 'user_id'
UNION ALL
SELECT
  'feishu_table_count',
  IF(COUNT(*) = 6, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '6'
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME IN (
    'user_third_party_account', 'feishu_cli_vault', 'feishu_auth_session',
    'feishu_operation', 'feishu_operation_proof_consumption',
    'feishu_operation_execution_gate'
  )
UNION ALL
SELECT
  'feishu_required_column_count',
  IF(COUNT(*) = 83, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '83'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND (
    (TABLE_NAME = 'user_third_party_account' AND COLUMN_NAME IN (
      'id', 'user_id', 'provider', 'app_id', 'app_secret_enc', 'access_token_enc',
      'refresh_token_enc', 'token_expires_at', 'scopes', 'connected', 'connected_at',
      'connection_state', 'lark_cli_version', 'granted_scopes_json',
      'capability_state_json', 'last_success_at', 'last_error_code', 'generation',
      'created_at', 'updated_at'
    ))
    OR (TABLE_NAME = 'feishu_cli_vault' AND COLUMN_NAME IN (
      'user_id', 'generation', 'ciphertext', 'key_version', 'checksum', 'revision',
      'created_at', 'updated_at'
    ))
    OR (TABLE_NAME = 'feishu_auth_session' AND COLUMN_NAME IN (
      'id', 'user_id', 'generation', 'operation_id', 'phase', 'requested_scopes_json',
      'state', 'lease_owner', 'lease_until', 'expires_at', 'protocol_version',
      'resume_credential_ciphertext', 'resume_key_version', 'resume_expires_at',
      'scope_hash', 'created_at', 'updated_at', 'completed_at'
    ))
    OR (TABLE_NAME = 'feishu_operation' AND COLUMN_NAME IN (
      'id', 'user_id', 'generation', 'agent_run_id', 'tool_call_id', 'idempotency_key',
      'command_path', 'domain', 'risk_level', 'request_ciphertext', 'key_version',
      'request_fingerprint', 'state', 'attempt_count', 'lease_owner', 'lease_until',
      'error_type', 'error_subtype', 'error_code', 'result_ciphertext',
      'result_summary_json', 'created_at', 'started_at', 'updated_at', 'finished_at'
    ))
    OR (TABLE_NAME = 'feishu_operation_proof_consumption' AND COLUMN_NAME IN (
      'source_operation_id', 'consumer_operation_id', 'user_id', 'generation',
      'agent_run_id', 'created_at'
    ))
    OR (TABLE_NAME = 'feishu_operation_execution_gate' AND COLUMN_NAME IN (
      'user_id', 'generation', 'lease_owner', 'operation_id', 'lease_until', 'updated_at'
    ))
  )
UNION ALL
SELECT
  'new_product_tables_existing_row_count',
  'PASS',
  CAST(SUM(row_count) AS CHAR),
  'informational; existing rows are preserved'
FROM (
  SELECT COUNT(*) AS row_count FROM document
  UNION ALL SELECT COUNT(*) FROM user_third_party_account
  UNION ALL SELECT COUNT(*) FROM feishu_cli_vault
  UNION ALL SELECT COUNT(*) FROM feishu_auth_session
  UNION ALL SELECT COUNT(*) FROM feishu_operation
  UNION ALL SELECT COUNT(*) FROM feishu_operation_proof_consumption
  UNION ALL SELECT COUNT(*) FROM feishu_operation_execution_gate
) new_tables
UNION ALL
SELECT
  'notification_constraint_count',
  IF(COUNT(*) = 9, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '9 (7 foreign keys + 2 unique keys)'
FROM information_schema.TABLE_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE()
  AND CONSTRAINT_NAME IN (
    'fk_annread_announcement', 'fk_annread_user', 'fk_sq_announcement',
    'fk_sr_announcement', 'fk_sr_user', 'fk_sa_response', 'fk_sa_question',
    'uk_annread', 'uk_sr'
  )
UNION ALL
SELECT
  'agent_pending_external_column_shapes',
  IF(
    COUNT(*) = 2
      AND COALESCE(SUM(
        CASE COLUMN_NAME
          WHEN 'pending_external_action_json' THEN COLUMN_TYPE = 'json' AND IS_NULLABLE = 'YES'
          WHEN 'pending_external_action_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
          ELSE FALSE
        END
      ), 0) = 2,
    'PASS',
    'FAIL'
  ),
  CONCAT('present=', COUNT(*), ',exact=', COALESCE(SUM(
    CASE COLUMN_NAME
      WHEN 'pending_external_action_json' THEN COLUMN_TYPE = 'json' AND IS_NULLABLE = 'YES'
      WHEN 'pending_external_action_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
      ELSE FALSE
    END
  ), 0)),
  'two exact external-action columns'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_run'
  AND COLUMN_NAME IN ('pending_external_action_json', 'pending_external_action_at')
UNION ALL
SELECT
  'agent_pending_index',
  IF(COUNT(*) = 2, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '2 indexed columns'
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_run'
  AND INDEX_NAME = 'idx_ar_state_pending'
UNION ALL
SELECT
  'agent_state_reason_rows',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 unsupported state_reason relationships'
FROM agent_run
WHERE NOT (
  state_reason IS NULL
  OR (state_reason = '' AND status = 'running')
  OR state_reason IN (
      'completed', 'blocking_limit', 'image_error', 'model_error',
      'aborted_streaming', 'prompt_too_long', 'stop_hook_prevented',
      'aborted_tools', 'hook_stopped', 'max_turns', 'error_max_budget',
      'error_max_retries', 'next_turn', 'collapse_drain_retry',
      'reactive_compact_retry', 'max_output_escalate', 'max_output_recovery',
      'stop_hook_blocking', 'token_budget_continue', 'running',
      'waiting_for_user_choice', 'permission_denied', 'context_exhausted',
      'cancelled', 'external_resume_ready'
  )
  OR (state_reason = 'zombie_cleanup_2026_05_28' AND is_deleted = 1)
  OR LEFT(state_reason, 11) = 'ext_resume:'
)
UNION ALL
SELECT
  'agent_state_reason_constraint',
  IF(
    COUNT(*) = 1
      AND MAX(
        SHA2(
          LOWER(
            REGEXP_REPLACE(
              REPLACE(
                REPLACE(
                  REPLACE(checks_meta.CHECK_CLAUSE, '_utf8mb4', ''),
                  '_utf8mb3',
                  ''
                ),
                '_latin1',
                ''
              ),
              '[[:space:]]+',
              ''
            )
          ),
          256
        )
      ) = '7bac0f04b3cf2225cdd40a61fe086c4d1ed982bb3b082e96341818040878d100',
    'PASS',
    'FAIL'
  ),
  CONCAT(
    'count=', COUNT(*),
    ',sha=',
    COALESCE(
      MAX(
        SHA2(
          LOWER(
            REGEXP_REPLACE(
              REPLACE(
                REPLACE(
                  REPLACE(checks_meta.CHECK_CLAUSE, '_utf8mb4', ''),
                  '_utf8mb3',
                  ''
                ),
                '_latin1',
                ''
              ),
              '[[:space:]]+',
              ''
            )
          ),
          256
        )
      ),
      'absent'
    )
  ),
  'one exact relational state constraint (sha 7bac0f04...)'
FROM information_schema.TABLE_CONSTRAINTS constraints_meta
JOIN information_schema.CHECK_CONSTRAINTS checks_meta
  ON checks_meta.CONSTRAINT_SCHEMA = constraints_meta.CONSTRAINT_SCHEMA
 AND checks_meta.CONSTRAINT_NAME = constraints_meta.CONSTRAINT_NAME
WHERE constraints_meta.CONSTRAINT_SCHEMA = DATABASE()
  AND constraints_meta.TABLE_NAME = 'agent_run'
  AND constraints_meta.CONSTRAINT_NAME = 'chk_ar_state_reason'
UNION ALL
SELECT
  'qwen35_active_service_count',
  IF(COUNT(*) = 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '1'
FROM ai_service
WHERE model_key = 'qwen3.5-flash' AND is_active = 1 AND deprecated_at IS NULL
UNION ALL
SELECT
  'qwen35_active_ali_route_count',
  IF(COUNT(*) = 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '1'
FROM ai_service service
JOIN ai_service_route route ON route.model_id = service.id
JOIN llm_provider provider ON provider.id = route.provider_id
WHERE service.model_key = 'qwen3.5-flash'
  AND provider.name = 'ali-dashscope'
  AND route.provider_model_id = 'qwen3.5-flash'
  AND route.is_active = 1
UNION ALL
SELECT
  'attachment_vision_task_route',
  IF(COUNT(*) = 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '1'
FROM task_profile profile
JOIN ai_service service ON service.id = profile.default_service_id
WHERE profile.task_id = 'attachment.vision_describe'
  AND service.model_key = 'qwen3.5-flash'
UNION ALL
SELECT
  'qwen35_active_pricing_count',
  IF(COUNT(*) = 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '1'
FROM pricing_rule
WHERE service_type = 'llm_vision'
  AND provider = 'ali-dashscope'
  AND model = 'qwen3.5-flash'
  AND is_active = 1
UNION ALL
SELECT
  'old_qwen_ali_route_active_count',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM ai_service service
JOIN ai_service_route route ON route.model_id = service.id
JOIN llm_provider provider ON provider.id = route.provider_id
WHERE service.model_key = 'qwen3-vl-flash'
  AND provider.name = 'ali-dashscope'
  AND route.is_active = 1
UNION ALL
SELECT
  'skill_template_rows',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM skill_template
UNION ALL
SELECT
  'official_example_skill_rows',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM skill
WHERE visibility = 'official'
  AND parent_user_id = 0
  AND owner_user_id = 0
  AND name = '官方示例技能'
  AND source_type = 'custom';

WITH expected AS (
  SELECT 'document' AS table_name, 'ac58e234470d95c46cbefe91cb49a4ea7cdcac1c9391242884638839cadbf112' AS contract_sha
  UNION ALL SELECT 'user_third_party_account', '11e886c79dd940c542976e0429f8626557f8fa7866e46cae7082b857ae08c855'
  UNION ALL SELECT 'feishu_cli_vault', '5c0e5a61fdb941a74e6f0565f95ca3657dd012094d76b4d07f8f4665f45fed8b'
  UNION ALL SELECT 'feishu_auth_session', '1c35a02a83342357259d4756ee47cb3dc34e7a0716b33521ccdee9c30ad56aad'
  UNION ALL SELECT 'feishu_operation', 'd4a26b6f540244802bde403ad6533933485985f5b3ee6d1ff24e651ad96045bf'
  UNION ALL SELECT 'feishu_operation_proof_consumption', 'ef5cee4f4c3c8276369137508da748513269b148dd994368733c44c0a16a20fe'
  UNION ALL SELECT 'feishu_operation_execution_gate', '69484e7f811455d26e9db05f3184ed7790188502d9b9d9375232f900af66f75b'
),
actual AS (
  SELECT
    tables_meta.TABLE_NAME AS table_name,
    SHA2(
      CONCAT_WS(
        '|',
        tables_meta.ENGINE,
        tables_meta.TABLE_COLLATION,
        tables_meta.ROW_FORMAT,
        (
          SELECT SHA2(
            GROUP_CONCAT(
              CONCAT_WS(
                '|', column_meta.COLUMN_NAME,
                column_meta.COLUMN_TYPE, column_meta.IS_NULLABLE,
                COALESCE(column_meta.COLUMN_DEFAULT, '<NULL>'), column_meta.EXTRA,
                COALESCE(column_meta.CHARACTER_SET_NAME, '<NULL>'),
                COALESCE(column_meta.COLLATION_NAME, '<NULL>')
              )
              ORDER BY column_meta.COLUMN_NAME SEPARATOR '\n'
            ),
            256
          )
          FROM information_schema.COLUMNS column_meta
          WHERE column_meta.TABLE_SCHEMA = DATABASE()
            AND column_meta.TABLE_NAME = tables_meta.TABLE_NAME
        ),
        (
          SELECT SHA2(
            GROUP_CONCAT(
              CONCAT_WS(
                '|', index_meta.INDEX_NAME, index_meta.NON_UNIQUE,
                index_meta.SEQ_IN_INDEX, COALESCE(index_meta.COLUMN_NAME, '<NULL>'),
                COALESCE(index_meta.SUB_PART, '<NULL>'), index_meta.INDEX_TYPE,
                COALESCE(index_meta.COLLATION, '<NULL>'),
                index_meta.IS_VISIBLE,
                COALESCE(index_meta.EXPRESSION, '<NULL>')
              )
              ORDER BY index_meta.INDEX_NAME, index_meta.SEQ_IN_INDEX SEPARATOR '\n'
            ),
            256
          )
          FROM information_schema.STATISTICS index_meta
          WHERE index_meta.TABLE_SCHEMA = DATABASE()
            AND index_meta.TABLE_NAME = tables_meta.TABLE_NAME
        )
      ),
      256
    ) AS contract_sha
  FROM information_schema.TABLES tables_meta
  WHERE tables_meta.TABLE_SCHEMA = DATABASE()
    AND tables_meta.TABLE_NAME IN (
      'document', 'user_third_party_account', 'feishu_cli_vault',
      'feishu_auth_session', 'feishu_operation',
      'feishu_operation_proof_consumption', 'feishu_operation_execution_gate'
    )
)
SELECT
  CONCAT(expected.table_name, '_schema_contract') AS check_name,
  IF(actual.contract_sha = expected.contract_sha, 'PASS', 'FAIL') AS status,
  COALESCE(actual.contract_sha, 'absent') AS observed,
  expected.contract_sha AS expected
FROM expected
LEFT JOIN actual ON actual.table_name = expected.table_name
ORDER BY expected.table_name;

SELECT
  'ai_service_model_key_unique_contract' AS check_name,
  IF(COUNT(*) >= 1, 'PASS', 'FAIL') AS status,
  CAST(COUNT(*) AS CHAR) AS observed,
  'at least one exact UNIQUE(model_key)' AS expected
FROM (
  SELECT INDEX_NAME
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service'
  GROUP BY INDEX_NAME
  HAVING MAX(NON_UNIQUE) = 0
     AND COUNT(*) = 1
     AND SUM(SUB_PART IS NULL) = COUNT(*)
     AND SUM(EXPRESSION IS NULL) = COUNT(*)
     AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'model_key'
) exact_indexes
UNION ALL
SELECT
  'ai_service_route_model_provider_unique_contract',
  IF(COUNT(*) >= 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  'at least one exact UNIQUE(model_id,provider_id)'
FROM (
  SELECT INDEX_NAME
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route'
  GROUP BY INDEX_NAME
  HAVING MAX(NON_UNIQUE) = 0
     AND COUNT(*) = 2
     AND SUM(SUB_PART IS NULL) = COUNT(*)
     AND SUM(EXPRESSION IS NULL) = COUNT(*)
     AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'model_id,provider_id'
) exact_indexes
UNION ALL
SELECT
  'task_profile_task_id_unique_contract',
  IF(COUNT(*) >= 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  'at least one exact UNIQUE(task_id)'
FROM (
  SELECT INDEX_NAME
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task_profile'
  GROUP BY INDEX_NAME
  HAVING MAX(NON_UNIQUE) = 0
     AND COUNT(*) = 1
     AND SUM(SUB_PART IS NULL) = COUNT(*)
     AND SUM(EXPRESSION IS NULL) = COUNT(*)
     AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'task_id'
) exact_indexes
UNION ALL
SELECT
  'pricing_rule_lookup_unique_contract',
  IF(COUNT(*) >= 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  'at least one exact UNIQUE(service_type,provider,model)'
FROM (
  SELECT INDEX_NAME
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
  GROUP BY INDEX_NAME
  HAVING MAX(NON_UNIQUE) = 0
     AND COUNT(*) = 3
     AND SUM(SUB_PART IS NULL) = COUNT(*)
     AND SUM(EXPRESSION IS NULL) = COUNT(*)
     AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') =
         'service_type,provider,model'
) exact_indexes;

SELECT
  'feishu_proof_fk_contract' AS check_name,
  IF(
    COUNT(*) = 2
      AND GROUP_CONCAT(
        CONCAT_WS(
          '|', constraints_meta.CONSTRAINT_NAME, key_meta.COLUMN_NAME,
          key_meta.REFERENCED_TABLE_NAME, key_meta.REFERENCED_COLUMN_NAME,
          ref_meta.DELETE_RULE, ref_meta.UPDATE_RULE
        )
        ORDER BY constraints_meta.CONSTRAINT_NAME SEPARATOR '\n'
      ) = CONCAT(
        'fk_feishu_proof_consumer_operation|consumer_operation_id|feishu_operation|id|RESTRICT|NO ACTION',
        '\n',
        'fk_feishu_proof_source_operation|source_operation_id|feishu_operation|id|RESTRICT|NO ACTION'
      ),
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT('constraints=', COUNT(*)) AS observed,
  'exact two restrictive operation FKs' AS expected
FROM information_schema.TABLE_CONSTRAINTS constraints_meta
JOIN information_schema.KEY_COLUMN_USAGE key_meta
  ON key_meta.CONSTRAINT_SCHEMA = constraints_meta.CONSTRAINT_SCHEMA
 AND key_meta.TABLE_NAME = constraints_meta.TABLE_NAME
 AND key_meta.CONSTRAINT_NAME = constraints_meta.CONSTRAINT_NAME
JOIN information_schema.REFERENTIAL_CONSTRAINTS ref_meta
  ON ref_meta.CONSTRAINT_SCHEMA = constraints_meta.CONSTRAINT_SCHEMA
 AND ref_meta.CONSTRAINT_NAME = constraints_meta.CONSTRAINT_NAME
WHERE constraints_meta.CONSTRAINT_SCHEMA = DATABASE()
  AND constraints_meta.TABLE_NAME = 'feishu_operation_proof_consumption'
  AND constraints_meta.CONSTRAINT_TYPE = 'FOREIGN KEY';

SELECT
  'feishu_proof_column_compatibility' AS check_name,
  IF(COUNT(*) = 2, 'PASS', 'FAIL') AS status,
  CAST(COUNT(*) AS CHAR) AS observed,
  '2 exact CHAR(36) operation references' AS expected
FROM information_schema.COLUMNS proof_column
JOIN information_schema.COLUMNS operation_column
  ON operation_column.TABLE_SCHEMA = proof_column.TABLE_SCHEMA
 AND operation_column.TABLE_NAME = 'feishu_operation'
 AND operation_column.COLUMN_NAME = 'id'
WHERE proof_column.TABLE_SCHEMA = DATABASE()
  AND proof_column.TABLE_NAME = 'feishu_operation_proof_consumption'
  AND proof_column.COLUMN_NAME IN ('source_operation_id', 'consumer_operation_id')
  AND proof_column.COLUMN_TYPE = operation_column.COLUMN_TYPE
  AND proof_column.CHARACTER_SET_NAME <=> operation_column.CHARACTER_SET_NAME
  AND proof_column.COLLATION_NAME <=> operation_column.COLLATION_NAME
UNION ALL
SELECT
  'feishu_proof_source_orphans',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM feishu_operation_proof_consumption proof
LEFT JOIN feishu_operation operation_row
  ON operation_row.id = proof.source_operation_id
WHERE operation_row.id IS NULL
UNION ALL
SELECT
  'feishu_proof_consumer_orphans',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM feishu_operation_proof_consumption proof
LEFT JOIN feishu_operation operation_row
  ON operation_row.id = proof.consumer_operation_id
WHERE operation_row.id IS NULL;

WITH expected AS (
  SELECT 'announcement_read' AS table_name, 'fk_annread_announcement' AS constraint_name,
         'announcement_id' AS column_name, 'announcement' AS referenced_table_name,
         'id' AS referenced_column_name, 'CASCADE' AS delete_rule, 'NO ACTION' AS update_rule
  UNION ALL SELECT 'announcement_read', 'fk_annread_user', 'user_id', 'user', 'id', 'CASCADE', 'NO ACTION'
  UNION ALL SELECT 'survey_question', 'fk_sq_announcement', 'announcement_id', 'announcement', 'id', 'CASCADE', 'NO ACTION'
  UNION ALL SELECT 'survey_response', 'fk_sr_announcement', 'announcement_id', 'announcement', 'id', 'CASCADE', 'NO ACTION'
  UNION ALL SELECT 'survey_response', 'fk_sr_user', 'user_id', 'user', 'id', 'CASCADE', 'NO ACTION'
  UNION ALL SELECT 'survey_answer', 'fk_sa_response', 'response_id', 'survey_response', 'id', 'CASCADE', 'NO ACTION'
  UNION ALL SELECT 'survey_answer', 'fk_sa_question', 'question_id', 'survey_question', 'id', 'CASCADE', 'NO ACTION'
),
actual AS (
  SELECT
    constraints_meta.TABLE_NAME AS table_name,
    constraints_meta.CONSTRAINT_NAME AS constraint_name,
    key_meta.COLUMN_NAME AS column_name,
    key_meta.REFERENCED_TABLE_NAME AS referenced_table_name,
    key_meta.REFERENCED_COLUMN_NAME AS referenced_column_name,
    ref_meta.DELETE_RULE AS delete_rule,
    ref_meta.UPDATE_RULE AS update_rule
  FROM information_schema.TABLE_CONSTRAINTS constraints_meta
  JOIN information_schema.KEY_COLUMN_USAGE key_meta
    ON key_meta.CONSTRAINT_SCHEMA = constraints_meta.CONSTRAINT_SCHEMA
   AND key_meta.TABLE_NAME = constraints_meta.TABLE_NAME
   AND key_meta.CONSTRAINT_NAME = constraints_meta.CONSTRAINT_NAME
  JOIN information_schema.REFERENTIAL_CONSTRAINTS ref_meta
    ON ref_meta.CONSTRAINT_SCHEMA = constraints_meta.CONSTRAINT_SCHEMA
   AND ref_meta.CONSTRAINT_NAME = constraints_meta.CONSTRAINT_NAME
  WHERE constraints_meta.CONSTRAINT_SCHEMA = DATABASE()
    AND constraints_meta.CONSTRAINT_NAME IN (
      'fk_annread_announcement', 'fk_annread_user', 'fk_sq_announcement',
      'fk_sr_announcement', 'fk_sr_user', 'fk_sa_response', 'fk_sa_question'
    )
)
SELECT
  CONCAT(expected.constraint_name, '_contract') AS check_name,
  IF(
    actual.table_name = expected.table_name
      AND actual.column_name = expected.column_name
      AND actual.referenced_table_name = expected.referenced_table_name
      AND actual.referenced_column_name = expected.referenced_column_name
      AND actual.delete_rule = expected.delete_rule
      AND actual.update_rule = expected.update_rule,
    'PASS',
    'FAIL'
  ) AS status,
  IF(actual.constraint_name IS NULL, 'absent', CONCAT_WS(
    '|', actual.table_name, actual.column_name, actual.referenced_table_name,
    actual.referenced_column_name, actual.delete_rule, actual.update_rule
  )) AS observed,
  CONCAT_WS(
    '|', expected.table_name, expected.column_name, expected.referenced_table_name,
    expected.referenced_column_name, expected.delete_rule, expected.update_rule
  ) AS expected
FROM expected
LEFT JOIN actual ON actual.constraint_name = expected.constraint_name
ORDER BY expected.constraint_name;

SELECT
  'uk_annread_contract' AS check_name,
  IF(
    COUNT(*) = 2
      AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'announcement_id,user_id'
      AND MAX(NON_UNIQUE) = 0,
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT_WS('/', COUNT(*), GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), MAX(NON_UNIQUE)) AS observed,
  '2/announcement_id,user_id/0' AS expected
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND INDEX_NAME = 'uk_annread'
UNION ALL
SELECT
  'uk_sr_contract',
  IF(
    COUNT(*) = 2
      AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'announcement_id,user_id'
      AND MAX(NON_UNIQUE) = 0,
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), MAX(NON_UNIQUE)),
  '2/announcement_id,user_id/0'
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response' AND INDEX_NAME = 'uk_sr';

SELECT
  'subscription_protected_projection' AS evidence_name,
  COUNT(*) AS row_count,
  SHA2(
    GROUP_CONCAT(
      CONCAT_WS(
        '|',
        id,
        user_id,
        DATE_FORMAT(first_started_at, '%Y-%m-%d %H:%i:%s'),
        DATE_FORMAT(current_started_at, '%Y-%m-%d %H:%i:%s'),
        DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%s'),
        total_months_purchased,
        source,
        IFNULL(granter_user_id, '<NULL>'),
        DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s'),
        DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s')
      )
      ORDER BY id SEPARATOR '\n'
    ),
    256
  ) AS sha256
FROM subscription;

SELECT
  'agent_attachment_protected_projection' AS evidence_name,
  COUNT(*) AS row_count,
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
  ) AS sha256
FROM agent_attachment
UNION ALL
SELECT
  'agent_run_protected_projection',
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
FROM agent_run;

SELECT
  'agent_attachment_complete_projection' AS evidence_name,
  COUNT(*) AS row_count,
  COALESCE(
    SHA2(
      GROUP_CONCAT(
        SHA2(CONCAT_WS(
          '|',
          CONCAT('id=V:', OCTET_LENGTH(CAST(id AS BINARY)), ':', SHA2(CAST(id AS BINARY), 256)),
          CONCAT('user_id=V:', OCTET_LENGTH(CAST(user_id AS BINARY)), ':', SHA2(CAST(user_id AS BINARY), 256)),
          IF(url IS NULL, 'url=N', CONCAT('url=V:', OCTET_LENGTH(CAST(url AS BINARY)), ':', SHA2(CAST(url AS BINARY), 256))),
          IF(filename IS NULL, 'filename=N', CONCAT('filename=V:', OCTET_LENGTH(CAST(filename AS BINARY)), ':', SHA2(CAST(filename AS BINARY), 256))),
          IF(mime_type IS NULL, 'mime_type=N', CONCAT('mime_type=V:', OCTET_LENGTH(CAST(mime_type AS BINARY)), ':', SHA2(CAST(mime_type AS BINARY), 256))),
          IF(size IS NULL, 'size=N', CONCAT('size=V:', OCTET_LENGTH(CAST(size AS BINARY)), ':', SHA2(CAST(size AS BINARY), 256))),
          IF(modality IS NULL, 'modality=N', CONCAT('modality=V:', OCTET_LENGTH(CAST(modality AS BINARY)), ':', SHA2(CAST(modality AS BINARY), 256))),
          IF(width IS NULL, 'width=N', CONCAT('width=V:', OCTET_LENGTH(CAST(width AS BINARY)), ':', SHA2(CAST(width AS BINARY), 256))),
          IF(height IS NULL, 'height=N', CONCAT('height=V:', OCTET_LENGTH(CAST(height AS BINARY)), ':', SHA2(CAST(height AS BINARY), 256))),
          IF(ocr_text IS NULL, 'ocr_text=N', CONCAT('ocr_text=V:', OCTET_LENGTH(CAST(ocr_text AS BINARY)), ':', SHA2(CAST(ocr_text AS BINARY), 256))),
          IF(vision_description IS NULL, 'vision_description=N', CONCAT('vision_description=V:', OCTET_LENGTH(CAST(vision_description AS BINARY)), ':', SHA2(CAST(vision_description AS BINARY), 256))),
          IF(text_fallback IS NULL, 'text_fallback=N', CONCAT('text_fallback=V:', OCTET_LENGTH(CAST(text_fallback AS BINARY)), ':', SHA2(CAST(text_fallback AS BINARY), 256))),
          IF(fallback_ready IS NULL, 'fallback_ready=N', CONCAT('fallback_ready=V:', OCTET_LENGTH(CAST(fallback_ready AS BINARY)), ':', SHA2(CAST(fallback_ready AS BINARY), 256))),
          IF(fallback_error IS NULL, 'fallback_error=N', CONCAT('fallback_error=V:', OCTET_LENGTH(CAST(fallback_error AS BINARY)), ':', SHA2(CAST(fallback_error AS BINARY), 256))),
          IF(parsed_content IS NULL, 'parsed_content=N', CONCAT('parsed_content=V:', OCTET_LENGTH(CAST(parsed_content AS BINARY)), ':', SHA2(CAST(parsed_content AS BINARY), 256))),
          IF(parsed_content_sha256 IS NULL, 'parsed_content_sha256=N', CONCAT('parsed_content_sha256=V:', OCTET_LENGTH(CAST(parsed_content_sha256 AS BINARY)), ':', SHA2(CAST(parsed_content_sha256 AS BINARY), 256))),
          IF(parsed_content_byte_size IS NULL, 'parsed_content_byte_size=N', CONCAT('parsed_content_byte_size=V:', OCTET_LENGTH(CAST(parsed_content_byte_size AS BINARY)), ':', SHA2(CAST(parsed_content_byte_size AS BINARY), 256))),
          IF(parsed_page_count IS NULL, 'parsed_page_count=N', CONCAT('parsed_page_count=V:', OCTET_LENGTH(CAST(parsed_page_count AS BINARY)), ':', SHA2(CAST(parsed_page_count AS BINARY), 256))),
          IF(parsed_at IS NULL, 'parsed_at=N', CONCAT('parsed_at=V:', OCTET_LENGTH(CAST(parsed_at AS BINARY)), ':', SHA2(CAST(parsed_at AS BINARY), 256))),
          IF(fallback_started_at IS NULL, 'fallback_started_at=N', CONCAT('fallback_started_at=V:', OCTET_LENGTH(CAST(fallback_started_at AS BINARY)), ':', SHA2(CAST(fallback_started_at AS BINARY), 256))),
          IF(fallback_completed_at IS NULL, 'fallback_completed_at=N', CONCAT('fallback_completed_at=V:', OCTET_LENGTH(CAST(fallback_completed_at AS BINARY)), ':', SHA2(CAST(fallback_completed_at AS BINARY), 256))),
          IF(retry_count IS NULL, 'retry_count=N', CONCAT('retry_count=V:', OCTET_LENGTH(CAST(retry_count AS BINARY)), ':', SHA2(CAST(retry_count AS BINARY), 256))),
          IF(created_at IS NULL, 'created_at=N', CONCAT('created_at=V:', OCTET_LENGTH(CAST(created_at AS BINARY)), ':', SHA2(CAST(created_at AS BINARY), 256)))
        ), 256)
        ORDER BY id SEPARATOR '\n'
      ),
      256
    ),
    SHA2('', 256)
  ) AS sha256
FROM agent_attachment;

SELECT
  'feishu_proof_business_projection' AS evidence_name,
  COUNT(*) AS row_count,
  COALESCE(
    SHA2(
      GROUP_CONCAT(
        SHA2(CONCAT_WS(
          '|',
          CONCAT('source_operation_id=V:', OCTET_LENGTH(CAST(source_operation_id AS BINARY)), ':', SHA2(CAST(source_operation_id AS BINARY), 256)),
          CONCAT('consumer_operation_id=V:', OCTET_LENGTH(CAST(consumer_operation_id AS BINARY)), ':', SHA2(CAST(consumer_operation_id AS BINARY), 256)),
          CONCAT('user_id=V:', OCTET_LENGTH(CAST(user_id AS BINARY)), ':', SHA2(CAST(user_id AS BINARY), 256)),
          CONCAT('generation=V:', OCTET_LENGTH(CAST(generation AS BINARY)), ':', SHA2(CAST(generation AS BINARY), 256)),
          CONCAT('agent_run_id=V:', OCTET_LENGTH(CAST(agent_run_id AS BINARY)), ':', SHA2(CAST(agent_run_id AS BINARY), 256)),
          CONCAT('created_at=V:', OCTET_LENGTH(CAST(created_at AS BINARY)), ':', SHA2(CAST(created_at AS BINARY), 256))
        ), 256)
        ORDER BY source_operation_id SEPARATOR '\n'
      ),
      256
    ),
    SHA2('', 256)
  ) AS sha256
FROM feishu_operation_proof_consumption;

CHECKSUM TABLE
  `user`, `trial_grant`, `credit_account`, `credit_cycle`,
  `user_booster_balance`, `membership_event`,
  `credit_reservation`, `credit_reservation_item`, `credit_transaction`,
  `sop_run`, `sop_node_run`, `chatbot_session`, `chatbot_message`,
  `sales_session`, `sales_message`
EXTENDED;

SELECT 'user' AS table_name, COUNT(*) AS row_count FROM user
UNION ALL SELECT 'subscription', COUNT(*) FROM subscription
UNION ALL SELECT 'trial_grant', COUNT(*) FROM trial_grant
UNION ALL SELECT 'credit_account', COUNT(*) FROM credit_account
UNION ALL SELECT 'credit_cycle', COUNT(*) FROM credit_cycle
UNION ALL SELECT 'user_booster_balance', COUNT(*) FROM user_booster_balance
UNION ALL SELECT 'membership_event', COUNT(*) FROM membership_event
UNION ALL SELECT 'credit_reservation', COUNT(*) FROM credit_reservation
UNION ALL SELECT 'credit_reservation_item', COUNT(*) FROM credit_reservation_item
UNION ALL SELECT 'credit_transaction', COUNT(*) FROM credit_transaction
UNION ALL SELECT 'sop_run', COUNT(*) FROM sop_run
UNION ALL SELECT 'sop_node_run', COUNT(*) FROM sop_node_run
UNION ALL SELECT 'chatbot_session', COUNT(*) FROM chatbot_session
UNION ALL SELECT 'chatbot_message', COUNT(*) FROM chatbot_message
UNION ALL SELECT 'sales_session', COUNT(*) FROM sales_session
UNION ALL SELECT 'sales_message', COUNT(*) FROM sales_message
UNION ALL SELECT 'agent_run', COUNT(*) FROM agent_run;

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
  'attachment_parsed_column_count',
  IF(COUNT(*) = 5, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '5'
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
    COUNT(*) = 1 AND MAX(COLUMN_TYPE) = 'int'
      AND MAX(IS_NULLABLE) = 'NO' AND MAX(COLUMN_DEFAULT) = '0',
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)),
  '1/int/NO/0'
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
  'new_product_tables_initially_empty',
  IF(SUM(row_count) = 0, 'PASS', 'FAIL'),
  CAST(SUM(row_count) AS CHAR),
  '0 before customer traffic'
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

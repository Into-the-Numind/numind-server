-- Read-only preflight for the full Dev -> Prod schema reconcile.
-- Output columns: check_name, status, observed, expected.

SET SESSION group_concat_max_len = 16777216;

SELECT
  'mysql_major_version' AS check_name,
  IF(SUBSTRING_INDEX(VERSION(), '.', 1) = '8', 'PASS', 'FAIL') AS status,
  VERSION() AS observed,
  '8.x' AS expected
UNION ALL
SELECT
  'required_base_tables',
  IF(COUNT(*) = 17, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '17'
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME IN (
    'user', 'subscription', 'agent_attachment', 'agent_run',
    'announcement', 'announcement_read', 'survey_question',
    'survey_response', 'survey_answer', 'llm_provider', 'ai_service',
    'ai_service_route', 'task_profile', 'task_profile_service',
    'pricing_rule', 'skill', 'skill_template'
  )
UNION ALL
SELECT
  'user_id_type',
  IF(COLUMN_TYPE = 'bigint unsigned', 'PASS', 'FAIL'),
  COALESCE(COLUMN_TYPE, 'missing'),
  'bigint unsigned'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user' AND COLUMN_NAME = 'id'
UNION ALL
SELECT
  'ali_dashscope_provider_count',
  IF(COUNT(*) = 1, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '1'
FROM llm_provider
WHERE name = 'ali-dashscope'
UNION ALL
SELECT
  'subscription_plan_type_column_count',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 1 after apply'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'plan_type'
UNION ALL
SELECT
  'subscription_cycle_credits_column_count',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 1 after apply'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'cycle_credits'
UNION ALL
SELECT
  'attachment_parsed_column_count',
  IF(COUNT(*) IN (0, 5), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 5 after apply'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_attachment'
  AND COLUMN_NAME IN (
    'parsed_content', 'parsed_content_sha256', 'parsed_content_byte_size',
    'parsed_page_count', 'parsed_at'
  )
UNION ALL
SELECT
  'document_table_shape',
  IF(
    (SELECT COUNT(*) FROM information_schema.TABLES
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'document') = 0
    OR COUNT(*) = 11,
    'PASS',
    'FAIL'
  ),
  CONCAT(
    'table=',
    (SELECT COUNT(*) FROM information_schema.TABLES
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'document'),
    ',required_columns=',
    COUNT(*)
  ),
  'absent or 11 required columns'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'document'
  AND COLUMN_NAME IN (
    'id', 'user_id', 'parent_user_id', 'source_object_key', 'source_run_id',
    'source_mime', 'title', 'content_md', 'parse_method', 'created_at', 'updated_at'
  )
UNION ALL
SELECT
  'feishu_table_count',
  IF(COUNT(*) IN (0, 7), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 7 after apply'
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME IN (
    'user_third_party_account', 'feishu_cli_vault', 'feishu_auth_session',
    'feishu_operation', 'feishu_operation_proof_consumption',
    'feishu_operation_execution_gate', 'document'
  )
UNION ALL
SELECT
  'skill_template_rows',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM skill_template
UNION ALL
SELECT
  'tenant_official_template_import_rows',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM skill
WHERE visibility = 'official'
  AND parent_user_id <> 0
  AND source_type = 'imported_from_template'
UNION ALL
SELECT
  'official_example_skill_rows',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 or one exact project seed'
FROM skill
WHERE visibility = 'official'
  AND parent_user_id = 0
  AND owner_user_id = 0
  AND name = '官方示例技能'
  AND source_type = 'custom'
UNION ALL
SELECT
  'qwen35_service_key_count',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 1 after apply'
FROM ai_service
WHERE model_key = 'qwen3.5-flash'
UNION ALL
SELECT
  'attachment_vision_task_key_count',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 1 after apply'
FROM task_profile
WHERE task_id = 'attachment.vision_describe'
UNION ALL
SELECT
  'qwen35_pricing_key_count',
  IF(COUNT(*) IN (0, 1), 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 before apply or 1 after apply'
FROM pricing_rule
WHERE service_type = 'llm_vision'
  AND provider = 'ali-dashscope'
  AND model = 'qwen3.5-flash';

SELECT
  'orphan_announcement_read_announcement' AS check_name,
  IF(COUNT(*) = 0, 'PASS', 'FAIL') AS status,
  CAST(COUNT(*) AS CHAR) AS observed,
  '0' AS expected
FROM announcement_read child
LEFT JOIN announcement parent ON parent.id = child.announcement_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_announcement_read_user',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM announcement_read child
LEFT JOIN user parent ON parent.id = child.user_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_survey_question_announcement',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM survey_question child
LEFT JOIN announcement parent ON parent.id = child.announcement_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_survey_response_announcement',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM survey_response child
LEFT JOIN announcement parent ON parent.id = child.announcement_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_survey_response_user',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM survey_response child
LEFT JOIN user parent ON parent.id = child.user_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_survey_answer_response',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM survey_answer child
LEFT JOIN survey_response parent ON parent.id = child.response_id
WHERE parent.id IS NULL
UNION ALL
SELECT
  'orphan_survey_answer_question',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0'
FROM survey_answer child
LEFT JOIN survey_question parent ON parent.id = child.question_id
WHERE parent.id IS NULL;

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

SELECT 'user' AS table_name, COUNT(*) AS row_count FROM user
UNION ALL SELECT 'subscription', COUNT(*) FROM subscription
UNION ALL SELECT 'credit_account', COUNT(*) FROM credit_account
UNION ALL SELECT 'credit_cycle', COUNT(*) FROM credit_cycle
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

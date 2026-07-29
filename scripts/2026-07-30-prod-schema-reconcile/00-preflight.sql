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
  IF(COUNT(*) = 20, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '20'
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME IN (
    'user', 'subscription', 'agent_attachment', 'agent_run',
    'trial_grant', 'user_booster_balance', 'membership_event',
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
  IF(
    COUNT(*) = 0 OR (
      COUNT(*) = 1
      AND MAX(COLUMN_TYPE) = 'varchar(20)'
      AND MAX(IS_NULLABLE) = 'NO'
      AND MAX(COLUMN_DEFAULT) = 'monthly'
    ),
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)),
  'absent or 1/varchar(20)/NO/monthly'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'plan_type'
UNION ALL
SELECT
  'subscription_cycle_credits_column_count',
  IF(
    COUNT(*) = 0 OR (
      COUNT(*) = 1
      AND MAX(COLUMN_TYPE) = 'int'
      AND MAX(IS_NULLABLE) = 'NO'
      AND MAX(COLUMN_DEFAULT) = '2000'
    ),
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), MAX(COLUMN_TYPE), MAX(IS_NULLABLE), MAX(COLUMN_DEFAULT)),
  'absent or 1/int/NO/2000'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'cycle_credits'
UNION ALL
SELECT
  'attachment_parsed_column_shapes',
  IF(
    COUNT(*) = 0
      OR (
        COUNT(*) BETWEEN 1 AND 5
        AND GROUP_CONCAT(
          COLUMN_NAME
          ORDER BY FIELD(
            COLUMN_NAME,
            'parsed_content', 'parsed_content_sha256', 'parsed_content_byte_size',
            'parsed_page_count', 'parsed_at'
          )
          SEPARATOR ','
        ) = SUBSTRING_INDEX(
          'parsed_content,parsed_content_sha256,parsed_content_byte_size,parsed_page_count,parsed_at',
          ',',
          COUNT(*)
        )
        AND COUNT(*) = COALESCE(SUM(
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
        ), 0)
      )
      OR (
        COUNT(*) = 5
        AND COUNT(*) = COALESCE(SUM(
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
        ), 0)
      ),
    'PASS',
    'FAIL'
  ),
  CASE
    WHEN COUNT(*) = 0 THEN 'absent'
    WHEN COUNT(*) = 5 AND COUNT(*) = COALESCE(SUM(
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
    ), 0) THEN 'legacy_complete'
    WHEN COUNT(*) BETWEEN 1 AND 5 AND COUNT(*) = COALESCE(SUM(
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
    ), 0) THEN CONCAT('final_prefix_', COUNT(*))
    ELSE 'incompatible'
  END,
  'absent, ordered final prefix, full final, or legacy_complete'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_attachment'
  AND COLUMN_NAME IN (
    'parsed_content', 'parsed_content_sha256', 'parsed_content_byte_size',
    'parsed_page_count', 'parsed_at'
  )
UNION ALL
SELECT
  'feishu_table_count',
  IF(COUNT(*) BETWEEN 0 AND 6, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 through 6 exact tables are upgradeable'
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME IN (
    'user_third_party_account', 'feishu_cli_vault', 'feishu_auth_session',
    'feishu_operation', 'feishu_operation_proof_consumption',
    'feishu_operation_execution_gate'
  )
UNION ALL
SELECT
  'agent_pending_external_column_shapes',
  IF(
    COUNT(*) = COALESCE(SUM(
      CASE COLUMN_NAME
        WHEN 'pending_external_action_json' THEN COLUMN_TYPE = 'json' AND IS_NULLABLE = 'YES'
        WHEN 'pending_external_action_at' THEN COLUMN_TYPE = 'datetime(3)' AND IS_NULLABLE = 'YES'
        ELSE FALSE
      END
    ), 0),
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
  'every present external-action column has the exact final shape'
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_run'
  AND COLUMN_NAME IN ('pending_external_action_json', 'pending_external_action_at')
UNION ALL
SELECT
  'agent_pending_index_shape',
  IF(
    COUNT(*) = 0 OR (
      COUNT(*) = 2
      AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') =
          'state_reason,pending_question_at'
      AND MAX(NON_UNIQUE) = 1
    ),
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), MAX(NON_UNIQUE)),
  'absent or 2/state_reason,pending_question_at/1'
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'agent_run'
  AND INDEX_NAME = 'idx_ar_state_pending'
UNION ALL
SELECT
  'agent_state_reason_upgradeable',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 unsupported state_reason rows'
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
  IF(actual.table_name IS NULL OR actual.contract_sha = expected.contract_sha, 'PASS', 'FAIL') AS status,
  COALESCE(actual.contract_sha, 'absent') AS observed,
  CONCAT('absent or ', expected.contract_sha) AS expected
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
    (SELECT COUNT(*) FROM information_schema.TABLES
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_NAME = 'feishu_operation_proof_consumption') = 0
    OR COUNT(*) = 0
    OR (
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
      )
    ),
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT('constraints=', COUNT(*)) AS observed,
  'absent, zero FKs for atomic repair, or exact two restrictive operation FKs' AS expected
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

SET @proof_preflight_sql := IF(
  (SELECT COUNT(*) FROM information_schema.TABLES
   WHERE TABLE_SCHEMA = DATABASE()
     AND TABLE_NAME = 'feishu_operation_proof_consumption') = 0,
  'SELECT ''feishu_proof_column_compatibility'' AS check_name, ''PASS'' AS status, ''absent'' AS observed, ''absent or 2 exact CHAR(36) operation references'' AS expected
   UNION ALL SELECT ''feishu_proof_source_orphans'', ''PASS'', ''absent'', ''0''
   UNION ALL SELECT ''feishu_proof_consumer_orphans'', ''PASS'', ''absent'', ''0''',
  'SELECT
     ''feishu_proof_column_compatibility'' AS check_name,
     IF(COUNT(*) = 2, ''PASS'', ''FAIL'') AS status,
     CAST(COUNT(*) AS CHAR) AS observed,
     ''2 exact CHAR(36) operation references'' AS expected
   FROM information_schema.COLUMNS proof_column
   JOIN information_schema.COLUMNS operation_column
     ON operation_column.TABLE_SCHEMA = proof_column.TABLE_SCHEMA
    AND operation_column.TABLE_NAME = ''feishu_operation''
    AND operation_column.COLUMN_NAME = ''id''
   WHERE proof_column.TABLE_SCHEMA = DATABASE()
     AND proof_column.TABLE_NAME = ''feishu_operation_proof_consumption''
     AND proof_column.COLUMN_NAME IN (''source_operation_id'', ''consumer_operation_id'')
     AND proof_column.COLUMN_TYPE = operation_column.COLUMN_TYPE
     AND proof_column.CHARACTER_SET_NAME <=> operation_column.CHARACTER_SET_NAME
     AND proof_column.COLLATION_NAME <=> operation_column.COLLATION_NAME
   UNION ALL
   SELECT
     ''feishu_proof_source_orphans'',
     IF(COUNT(*) = 0, ''PASS'', ''FAIL''),
     CAST(COUNT(*) AS CHAR),
     ''0''
   FROM feishu_operation_proof_consumption proof
   LEFT JOIN feishu_operation operation_row
     ON operation_row.id = proof.source_operation_id
   WHERE operation_row.id IS NULL
   UNION ALL
   SELECT
     ''feishu_proof_consumer_orphans'',
     IF(COUNT(*) = 0, ''PASS'', ''FAIL''),
     CAST(COUNT(*) AS CHAR),
     ''0''
   FROM feishu_operation_proof_consumption proof
   LEFT JOIN feishu_operation operation_row
     ON operation_row.id = proof.consumer_operation_id
   WHERE operation_row.id IS NULL'
);
PREPARE proof_preflight_stmt FROM @proof_preflight_sql;
EXECUTE proof_preflight_stmt;
DEALLOCATE PREPARE proof_preflight_stmt;

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
WHERE parent.id IS NULL
UNION ALL
SELECT
  'duplicate_announcement_read_user_pair',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 duplicate groups'
FROM (
  SELECT announcement_id, user_id
  FROM announcement_read
  GROUP BY announcement_id, user_id
  HAVING COUNT(*) > 1
) duplicate_annread
UNION ALL
SELECT
  'duplicate_survey_response_user_pair',
  IF(COUNT(*) = 0, 'PASS', 'FAIL'),
  CAST(COUNT(*) AS CHAR),
  '0 duplicate groups'
FROM (
  SELECT announcement_id, user_id
  FROM survey_response
  GROUP BY announcement_id, user_id
  HAVING COUNT(*) > 1
) duplicate_survey_response;

WITH expected AS (
  SELECT 'fk_annread_announcement' AS constraint_name,
         'announcement_read' AS child_table, 'announcement_id' AS child_column,
         'announcement' AS parent_table, 'id' AS parent_column
  UNION ALL SELECT 'fk_annread_user', 'announcement_read', 'user_id', 'user', 'id'
  UNION ALL SELECT 'fk_sq_announcement', 'survey_question', 'announcement_id', 'announcement', 'id'
  UNION ALL SELECT 'fk_sr_announcement', 'survey_response', 'announcement_id', 'announcement', 'id'
  UNION ALL SELECT 'fk_sr_user', 'survey_response', 'user_id', 'user', 'id'
  UNION ALL SELECT 'fk_sa_response', 'survey_answer', 'response_id', 'survey_response', 'id'
  UNION ALL SELECT 'fk_sa_question', 'survey_answer', 'question_id', 'survey_question', 'id'
)
SELECT
  CONCAT(expected.constraint_name, '_column_compatibility') AS check_name,
  IF(
    child_meta.COLUMN_TYPE = parent_meta.COLUMN_TYPE
      AND COALESCE(child_meta.CHARACTER_SET_NAME, '<NULL>') =
          COALESCE(parent_meta.CHARACTER_SET_NAME, '<NULL>')
      AND COALESCE(child_meta.COLLATION_NAME, '<NULL>') =
          COALESCE(parent_meta.COLLATION_NAME, '<NULL>'),
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT_WS(
    '|', child_meta.COLUMN_TYPE, COALESCE(child_meta.CHARACTER_SET_NAME, '<NULL>'),
    COALESCE(child_meta.COLLATION_NAME, '<NULL>')
  ) AS observed,
  CONCAT_WS(
    '|', parent_meta.COLUMN_TYPE, COALESCE(parent_meta.CHARACTER_SET_NAME, '<NULL>'),
    COALESCE(parent_meta.COLLATION_NAME, '<NULL>')
  ) AS expected
FROM expected
LEFT JOIN information_schema.COLUMNS child_meta
  ON child_meta.TABLE_SCHEMA = DATABASE()
 AND child_meta.TABLE_NAME = expected.child_table
 AND child_meta.COLUMN_NAME = expected.child_column
LEFT JOIN information_schema.COLUMNS parent_meta
  ON parent_meta.TABLE_SCHEMA = DATABASE()
 AND parent_meta.TABLE_NAME = expected.parent_table
 AND parent_meta.COLUMN_NAME = expected.parent_column
ORDER BY expected.constraint_name;

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
    actual.constraint_name IS NULL OR (
      actual.table_name = expected.table_name
      AND actual.column_name = expected.column_name
      AND actual.referenced_table_name = expected.referenced_table_name
      AND actual.referenced_column_name = expected.referenced_column_name
      AND actual.delete_rule = expected.delete_rule
      AND actual.update_rule = expected.update_rule
    ),
    'PASS',
    'FAIL'
  ) AS status,
  IF(actual.constraint_name IS NULL, 'absent', CONCAT_WS(
    '|', actual.table_name, actual.column_name, actual.referenced_table_name,
    actual.referenced_column_name, actual.delete_rule, actual.update_rule
  )) AS observed,
  CONCAT('absent or ', CONCAT_WS(
    '|', expected.table_name, expected.column_name, expected.referenced_table_name,
    expected.referenced_column_name, expected.delete_rule, expected.update_rule
  )) AS expected
FROM expected
LEFT JOIN actual ON actual.constraint_name = expected.constraint_name
ORDER BY expected.constraint_name;

SELECT
  'uk_annread_contract' AS check_name,
  IF(
    COUNT(*) = 0 OR (
      COUNT(*) = 2
      AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'announcement_id,user_id'
      AND MAX(NON_UNIQUE) = 0
    ),
    'PASS',
    'FAIL'
  ) AS status,
  CONCAT_WS('/', COUNT(*), GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), MAX(NON_UNIQUE)) AS observed,
  'absent or 2/announcement_id,user_id/0' AS expected
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND INDEX_NAME = 'uk_annread'
UNION ALL
SELECT
  'uk_sr_contract',
  IF(
    COUNT(*) = 0 OR (
      COUNT(*) = 2
      AND GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') = 'announcement_id,user_id'
      AND MAX(NON_UNIQUE) = 0
    ),
    'PASS',
    'FAIL'
  ),
  CONCAT_WS('/', COUNT(*), GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), MAX(NON_UNIQUE)),
  'absent or 2/announcement_id,user_id/0'
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

-- Full attachment projection. Missing rollout columns are represented by the
-- exact defaults that ADD COLUMN will give historical rows, so the before and
-- after values are directly comparable without writing customer data.
SET @aa_parsed_content_expr := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
     AND COLUMN_NAME = 'parsed_content') = 1,
  'IF(`parsed_content` IS NULL, ''parsed_content=N'', CONCAT(''parsed_content=V:'', OCTET_LENGTH(CAST(`parsed_content` AS BINARY)), '':'', SHA2(CAST(`parsed_content` AS BINARY), 256)))',
  '''parsed_content=N'''
);
SET @aa_parsed_sha_expr := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
     AND COLUMN_NAME = 'parsed_content_sha256') = 1,
  'IF(`parsed_content_sha256` IS NULL, ''parsed_content_sha256=N'', CONCAT(''parsed_content_sha256=V:'', OCTET_LENGTH(CAST(`parsed_content_sha256` AS BINARY)), '':'', SHA2(CAST(`parsed_content_sha256` AS BINARY), 256)))',
  '''parsed_content_sha256=V:0:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'''
);
SET @aa_parsed_byte_expr := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
     AND COLUMN_NAME = 'parsed_content_byte_size') = 1,
  'IF(`parsed_content_byte_size` IS NULL, ''parsed_content_byte_size=N'', CONCAT(''parsed_content_byte_size=V:'', OCTET_LENGTH(CAST(`parsed_content_byte_size` AS BINARY)), '':'', SHA2(CAST(`parsed_content_byte_size` AS BINARY), 256)))',
  '''parsed_content_byte_size=V:1:5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9'''
);
SET @aa_parsed_page_expr := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
     AND COLUMN_NAME = 'parsed_page_count') = 1,
  'IF(`parsed_page_count` IS NULL, ''parsed_page_count=N'', CONCAT(''parsed_page_count=V:'', OCTET_LENGTH(CAST(`parsed_page_count` AS BINARY)), '':'', SHA2(CAST(`parsed_page_count` AS BINARY), 256)))',
  '''parsed_page_count=V:1:5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9'''
);
SET @aa_parsed_at_expr := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
     AND COLUMN_NAME = 'parsed_at') = 1,
  'IF(`parsed_at` IS NULL, ''parsed_at=N'', CONCAT(''parsed_at=V:'', OCTET_LENGTH(CAST(`parsed_at` AS BINARY)), '':'', SHA2(CAST(`parsed_at` AS BINARY), 256)))',
  '''parsed_at=N'''
);
SET @aa_complete_projection_sql := CONCAT(
  'SELECT ''agent_attachment_complete_projection'' AS evidence_name, COUNT(*) AS row_count, ',
  'COALESCE(SHA2(GROUP_CONCAT(SHA2(CONCAT_WS(''|'', ',
  'CONCAT(''id=V:'', OCTET_LENGTH(CAST(`id` AS BINARY)), '':'', SHA2(CAST(`id` AS BINARY), 256)), ',
  'CONCAT(''user_id=V:'', OCTET_LENGTH(CAST(`user_id` AS BINARY)), '':'', SHA2(CAST(`user_id` AS BINARY), 256)), ',
  'IF(`url` IS NULL, ''url=N'', CONCAT(''url=V:'', OCTET_LENGTH(CAST(`url` AS BINARY)), '':'', SHA2(CAST(`url` AS BINARY), 256))), ',
  'IF(`filename` IS NULL, ''filename=N'', CONCAT(''filename=V:'', OCTET_LENGTH(CAST(`filename` AS BINARY)), '':'', SHA2(CAST(`filename` AS BINARY), 256))), ',
  'IF(`mime_type` IS NULL, ''mime_type=N'', CONCAT(''mime_type=V:'', OCTET_LENGTH(CAST(`mime_type` AS BINARY)), '':'', SHA2(CAST(`mime_type` AS BINARY), 256))), ',
  'IF(`size` IS NULL, ''size=N'', CONCAT(''size=V:'', OCTET_LENGTH(CAST(`size` AS BINARY)), '':'', SHA2(CAST(`size` AS BINARY), 256))), ',
  'IF(`modality` IS NULL, ''modality=N'', CONCAT(''modality=V:'', OCTET_LENGTH(CAST(`modality` AS BINARY)), '':'', SHA2(CAST(`modality` AS BINARY), 256))), ',
  'IF(`width` IS NULL, ''width=N'', CONCAT(''width=V:'', OCTET_LENGTH(CAST(`width` AS BINARY)), '':'', SHA2(CAST(`width` AS BINARY), 256))), ',
  'IF(`height` IS NULL, ''height=N'', CONCAT(''height=V:'', OCTET_LENGTH(CAST(`height` AS BINARY)), '':'', SHA2(CAST(`height` AS BINARY), 256))), ',
  'IF(`ocr_text` IS NULL, ''ocr_text=N'', CONCAT(''ocr_text=V:'', OCTET_LENGTH(CAST(`ocr_text` AS BINARY)), '':'', SHA2(CAST(`ocr_text` AS BINARY), 256))), ',
  'IF(`vision_description` IS NULL, ''vision_description=N'', CONCAT(''vision_description=V:'', OCTET_LENGTH(CAST(`vision_description` AS BINARY)), '':'', SHA2(CAST(`vision_description` AS BINARY), 256))), ',
  'IF(`text_fallback` IS NULL, ''text_fallback=N'', CONCAT(''text_fallback=V:'', OCTET_LENGTH(CAST(`text_fallback` AS BINARY)), '':'', SHA2(CAST(`text_fallback` AS BINARY), 256))), ',
  'IF(`fallback_ready` IS NULL, ''fallback_ready=N'', CONCAT(''fallback_ready=V:'', OCTET_LENGTH(CAST(`fallback_ready` AS BINARY)), '':'', SHA2(CAST(`fallback_ready` AS BINARY), 256))), ',
  'IF(`fallback_error` IS NULL, ''fallback_error=N'', CONCAT(''fallback_error=V:'', OCTET_LENGTH(CAST(`fallback_error` AS BINARY)), '':'', SHA2(CAST(`fallback_error` AS BINARY), 256))), ',
  @aa_parsed_content_expr, ', ', @aa_parsed_sha_expr, ', ',
  @aa_parsed_byte_expr, ', ', @aa_parsed_page_expr, ', ', @aa_parsed_at_expr, ', ',
  'IF(`fallback_started_at` IS NULL, ''fallback_started_at=N'', CONCAT(''fallback_started_at=V:'', OCTET_LENGTH(CAST(`fallback_started_at` AS BINARY)), '':'', SHA2(CAST(`fallback_started_at` AS BINARY), 256))), ',
  'IF(`fallback_completed_at` IS NULL, ''fallback_completed_at=N'', CONCAT(''fallback_completed_at=V:'', OCTET_LENGTH(CAST(`fallback_completed_at` AS BINARY)), '':'', SHA2(CAST(`fallback_completed_at` AS BINARY), 256))), ',
  'IF(`retry_count` IS NULL, ''retry_count=N'', CONCAT(''retry_count=V:'', OCTET_LENGTH(CAST(`retry_count` AS BINARY)), '':'', SHA2(CAST(`retry_count` AS BINARY), 256))), ',
  'IF(`created_at` IS NULL, ''created_at=N'', CONCAT(''created_at=V:'', OCTET_LENGTH(CAST(`created_at` AS BINARY)), '':'', SHA2(CAST(`created_at` AS BINARY), 256)))',
  '), 256) ORDER BY `id` SEPARATOR ''\n''), 256), SHA2('''', 256)) AS sha256 ',
  'FROM `agent_attachment`'
);
PREPARE aa_complete_projection_stmt FROM @aa_complete_projection_sql;
EXECUTE aa_complete_projection_stmt;
DEALLOCATE PREPARE aa_complete_projection_stmt;

SET @proof_business_projection_sql := IF(
  (SELECT COUNT(*) FROM information_schema.TABLES
   WHERE TABLE_SCHEMA = DATABASE()
     AND TABLE_NAME = 'feishu_operation_proof_consumption') = 0,
  'SELECT ''feishu_proof_business_projection'' AS evidence_name, 0 AS row_count, SHA2('''', 256) AS sha256',
  'SELECT
     ''feishu_proof_business_projection'' AS evidence_name,
     COUNT(*) AS row_count,
     COALESCE(
       SHA2(
         GROUP_CONCAT(
           SHA2(CONCAT_WS(
             ''|'',
             CONCAT(''source_operation_id=V:'', OCTET_LENGTH(CAST(source_operation_id AS BINARY)), '':'', SHA2(CAST(source_operation_id AS BINARY), 256)),
             CONCAT(''consumer_operation_id=V:'', OCTET_LENGTH(CAST(consumer_operation_id AS BINARY)), '':'', SHA2(CAST(consumer_operation_id AS BINARY), 256)),
             CONCAT(''user_id=V:'', OCTET_LENGTH(CAST(user_id AS BINARY)), '':'', SHA2(CAST(user_id AS BINARY), 256)),
             CONCAT(''generation=V:'', OCTET_LENGTH(CAST(generation AS BINARY)), '':'', SHA2(CAST(generation AS BINARY), 256)),
             CONCAT(''agent_run_id=V:'', OCTET_LENGTH(CAST(agent_run_id AS BINARY)), '':'', SHA2(CAST(agent_run_id AS BINARY), 256)),
             CONCAT(''created_at=V:'', OCTET_LENGTH(CAST(created_at AS BINARY)), '':'', SHA2(CAST(created_at AS BINARY), 256))
           ), 256)
           ORDER BY source_operation_id SEPARATOR ''\n''
         ),
         256
       ),
       SHA2('''', 256)
     ) AS sha256
   FROM feishu_operation_proof_consumption'
);
PREPARE proof_business_projection_stmt FROM @proof_business_projection_sql;
EXECUTE proof_business_projection_stmt;
DEALLOCATE PREPARE proof_business_projection_stmt;

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

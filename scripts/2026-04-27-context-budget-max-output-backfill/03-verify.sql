-- Verify: post-backfill sanity checks for max_output_tokens
-- Run with: docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 03-verify.sql
--
-- SUCCESS criteria:
--   Section A: 0 rows (no NULL or 0 max_output_tokens remaining)
--   Section B: 0 rows (no max_output_tokens >= context_window violations)
--   Section C: 0 rows (no max_output_tokens < 16384 = below default reserved_output_tokens)
--
-- If any section returns non-zero rows:
--   → DO NOT proceed with feature flag activation
--   → Investigate the specific model_key(s)
--   → Either fix 02-apply.sql and re-apply, or rollback from mysqldump

SELECT '=== A. MUST BE 0 ROWS: LLM services still missing max_output_tokens ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo,
  'STILL NULL OR ZERO — backfill incomplete' AS issue
FROM ai_service
WHERE service_type = 'llm'
  AND (
    JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0
  );

SELECT '=== B. MUST BE 0 ROWS: max_output_tokens >= context_window (spec §2.4 violation) ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo,
  'CONSTRAINT VIOLATION: mo >= cw — will trigger ErrContextConfigInvalid' AS issue
FROM ai_service
WHERE service_type = 'llm'
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NOT NULL
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') > 0
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') >= JSON_EXTRACT(capability_json, '$.context_window');

SELECT '=== C. MUST BE 0 ROWS: max_output_tokens < 16384 (below default reserved_output_tokens) ===' AS section;
-- Context Budget spec: default reserved_output_tokens for sop_run = 16384.
-- If max_output_tokens < reserved_output_tokens, safe_input_budget calculation will error.
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo,
  'WARNING: mo < 16384 (default reserved). Safe only if this model uses a lower reserved profile' AS issue
FROM ai_service
WHERE service_type = 'llm'
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NOT NULL
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') > 0
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') < 16384;

SELECT '=== D. INFORMATIONAL: full post-backfill state of all LLM services ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo,
  CASE
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
      OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0
      THEN 'ERROR: still null/zero'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') >= JSON_EXTRACT(capability_json, '$.context_window')
      THEN 'ERROR: mo >= cw'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') < 16384
      THEN 'WARN: mo < 16384'
    ELSE 'OK'
  END AS check_status
FROM ai_service
WHERE service_type = 'llm'
ORDER BY check_status DESC, id;

SELECT '=== E. SUMMARY: count by check_status ===' AS section;
SELECT
  CASE
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
      OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0
      THEN 'ERROR: still null/zero'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') >= JSON_EXTRACT(capability_json, '$.context_window')
      THEN 'ERROR: mo >= cw'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') < 16384
      THEN 'WARN: mo < 16384'
    ELSE 'OK'
  END AS check_status,
  COUNT(*) AS n
FROM ai_service
WHERE service_type = 'llm'
GROUP BY check_status
ORDER BY check_status;

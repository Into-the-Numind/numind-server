-- Dry-run: read-only inventory of LLM service max_output_tokens status
-- Run with: docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 01-dry-run.sql
-- This query is SAFE: no writes, no side effects.
--
-- USAGE:
--   1. Run this script and redirect output to a file:
--        docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
--          < 01-dry-run.sql > dry-run-output.txt 2>&1
--   2. Open dry-run-output.txt and find all rows with status='NEEDS BACKFILL'.
--   3. For each NEEDS BACKFILL row, cross-reference model_key with:
--        docs/superpowers/research/2026-04-27-llm-max-output-tokens-table.md
--      to determine the correct max_output_tokens value.
--   4. Edit 02-apply.sql to ensure each NEEDS BACKFILL model_key is covered
--      by a LIKE-pattern UPDATE block, then proceed with apply.

SELECT '=== A. Full LLM Service Inventory ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')     AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens')  AS mo,
  CASE
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL THEN 'NEEDS BACKFILL'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0     THEN 'NEEDS BACKFILL'
    ELSE 'ALREADY SET'
  END AS status
FROM ai_service
WHERE service_type = 'llm'
ORDER BY status DESC, id;

SELECT '=== B. Summary: count by status ===' AS section;
SELECT
  CASE
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL THEN 'NEEDS BACKFILL'
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0     THEN 'NEEDS BACKFILL'
    ELSE 'ALREADY SET'
  END AS status,
  COUNT(*) AS n
FROM ai_service
WHERE service_type = 'llm'
GROUP BY status;

SELECT '=== C. Constraint check: rows where current max_output_tokens >= context_window (invalid) ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo,
  'CONSTRAINT VIOLATION: mo >= cw' AS issue
FROM ai_service
WHERE service_type = 'llm'
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NOT NULL
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') > 0
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') >= JSON_EXTRACT(capability_json, '$.context_window');

SELECT '=== D. Action items: copy this list, map to reference table, fill in 02-apply.sql ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window') AS context_window,
  '*** LOOKUP max_output_tokens in research doc §3.2 ***' AS action_required
FROM ai_service
WHERE service_type = 'llm'
  AND (
    JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0
  )
ORDER BY id;

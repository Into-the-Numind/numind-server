-- Rollback: remove max_output_tokens set by 02-apply.sql
-- Run with: docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 04-rollback.sql
--
-- ============================================================================
-- IMPORTANT: PREFER mysqldump RESTORE OVER THIS SCRIPT
-- ============================================================================
-- This rollback script removes $.max_output_tokens from all LLM service rows.
-- It is a BLUNT instrument: it removes ALL max_output_tokens, including values
-- that were already set BEFORE the backfill (those 2/14 rows that had values).
--
-- PREFERRED rollback approach:
--   1. Restore from the mysqldump taken before 02-apply.sql:
--        docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
--          < ai_service_backup_YYYYMMDD_HHMMSS.sql
--   2. This precisely restores the original state without touching other tables.
--
-- Use THIS script ONLY if:
--   - No mysqldump backup is available, AND
--   - You are certain that ALL LLM services had NULL max_output_tokens before backfill
--     (i.e., you are reverting to the pre-F1-backfill state where all were NULL)
-- ============================================================================
--
-- TRIGGER CONDITIONS for rollback:
--   - 03-verify.sql shows ERROR rows (mo >= cw or still null/zero after apply)
--   - Context Budget feature causes unexpected 4xx LLM errors post-deployment
--   - Business decision to abort feature rollout
--   - Admin UI shows corrupted capability_json data

START TRANSACTION;

-- ----------------------------------------------------------------------
-- Remove max_output_tokens from all LLM services
-- (This is safe because JSON_REMOVE only removes the specified path;
-- all other capability_json fields remain intact)
-- ----------------------------------------------------------------------
UPDATE ai_service
SET capability_json = JSON_REMOVE(capability_json, '$.max_output_tokens'),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NOT NULL;

COMMIT;

-- Confirm rollback state
SELECT 'ROLLBACK COMPLETE' AS status;

SELECT '=== Post-rollback state: all LLM services should have max_output_tokens=NULL ===' AS section;
SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo_after_rollback,
  CASE
    WHEN JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL THEN 'ROLLED BACK'
    ELSE 'NOT ROLLED BACK — unexpected'
  END AS rollback_status
FROM ai_service
WHERE service_type = 'llm'
ORDER BY id;

SELECT '=== ACTION REQUIRED after rollback ===' AS section;
SELECT
  'Context Budget feature (F-1) will return ErrContextConfigInvalid for all LLM calls.' AS warning,
  'Deactivate Context Budget feature flag immediately after rollback.' AS action_1,
  'Investigate root cause before re-attempting backfill.' AS action_2;

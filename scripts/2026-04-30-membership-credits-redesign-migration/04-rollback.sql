-- =============================================================================
-- 04-rollback.sql  —  membership-credits-redesign migration ROLLBACK
-- Deletes all rows inserted by 02-apply.sql using apply_log as targeting guide.
-- T+24h window: should not be run after 24 hours post-apply without DBA review.
--
-- Run with:
--   docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 04-rollback.sql
--
-- When to run:
--   03-verify.sql shows any violation_count > 0
--   Business decision to abort migration
--
-- What this does NOT undo:
--   • migration_20260430_credit_pkg_backup (preserved for audit)
--   • migration_20260430_apply_log (preserved for audit)
-- =============================================================================

-- ─────────────────────────────────────────────────────────────────────────────
-- PRE-FLIGHT: confirm apply_log exists and has data
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== PRE-FLIGHT: apply_log check ===' AS section;
SELECT step, table_name, rows_inserted, applied_at
FROM migration_20260430_apply_log
ORDER BY applied_at;

-- ─────────────────────────────────────────────────────────────────────────────
-- TRANSACTION BEGIN
-- ─────────────────────────────────────────────────────────────────────────────

START TRANSACTION;

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 1: DELETE membership_event rows created by this migration
-- Primary targeting: idempotency_key LIKE 'migration-20260430-cp-%'
-- Fallback (belt-and-suspenders): occurred_at >= apply_log timestamp
-- ─────────────────────────────────────────────────────────────────────────────

DELETE FROM membership_event
WHERE idempotency_key LIKE 'migration-20260430-cp-%';

SELECT ROW_COUNT() AS membership_event_rows_deleted;

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 2: DELETE user_booster_balance rows created by this migration
-- Target: rows where updated_at matches the apply cutover_ts
-- Belt-and-suspenders: use apply_log.applied_at for the booster step
-- ─────────────────────────────────────────────────────────────────────────────

DELETE ubb FROM user_booster_balance ubb
INNER JOIN (
  SELECT applied_at AS cutover_ts
  FROM migration_20260430_apply_log
  WHERE step = 'step3_user_booster_balance'
  LIMIT 1
) lg ON ubb.updated_at >= DATE_SUB(lg.cutover_ts, INTERVAL 5 SECOND)
      AND ubb.updated_at <= DATE_ADD(lg.cutover_ts, INTERVAL 5 SECOND);

SELECT ROW_COUNT() AS user_booster_balance_rows_deleted;

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 3: DELETE trial_grant rows created by this migration
-- ─────────────────────────────────────────────────────────────────────────────

DELETE tg FROM trial_grant tg
INNER JOIN (
  SELECT applied_at AS cutover_ts
  FROM migration_20260430_apply_log
  WHERE step = 'step2_trial_grant'
  LIMIT 1
) lg ON tg.created_at >= DATE_SUB(lg.cutover_ts, INTERVAL 5 SECOND)
      AND tg.created_at <= DATE_ADD(lg.cutover_ts, INTERVAL 5 SECOND);

SELECT ROW_COUNT() AS trial_grant_rows_deleted;

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 4: DELETE credit_cycle rows (application-created after migration)
-- Only delete rows created within the 24h rollback window.
-- ─────────────────────────────────────────────────────────────────────────────

DELETE cc FROM credit_cycle cc
INNER JOIN (
  SELECT applied_at AS cutover_ts
  FROM migration_20260430_apply_log
  WHERE step = 'step1_subscription'
  LIMIT 1
) lg ON cc.created_at >= DATE_SUB(lg.cutover_ts, INTERVAL 5 SECOND);

SELECT ROW_COUNT() AS credit_cycle_rows_deleted;

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 5: DELETE subscription rows created by this migration
-- ─────────────────────────────────────────────────────────────────────────────

DELETE s FROM subscription s
INNER JOIN (
  SELECT applied_at AS cutover_ts
  FROM migration_20260430_apply_log
  WHERE step = 'step1_subscription'
  LIMIT 1
) lg ON s.created_at >= DATE_SUB(lg.cutover_ts, INTERVAL 5 SECOND)
      AND s.created_at <= DATE_ADD(lg.cutover_ts, INTERVAL 5 SECOND);

SELECT ROW_COUNT() AS subscription_rows_deleted;

-- ─────────────────────────────────────────────────────────────────────────────
-- COMMIT
-- ─────────────────────────────────────────────────────────────────────────────

COMMIT;

-- ─────────────────────────────────────────────────────────────────────────────
-- POST-ROLLBACK: verify new tables are empty (or back to pre-migration state)
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== POST-ROLLBACK: new table row counts (expect 0 each) ===' AS section;
SELECT 'subscription'         AS tbl, COUNT(*) AS rows FROM subscription
UNION ALL
SELECT 'trial_grant'          AS tbl, COUNT(*) AS rows FROM trial_grant
UNION ALL
SELECT 'credit_cycle'         AS tbl, COUNT(*) AS rows FROM credit_cycle
UNION ALL
SELECT 'user_booster_balance' AS tbl, COUNT(*) AS rows FROM user_booster_balance
UNION ALL
SELECT 'membership_event'     AS tbl, COUNT(*) AS rows FROM membership_event
  WHERE idempotency_key LIKE 'migration-20260430-cp-%';

SELECT 'ROLLBACK COMPLETE — backup table migration_20260430_credit_pkg_backup preserved for audit' AS status;
SELECT '(Drop after audit: DROP TABLE migration_20260430_credit_pkg_backup, migration_20260430_apply_log;)' AS cleanup_note;

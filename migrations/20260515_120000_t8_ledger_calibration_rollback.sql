-- T8 Ledger Calibration — Rollback Migration
-- Feature: membership-credits-redesign cleanup T8
-- Date: 2026-05-15
--
-- WHEN TO RUN THIS:
--   * Only if the forward migration (20260515_120000_t8_ledger_calibration.sql) caused
--     incorrect data or was run in error.
--   * Two rollback windows (see below).
--
-- TWO-WINDOW ROLLBACK STRATEGY (from T8 spec Reviewer P1-7 fix):
--
--   Window A (0-1 hour after forward migration):
--     Full table restore from backup tables is safe because no user has generated
--     new deductions since T8 ran. Run this script as-is.
--
--   Window B (>1 hour after forward migration):
--     Users may have generated new legitimate deductions since T8. Full table restore
--     would OVERWRITE post-T8 deductions (losing user credits). In Window B:
--     1. DO NOT run this script's DELETE+INSERT block.
--     2. Instead, identify the specific rows changed by T8 using the audit idempotency_keys.
--     3. Run the targeted_rollback section at the bottom for individual row recovery.
--     4. After restoring, re-run the ledger calibration SQL (T8_I10, T8_I11 checks)
--        to verify convergence.
--
-- Backup tables retention policy: 30 days minimum.
-- DO NOT DROP trial_grant_backup_t8 or credit_cycle_backup_t8 until 30 days post-T8.

-- ── Confirm backup tables exist before proceeding ─────────────────────────────
SELECT 'ROLLBACK PRE-CHECK' AS status;
SELECT COUNT(*) AS trial_grant_backup_rows FROM trial_grant_backup_t8;
SELECT COUNT(*) AS credit_cycle_backup_rows FROM credit_cycle_backup_t8;
-- If either table shows 0 rows or doesn't exist, STOP — backup was not created properly.

-- ── Step R1: Delete T8 audit rows from membership_event ──────────────────────
-- These are safe to delete regardless of window (they are system audit rows, not user events).
DELETE FROM membership_event
WHERE idempotency_key LIKE 't8_calibration_dev_20260515%';
-- For prod rollback, change pattern to: 't8_calibration_prod_20260515%'

SELECT 'AUDIT ROWS DELETED' AS status,
       ROW_COUNT() AS rows_deleted;

-- ── Step R2: Revert the membership_event ENUM extension ───────────────────────
-- Only run this if NO OTHER audit_calibration rows exist in membership_event.
-- Check first:
SELECT 'check_other_admin_calibration_events' AS check_name, COUNT(*) AS count
FROM membership_event WHERE event_type = 'admin_calibration';
-- If count > 0 after deleting T8 audit rows, DO NOT revert the ENUM —
-- another calibration migration also uses it.
-- If count = 0, it is safe to revert:
-- ALTER TABLE membership_event
--   MODIFY COLUMN event_type ENUM(
--     'trial_granted',
--     'sub_granted',
--     'sub_renewed',
--     'booster_granted'
--   ) NOT NULL;
-- (Commented out intentionally — DBA must verify count=0 above before running manually.)

-- ── Step R3: Full restore (WINDOW A ONLY — within 1 hour of forward migration) ──
-- WARNING: DO NOT RUN IN WINDOW B (>1 hour after forward migration).
--          Post-T8 user deductions will be overwritten.

START TRANSACTION;

-- Restore trial_grant from backup
DELETE FROM trial_grant;
INSERT INTO trial_grant
  SELECT id, user_id, granted_at, expires_at, credits_remaining, source, granter_user_id, created_at
  FROM trial_grant_backup_t8;

-- Restore credit_cycle from backup
DELETE FROM credit_cycle;
INSERT INTO credit_cycle
  SELECT id, user_id, subscription_id, cycle_start, cycle_end, credits_granted, credits_remaining, created_at, updated_at
  FROM credit_cycle_backup_t8;

COMMIT;

SELECT 'FULL RESTORE COMPLETE (Window A)' AS status;
SELECT 'Verify: trial_grant and credit_cycle match their backup tables.' AS reminder;

-- Post-restore verification
SELECT 'post_restore_trial_grant_match' AS check_name, COUNT(*) AS mismatch_count
FROM trial_grant tg
JOIN trial_grant_backup_t8 bk ON bk.user_id = tg.user_id
WHERE tg.credits_remaining != bk.credits_remaining;
-- Expected: 0

SELECT 'post_restore_credit_cycle_match' AS check_name, COUNT(*) AS mismatch_count
FROM credit_cycle cc
JOIN credit_cycle_backup_t8 bk ON bk.id = cc.id
WHERE cc.credits_remaining != bk.credits_remaining;
-- Expected: 0

-- ── Step R4: Backup table retention (DO NOT DROP) ─────────────────────────────
-- Backup tables trial_grant_backup_t8 and credit_cycle_backup_t8 should be
-- retained for 30 days as per T8 rollback policy.
-- DROP them only after 30 days and after confirming no rollback is needed:
--   DROP TABLE IF EXISTS trial_grant_backup_t8;
--   DROP TABLE IF EXISTS credit_cycle_backup_t8;
-- (Commented out — manual DBA action after 30-day window.)

-- ── WINDOW B: Targeted rollback (use when >1 hour after forward migration) ────
-- Identify T8-affected rows using audit records:
--
-- For expired trial rows that were zeroed (user_id from audit):
--   UPDATE trial_grant tg
--   JOIN trial_grant_backup_t8 bk ON bk.user_id = tg.user_id
--   SET tg.credits_remaining = bk.credits_remaining
--   WHERE tg.expires_at < NOW()
--     AND bk.credits_remaining != tg.credits_remaining
--     -- Only restore rows that T8 actually touched (audit idempotency_key exists):
--     AND EXISTS (
--       SELECT 1 FROM membership_event me
--       WHERE me.idempotency_key = CONCAT('t8_calibration_dev_20260515_trial_', tg.user_id)
--     );
--
-- For cycle rows that were re-based:
--   UPDATE credit_cycle cc
--   JOIN credit_cycle_backup_t8 bk ON bk.id = cc.id
--   SET cc.credits_remaining = bk.credits_remaining
--   WHERE cc.cycle_end > NOW()
--     AND bk.credits_remaining != cc.credits_remaining
--     AND EXISTS (
--       SELECT 1 FROM membership_event me
--       WHERE me.idempotency_key = CONCAT('t8_calibration_dev_20260515_cycle_', cc.id)
--     );
--
-- After targeted rollback, run ledger calibration checks to verify state:
--   (T8_I9, T8_I10, T8_I11 from the forward migration post-checks)
-- (All targeted rollback queries are commented out — run manually with DBA oversight.)

-- T8 Ledger Calibration — Forward Migration
-- Feature: membership-credits-redesign cleanup T8
-- Date: 2026-05-15
--
-- Purpose: Re-base trial_grant.credits_remaining and credit_cycle.credits_remaining
-- to match the credit_transaction ledger (the authoritative source of truth after T1).
--
-- What this does:
--   1. Backup snapshot of trial_grant + credit_cycle for rollback safety
--   2. Pre-check: confirm source_type 100% backfilled (T1 precondition)
--   3. Force expired trial_grant.credits_remaining = 0
--   4. Re-base in-period trial_grant.credits_remaining = GREATEST(200 - ledger_deducted, 0)
--   5. Re-base in-period credit_cycle.credits_remaining = GREATEST(credits_granted - ledger_deducted, 0)
--   6. Post-check: verify all 8 spec §6 invariants still hold
--   7. Audit log: write membership_event row with idempotency_key=<@T8_KEY_PREFIX>_*
--
-- IMPORTANT PRECONDITIONS:
--   * T1 (source_type column + backfill) MUST be deployed BEFORE running this.
--     This SQL uses source_type='trial'|'cycle' to identify ledger rows per pool.
--     On a schema without T1 source_type column, this SQL WILL FAIL.
--   * This SQL does NOT run on prod. Dev-only at this stage.
--     Prod has ~30 expired trial rows with positive credits_remaining (documented below).
--
-- Idempotency: UPDATE statements use WHERE guards (credits_remaining != computed)
-- so re-running is safe — already-calibrated rows are skipped. The backup tables
-- use CREATE TABLE IF NOT EXISTS + "INSERT only if backup empty" guard so re-runs
-- never overwrite a pre-T8 snapshot with post-T8 state (see Step 1).
--
-- ⚠️  DDL OUTSIDE THE MAIN TRANSACTION
--   Step 0 below extends the membership_event.event_type ENUM (and the source ENUM
--   in fix #7). MySQL implicitly auto-commits ALL DDL statements. If subsequent
--   steps fail and you ROLLBACK, the ENUM extensions PERSIST — they cannot be
--   rolled back inside this script. This is safe because the changes are purely
--   additive (existing rows unaffected), but the rollback script documents the
--   "DBA manual verification required" handling for reverting them.
--
-- Prod readiness concerns (documented for future prod deployment):
--   * Prod has ~30 expired trial rows with credits_remaining > 0 (dev has 2).
--     The force-zero UPDATE will affect those 30 rows on prod.
--   * The membership_event idempotency_key prefix MUST be changed for prod by
--     editing the @T8_KEY_PREFIX SET statement below (dev → prod). The SET statement
--     is the single source of truth — no other literals to change.
--   * membership_event.event_type ENUM was extended by this migration to include
--     'admin_calibration'. Ensure prod schema has this ENUM extended before running.
--   * membership_event.source ENUM was extended by this migration to include
--     'system' for system-issued (non-user, non-B2B-grant) audit rows.
--     The B2B billing report aggregates by source='b2b_grant'; calibration rows
--     are tagged source='system' so they do NOT pollute the B2B billing aggregation.
--   * Run inside MAINTENANCE_MODE=true to avoid concurrent deduction races during calibration.
--
-- Rollback: use 20260515_120000_t8_ledger_calibration_rollback.sql
-- Rollback window:
--   * 0-1 hour after forward: full table restore from backup tables (safe).
--   * After 1 hour: partial recovery only — backup tables + idempotency_key audit row
--     allow row-level rollback; do NOT overwrite post-T8 user deductions.

-- ── Idempotency key prefix (CHANGE BEFORE PROD RUN) ───────────────────────────
-- ⚠️ This is the SINGLE source of truth for the idempotency_key prefix.
-- For dev:  SET @T8_KEY_PREFIX = 't8_calibration_dev_20260515';
-- For prod: SET @T8_KEY_PREFIX = 't8_calibration_prod_20260515';
-- All audit INSERTs below CONCAT() this var with their per-row suffix.
-- The rollback script has a matching SET — keep both in sync per environment.
SET @T8_KEY_PREFIX = 't8_calibration_dev_20260515';

-- ── Step 0.5: Trial denomination pre-check ────────────────────────────────────
-- Sanity check: confirm all credit_package(trial) rows were issued at 200 credits.
-- The trial calibration formula in Step 4 hardcodes 200 as the initial credits
-- (GREATEST(200 - ledger_deducted, 0)). If a future trial pkg is issued at a
-- different value (e.g. 300), this script silently miscomputes that user's balance.
-- Re-evaluate the Step 4 formula before continuing if this is non-zero.
SELECT 'pre_check_trial_denomination' AS check_name,
       COUNT(*) AS non_200_trial_pkgs
  FROM credit_package
 WHERE type = 'trial' AND total_credits != 200;
-- Expected: 0. If >0, STOP and review whether the 200 literal in Step 4 is still correct.

-- ── Step 0: Extend membership_event ENUMs ────────────────────────────────────
-- (a) event_type: add 'admin_calibration' so we can write a proper audit row.
-- (b) source:     add 'system' so calibration rows are NOT tagged 'b2b_grant'
--                 (which would pollute the B2B billing aggregation that filters
--                  by source='b2b_grant').
-- Both ENUM extensions are backward-compatible (existing rows are unaffected).
-- ⚠️ Both ALTER statements auto-commit. See header note "DDL OUTSIDE THE MAIN TRANSACTION".
ALTER TABLE membership_event
  MODIFY COLUMN event_type ENUM(
    'trial_granted',
    'sub_granted',
    'sub_renewed',
    'booster_granted',
    'admin_calibration'
  ) NOT NULL;

ALTER TABLE membership_event
  MODIFY COLUMN source ENUM(
    'self_purchase',
    'b2b_grant',
    'system'
  ) NOT NULL;

-- ── Step 1: Backup snapshot ───────────────────────────────────────────────────
-- Backup trial_grant + credit_cycle current state before any modifications.
--
-- ⚠️ RE-RUN PROTECTION: only INSERT into backup IF the backup table is currently
-- empty. This protects against the case where T8 was already run once (backup
-- holds the genuine pre-T8 state) and is being re-run later (current rows are
-- POST-T8 state — re-snapshotting would overwrite the pre-T8 backup, destroying
-- our ability to roll back). On the first run, the backup is empty, NOT EXISTS
-- is true, INSERT proceeds. On re-runs, NOT EXISTS is false, INSERT is a no-op.
CREATE TABLE IF NOT EXISTS trial_grant_backup_t8 LIKE trial_grant;
INSERT INTO trial_grant_backup_t8
SELECT * FROM trial_grant
WHERE NOT EXISTS (SELECT 1 FROM trial_grant_backup_t8 LIMIT 1);

CREATE TABLE IF NOT EXISTS credit_cycle_backup_t8 LIKE credit_cycle;
INSERT INTO credit_cycle_backup_t8
SELECT * FROM credit_cycle
WHERE NOT EXISTS (SELECT 1 FROM credit_cycle_backup_t8 LIMIT 1);

SELECT 'BACKUP COMPLETE' AS status,
       (SELECT COUNT(*) FROM trial_grant_backup_t8) AS trial_grant_rows_backed_up,
       (SELECT COUNT(*) FROM credit_cycle_backup_t8) AS credit_cycle_rows_backed_up;
-- Note: row counts reflect the ORIGINAL pre-T8 snapshot on first run, and are
-- unchanged on re-runs (since the NOT EXISTS guard skipped the INSERT).

-- ── Step 2: Pre-check invariants ─────────────────────────────────────────────
-- Pre-check A: source_type must be 100% backfilled for non-debt rows (T1 precondition).
-- Debt rows (package_id=0, reconcile_debt operations) intentionally have NULL source_type.
-- Only rows that joined credit_package via T1 backfill should be non-NULL.
SELECT 'pre_check_A_source_type_null_non_debt' AS check_name,
       COUNT(*) AS null_rows_excluding_debt
FROM credit_transaction
WHERE source_type IS NULL
  AND package_id != 0;
-- Expected: 0. If >0, T1 backfill incomplete — DO NOT proceed with calibration.

-- Pre-check B: show expired trial rows with positive credits_remaining (calibration targets).
SELECT 'pre_check_B_expired_trial_targets' AS check_name,
       user_id,
       credits_remaining AS current_remaining,
       expires_at
FROM trial_grant
WHERE expires_at < NOW()
  AND credits_remaining > 0;
-- Expected on dev: 2 rows (user_id 55, 54 — both expired 2026-03-31 with 200 remaining each).
-- Expected on prod: ~30 rows.

-- Pre-check C: show in-period trial rows with ledger drift.
SELECT 'pre_check_C_active_trial_drift' AS check_name,
       tg.user_id,
       tg.credits_remaining AS current_remaining,
       GREATEST(200 - COALESCE(SUM(-ct.amount), 0), 0) AS computed_remaining,
       tg.credits_remaining - GREATEST(200 - COALESCE(SUM(-ct.amount), 0), 0) AS delta,
       tg.expires_at
FROM trial_grant tg
LEFT JOIN credit_transaction ct
  ON ct.user_id = tg.user_id
  AND ct.source_type = 'trial'
  AND ct.amount < 0
WHERE tg.expires_at >= NOW()
GROUP BY tg.user_id, tg.credits_remaining, tg.expires_at
HAVING tg.credits_remaining != GREATEST(200 - COALESCE(SUM(-ct.amount), 0), 0);
-- Expected on dev: 0 rows (user 61 is clean: 200 remaining, 0 deducted by ledger).

-- Pre-check D: show in-period credit_cycle rows with ledger drift.
-- FORMULA: computed_remaining = GREATEST(credits_granted + SUM(all amounts), 0)
-- This uses the NET ledger sum (deductions + refunds), not deduction-only.
-- Refunds (positive amount rows from Reconcile) must be included to get the true balance.
SELECT 'pre_check_D_active_cycle_drift' AS check_name,
       cc.id AS cycle_id,
       cc.user_id,
       cc.credits_granted,
       cc.credits_remaining AS current_remaining,
       GREATEST(cc.credits_granted + COALESCE(SUM(ct.amount), 0), 0) AS computed_remaining,
       cc.credits_remaining - GREATEST(cc.credits_granted + COALESCE(SUM(ct.amount), 0), 0) AS delta,
       cc.cycle_end
FROM credit_cycle cc
LEFT JOIN credit_transaction ct
  ON ct.user_id = cc.user_id
  AND ct.source_id = cc.id
  AND ct.source_type = 'cycle'
WHERE cc.cycle_end > NOW()
GROUP BY cc.id, cc.user_id, cc.credits_granted, cc.credits_remaining, cc.cycle_end
HAVING cc.credits_remaining != GREATEST(cc.credits_granted + COALESCE(SUM(ct.amount), 0), 0);
-- Expected on dev:
--   cycle_id=4 (user 62): current=1949, computed=2000, delta=-51 (pre-T1 deductions not in ledger)
--   cycle_id=6 (user 25): current=1997, computed=1997, delta=0 (no drift — refund already reflected)
-- NOTE: cycle_id=6 shows NO drift because the refund (+69) correctly offsets the deduction (-72).
-- On prod: unknown drift rows.

-- ── Step 3–5: Calibration UPDATEs (atomic transaction) ───────────────────────
START TRANSACTION;

-- Step 3: Force expired trial_grant.credits_remaining = 0.
-- Rows: expires_at < NOW() AND credits_remaining > 0.
-- On dev: affects user_id 55, 54.
-- On prod: affects ~30 rows.
-- Idempotent: WHERE guard credits_remaining > 0 ensures re-run is no-op.
UPDATE trial_grant
SET credits_remaining = 0
WHERE expires_at < NOW()
  AND credits_remaining > 0;

-- Capture rows affected (approximate via subsequent SELECT for logging purposes).
-- MySQL does not support ROW_COUNT() in SELECT directly; affectedness logged below.

-- Step 4: Re-base in-period trial_grant.credits_remaining to ledger.
-- Formula: GREATEST(200 - SUM(-amount WHERE source_type='trial' AND amount<0), 0)
-- Only updates rows where current != computed (idempotent).
-- Handles the case where a user has NO trial credit_transaction rows (0 deducted = 200 remaining).
UPDATE trial_grant tg
LEFT JOIN (
  SELECT ct.user_id,
         GREATEST(200 - COALESCE(SUM(-ct.amount), 0), 0) AS computed_remaining
    FROM credit_transaction ct
   WHERE ct.source_type = 'trial'
     AND ct.amount < 0
   GROUP BY ct.user_id
) calc ON calc.user_id = tg.user_id
SET tg.credits_remaining = COALESCE(calc.computed_remaining, 200)
-- COALESCE: if no ledger rows exist for this user (never deducted), computed = 200
WHERE tg.expires_at >= NOW()
  AND tg.credits_remaining != COALESCE(calc.computed_remaining, 200);

-- Step 5: Re-base in-period credit_cycle.credits_remaining to ledger.
-- FORMULA: GREATEST(credits_granted + SUM(all amounts), 0)
-- This uses the NET ledger sum (deductions + refunds), not deduction-only.
-- Reason: Reconcile path writes positive-amount refund rows when actual usage < reserved amount.
-- Including refunds gives the true running balance: granted - deducted + refunded.
-- On dev: affects cycle_id=4 (user 62): 2000+0=2000 (was 1949, +51 fix — pre-T1 drift)
--         cycle_id=6 (user 25): 2000-72+69=1997 (already correct — no change needed)
UPDATE credit_cycle cc
LEFT JOIN (
  SELECT ct.user_id,
         ct.source_id AS cycle_id,
         GREATEST(cc2.credits_granted + COALESCE(SUM(ct.amount), 0), 0) AS computed_remaining
    FROM credit_transaction ct
    JOIN credit_cycle cc2 ON cc2.id = ct.source_id AND cc2.user_id = ct.user_id
   WHERE ct.source_type = 'cycle'
     AND cc2.cycle_end > NOW()
   GROUP BY ct.user_id, ct.source_id, cc2.credits_granted
) calc ON calc.user_id = cc.user_id AND calc.cycle_id = cc.id
-- LEFT JOIN handles cycles with NO ledger rows at all (user 62: 0 transactions → computed = credits_granted)
SET cc.credits_remaining = CASE
  WHEN calc.computed_remaining IS NOT NULL THEN calc.computed_remaining
  ELSE cc.credits_granted  -- No ledger rows = fully unused = restore to credits_granted
END
WHERE cc.cycle_end > NOW()
  AND cc.credits_remaining != CASE
    WHEN calc.computed_remaining IS NOT NULL THEN calc.computed_remaining
    ELSE cc.credits_granted
  END;

COMMIT;

-- ── Step 6: Post-state summary ────────────────────────────────────────────────
SELECT 'POST_STATE_trial_grant' AS section;
SELECT user_id, credits_remaining, expires_at,
       CASE WHEN expires_at < NOW() THEN 'EXPIRED' ELSE 'ACTIVE' END AS status
FROM trial_grant;

SELECT 'POST_STATE_credit_cycle_active' AS section;
SELECT id AS cycle_id, user_id, credits_granted, credits_remaining, cycle_start, cycle_end
FROM credit_cycle WHERE cycle_end > NOW();

-- ── Step 7: Post-check invariants (all 8 from spec §6 03-verify.sql) ─────────
SELECT '=== T8 POST-CALIBRATION INVARIANT CHECK (8 invariants) ===' AS section;
SELECT 'All violation_count must be 0.' AS note;

-- I1: No negative booster balance
SELECT 'I1_no_negative_booster_balance' AS check_name, COUNT(*) AS violation_count
FROM user_booster_balance
WHERE credits_remaining < 0;
-- Expected: 0

-- I2: subscription.expires_at matches latest subscription credit_package expiry
SELECT 'I2_sub_expires_at_matches_latest_pkg' AS check_name, COUNT(*) AS violation_count
FROM subscription s
INNER JOIN (
  SELECT user_id, MAX(expires_at) AS max_pkg_expires
  FROM credit_package
  WHERE type = 'subscription'
    AND status IN ('active', 'exhausted', 'expired', 'pending')
  GROUP BY user_id
) latest ON latest.user_id = s.user_id
WHERE ABS(TIMESTAMPDIFF(SECOND, s.expires_at, latest.max_pkg_expires)) > 1;
-- Expected: 0

-- I3: trial_grant.credits_remaining matches credit_package remain_credits for earliest trial pkg
SELECT 'I3_trial_credits_remaining_matches_pkg' AS check_name, COUNT(*) AS violation_count
FROM trial_grant tg
INNER JOIN (
  SELECT cp.user_id, cp.remain_credits
  FROM credit_package cp
  INNER JOIN (
    SELECT user_id, MIN(activated_at) AS min_at
    FROM credit_package
    WHERE type = 'trial'
    GROUP BY user_id
  ) earliest ON earliest.user_id = cp.user_id AND cp.activated_at = earliest.min_at
    AND cp.type = 'trial'
) expected ON expected.user_id = tg.user_id
WHERE tg.credits_remaining <> expected.remain_credits;
-- NOTE: After T8 calibration, trial_grant.credits_remaining is ledger-based,
-- NOT credit_package.remain_credits based. This invariant may show violations
-- for expired rows (which we zeroed) if credit_package still shows old remain_credits.
-- This is expected and correct: ledger is now SOT, credit_package is legacy reference.
-- The violation here indicates the TRANSITION STATE between old and new SOT.
-- Expected: violations for expired rows that we zeroed (was: 200, now: 0).

-- I4: user_booster_balance.credits_remaining = SUM of active booster pkg remain
SELECT 'I4_booster_balance_sum_matches_active_pkgs' AS check_name, COUNT(*) AS violation_count
FROM user_booster_balance ubb
INNER JOIN (
  SELECT user_id,
    SUM(CASE WHEN status = 'active' AND expires_at > NOW()
             THEN remain_credits ELSE 0 END) AS active_sum
  FROM credit_package
  WHERE type = 'booster'
  GROUP BY user_id
) pkg_sum ON pkg_sum.user_id = ubb.user_id
WHERE ubb.credits_remaining <> pkg_sum.active_sum;
-- Expected: 0

-- I5a: membership_event count >= credit_package row count (migration events present)
SELECT 'I5a_event_count_ge_package_count' AS check_name, COUNT(*) AS violation_count
FROM (
  SELECT
    (SELECT COUNT(*) FROM membership_event
     WHERE idempotency_key LIKE 'migration-20260430-cp-%') AS event_count,
    (SELECT COUNT(*) FROM credit_package) AS pkg_count
) counts
WHERE event_count < pkg_count;
-- Expected: 0

-- I6: trial_grant.user_id is UNIQUE (one trial per user)
SELECT 'I6_trial_grant_user_id_unique' AS check_name, COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM trial_grant
  GROUP BY user_id
  HAVING cnt > 1
) dup;
-- Expected: 0

-- I7: subscription.user_id is UNIQUE (one subscription row per user)
SELECT 'I7_subscription_user_id_unique' AS check_name, COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM subscription
  GROUP BY user_id
  HAVING cnt > 1
) dup;
-- Expected: 0

-- I8: No orphan membership_event rows (event.user_id must exist in user table)
SELECT 'I8_no_orphan_membership_events' AS check_name, COUNT(*) AS violation_count
FROM membership_event me
LEFT JOIN user u ON u.id = me.user_id
WHERE u.id IS NULL;
-- Expected: 0

-- T8-specific additional checks (ledger convergence)
SELECT 'T8_I9_no_expired_trial_positive_remaining' AS check_name, COUNT(*) AS violation_count
FROM trial_grant
WHERE expires_at < NOW()
  AND credits_remaining > 0;
-- Expected: 0 (key T8 outcome)

SELECT 'T8_I10_active_trial_ledger_convergence' AS check_name, COUNT(*) AS violation_count
FROM trial_grant tg
LEFT JOIN (
  SELECT user_id, GREATEST(200 - COALESCE(SUM(-amount), 0), 0) AS computed
  FROM credit_transaction
  WHERE source_type = 'trial' AND amount < 0
  GROUP BY user_id
) calc ON calc.user_id = tg.user_id
WHERE tg.expires_at >= NOW()
  AND tg.credits_remaining != COALESCE(calc.computed, 200);
-- Expected: 0

SELECT 'T8_I11_active_cycle_ledger_convergence' AS check_name, COUNT(*) AS violation_count
FROM credit_cycle cc
LEFT JOIN (
  SELECT ct.user_id, ct.source_id AS cycle_id,
         GREATEST(cc2.credits_granted + COALESCE(SUM(ct.amount), 0), 0) AS computed
  FROM credit_transaction ct
  JOIN credit_cycle cc2 ON cc2.id = ct.source_id AND cc2.user_id = ct.user_id
  WHERE ct.source_type = 'cycle'
    AND cc2.cycle_end > NOW()
  GROUP BY ct.user_id, ct.source_id, cc2.credits_granted
) calc ON calc.user_id = cc.user_id AND calc.cycle_id = cc.id
WHERE cc.cycle_end > NOW()
  AND cc.credits_remaining != COALESCE(calc.computed, cc.credits_granted);
-- Expected: 0 (uses net formula including refunds)

-- ── Step 8: Audit log ─────────────────────────────────────────────────────────
-- Write a single audit row per calibrated user to membership_event.
-- event_type='admin_calibration' (enum extended in Step 0).
-- source='system' (enum extended in Step 0) — calibration rows are NEITHER
-- self-purchases NOR B2B grants, so we tag them 'system' to exclude them from
-- B2B billing aggregation (which filters by source='b2b_grant').
-- granter_user_id=NULL because no user/admin "granted" anything; this is a
-- ledger calibration, not a credit issuance.
-- idempotency_key is built from @T8_KEY_PREFIX + per-row suffix; the UNIQUE
-- constraint on idempotency_key + INSERT IGNORE makes re-runs no-op.
--
-- NOTE: We insert one audit row per affected user for traceability.
-- For the expired trial users (55, 54), product_type='trial'.
-- For cycle drift users, product_type='monthly'.

INSERT IGNORE INTO membership_event
  (user_id, event_type, product_type, amount_cents, source, granter_user_id, idempotency_key, occurred_at)
-- Audit row for expired trial users that were zeroed
SELECT
  tg.user_id,
  'admin_calibration',
  'trial',
  0,
  'system',
  NULL,
  CONCAT(@T8_KEY_PREFIX, '_trial_', tg.user_id),
  NOW()
FROM trial_grant_backup_t8 tg
WHERE tg.expires_at < NOW()
  AND tg.credits_remaining > 0

UNION ALL

-- Audit row for active trial users that were re-based (if any)
SELECT
  tg.user_id,
  'admin_calibration',
  'trial',
  0,
  'system',
  NULL,
  CONCAT(@T8_KEY_PREFIX, '_trial_active_', tg.user_id),
  NOW()
FROM trial_grant_backup_t8 tg
JOIN trial_grant tg_new ON tg_new.user_id = tg.user_id
WHERE tg.expires_at >= NOW()
  AND tg.credits_remaining != tg_new.credits_remaining

UNION ALL

-- Audit row for cycle users that were re-based
SELECT
  cc.user_id,
  'admin_calibration',
  'monthly',
  0,
  'system',
  NULL,
  CONCAT(@T8_KEY_PREFIX, '_cycle_', cc.id),
  NOW()
FROM credit_cycle_backup_t8 cc
JOIN credit_cycle cc_new ON cc_new.id = cc.id
WHERE cc.cycle_end > NOW()
  AND cc.credits_remaining != cc_new.credits_remaining;

SELECT 'AUDIT ROWS INSERTED' AS status;
SELECT idempotency_key, event_type, product_type, user_id, occurred_at
FROM membership_event
WHERE idempotency_key LIKE CONCAT(@T8_KEY_PREFIX, '%')
ORDER BY idempotency_key;

SELECT 'T8 LEDGER CALIBRATION COMPLETE' AS final_status;
SELECT 'Check all violation_count = 0 above before considering this successful.' AS reminder;

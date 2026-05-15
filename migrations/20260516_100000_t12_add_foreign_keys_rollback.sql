-- T12 — Rollback: Drop FK constraints + CHECK constraint
-- Plan ref: docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md §T12
--
-- Run this only if T12 forward migration needs to be reverted.
-- Note: The orphan DELETEs from the forward migration are NOT reversed here
-- (they deleted invalid data that should not be restored).
-- Dropping the constraints merely re-allows orphan rows to be inserted; it does
-- not restore previously deleted rows.
--
-- Safety: All DROP CONSTRAINT operations are idempotent in the sense that running
-- the rollback when constraints don't exist will error — check INFORMATION_SCHEMA
-- first if re-running after partial rollback.

-- ============================================================
-- Step 1: Pre-check — confirm which constraints are currently active
-- ============================================================
SELECT
  TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE
FROM information_schema.TABLE_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE()
  AND CONSTRAINT_NAME IN ('fk_cycle_subscription', 'fk_item_reservation', 'chk_ct_source_type')
ORDER BY TABLE_NAME;

-- ============================================================
-- Step 2: Drop CHECK constraint on credit_transaction
-- ============================================================
ALTER TABLE credit_transaction
  DROP CHECK chk_ct_source_type;

-- ============================================================
-- Step 3: Drop FK — credit_reservation_item
-- ============================================================
ALTER TABLE credit_reservation_item
  DROP FOREIGN KEY fk_item_reservation;

-- ============================================================
-- Step 4: Drop FK — credit_cycle
-- ============================================================
ALTER TABLE credit_cycle
  DROP FOREIGN KEY fk_cycle_subscription;

-- ============================================================
-- Step 5: Verify constraints are gone
-- ============================================================
SELECT
  'post_rollback_constraints_remaining' AS check_name,
  COUNT(*) AS cnt
FROM information_schema.TABLE_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE()
  AND CONSTRAINT_NAME IN ('fk_cycle_subscription', 'fk_item_reservation', 'chk_ct_source_type');
-- Expected: 0

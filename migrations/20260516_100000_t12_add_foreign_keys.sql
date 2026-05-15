-- T12 — Add hard FK constraints + CHECK constraint
-- Plan ref: docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md §T12
--
-- Prerequisites (must verify before executing):
--   1. T11 complete: credit_package DROP'd, legacy_credit_package_archive_20260515 exists
--   2. subscription / trial_grant / credit_cycle / credit_reservation / credit_reservation_item tables exist
--
-- Purpose: Add referential integrity constraints so future code cannot create
-- orphan rows in credit_cycle or credit_reservation_item.
-- The CHECK constraint on credit_transaction.source_type enforces the polymorphic
-- type enum so that new source_types are always declared explicitly.
--
-- Safety:
--   * Orphan DELETE is idempotent (DELETE WHERE NOT IN is no-op if 0 orphans).
--   * ADD CONSTRAINT names are unique and deterministic; re-running after partial
--     failure will error on the already-applied constraint — handle by checking
--     INFORMATION_SCHEMA before re-running.
--   * NULL is allowed in source_type for:
--       - Legacy debt rows (package_id=0, Reconcile path, pre-T1 backfill)
--       - Rows produced before T1 migration ran on that environment
--   * 'subscription' is included in CHECK because T1 backfill used credit_package.type
--     which had type='subscription' for subscription packages; dev DB has 89 such rows.

-- ============================================================
-- Step 1: Pre-check — count orphans before cleaning
-- (expect 0 on any environment that ran T1-T11 correctly)
-- ============================================================
SELECT
  'pre_check_credit_cycle_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_cycle
WHERE subscription_id NOT IN (SELECT id FROM subscription);

SELECT
  'pre_check_credit_reservation_item_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_reservation_item
WHERE reservation_id NOT IN (SELECT id FROM credit_reservation);

SELECT
  'pre_check_credit_transaction_invalid_source_type' AS check_name,
  COUNT(*) AS invalid_count
FROM credit_transaction
WHERE source_type IS NOT NULL
  AND source_type NOT IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system');

-- ============================================================
-- Step 2: Clean orphans (so FK ADD does not fail)
-- Expect 0 rows deleted on clean environments; included for safety.
-- ============================================================
DELETE FROM credit_cycle
WHERE subscription_id NOT IN (SELECT id FROM subscription);

DELETE FROM credit_reservation_item
WHERE reservation_id NOT IN (SELECT id FROM credit_reservation);

-- ============================================================
-- Step 3: Post-clean orphan counts (must be 0 before proceeding)
-- ============================================================
SELECT
  'post_clean_credit_cycle_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_cycle
WHERE subscription_id NOT IN (SELECT id FROM subscription);

SELECT
  'post_clean_credit_reservation_item_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_reservation_item
WHERE reservation_id NOT IN (SELECT id FROM credit_reservation);

-- ============================================================
-- Step 4: Add FK — credit_cycle.subscription_id → subscription.id
-- ON DELETE CASCADE: deleting a subscription row cascades to its credit_cycles.
-- This is safe because a subscription without credit_cycles is an orphan state
-- and credit_cycles are regenerated lazily per billing month.
-- ============================================================
ALTER TABLE credit_cycle
  ADD CONSTRAINT fk_cycle_subscription
  FOREIGN KEY (subscription_id) REFERENCES subscription(id) ON DELETE CASCADE;

-- ============================================================
-- Step 5: Add FK — credit_reservation_item.reservation_id → credit_reservation.id
-- ON DELETE CASCADE: deleting a reservation cascades to its line items.
-- This enforces the parent-child relationship in the Reserve/Reconcile two-phase
-- credit deduction protocol.
-- ============================================================
ALTER TABLE credit_reservation_item
  ADD CONSTRAINT fk_item_reservation
  FOREIGN KEY (reservation_id) REFERENCES credit_reservation(id) ON DELETE CASCADE;

-- ============================================================
-- Step 6: Add CHECK constraint — credit_transaction.source_type enum
-- Validates the polymorphic discriminator. NULL is allowed (see §Safety above).
-- Includes 'subscription' because T1 backfill used credit_package.type which
-- included 'subscription' as a valid type value (dev: 89 rows; prod: verify).
-- ============================================================
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system')
         OR source_type IS NULL);

-- ============================================================
-- Step 7: Post-run verification
-- ============================================================
-- Verify FKs are active:
SELECT
  TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE
FROM information_schema.TABLE_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE()
  AND CONSTRAINT_NAME IN ('fk_cycle_subscription', 'fk_item_reservation', 'chk_ct_source_type')
ORDER BY TABLE_NAME;
-- Expected: 3 rows (2 FOREIGN KEY + 1 CHECK)

-- Verify 0 orphans remain:
SELECT
  'final_credit_cycle_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_cycle
WHERE subscription_id NOT IN (SELECT id FROM subscription);

SELECT
  'final_credit_reservation_item_orphans' AS check_name,
  COUNT(*) AS orphan_count
FROM credit_reservation_item
WHERE reservation_id NOT IN (SELECT id FROM credit_reservation);

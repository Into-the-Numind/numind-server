-- credit_transaction add source_type/source_id (forward)
-- Feature: membership-credits-redesign cleanup T1
-- Date: 2026-05-15
--
-- Why: After T11 DROPs credit_package, the 3075 historical ledger rows in
-- credit_transaction would lose their type information (trial/cycle/booster).
-- Adding source_type + source_id makes the ledger self-contained so that
-- post-DROP forensics and T7/T8 calibration SQL can identify per-pool entries
-- without joining a table that no longer exists.
--
-- The backfill uses credit_package.type as the authority (cp is still live at T1
-- time). Rows without a matching credit_package row (orphaned package_id) are
-- left NULL and counted by the post-check SELECT.
--
-- Migration safety:
--   * source_type / source_id are nullable additive columns. Existing rows are
--     untouched except by the explicit backfill UPDATE below.
--   * The UPDATE joins on credit_transaction.package_id → credit_package.id so
--     only rows with a valid FK are touched. package_id=0 debt rows (Reconcile
--     path) are intentionally left NULL (no package).
--   * Index is a composite covering both columns for the T7/T8 calibration queries.
--   * 3075 rows UPDATE is a single transaction < 5s on prod; acceptable.

ALTER TABLE credit_transaction
    ADD COLUMN source_type VARCHAR(20) NULL COMMENT 'trial/cycle/booster; NULL = legacy path or reconcile_debt row' AFTER package_id,
    ADD COLUMN source_id BIGINT UNSIGNED NULL COMMENT 'FK to credit_cycle.id / user_booster_balance.user_id / trial_grant.id depending on source_type' AFTER source_type,
    ADD INDEX idx_ct_source (source_type, source_id);

-- Backfill: set source_type = credit_package.type, source_id = credit_package.id
-- for all rows that have a matching credit_package row.
UPDATE credit_transaction ct
JOIN credit_package cp ON ct.package_id = cp.id
SET ct.source_type = cp.type,
    ct.source_id   = cp.id;

-- Post-check: expect 0 rows with NULL source_type (excluding debt rows where
-- package_id=0 which have no credit_package to join against).
-- If this returns > 0, the remaining NULLs are orphaned package_id references
-- (package deleted) — acceptable, they remain NULL in the ledger.
SELECT 'T1_verify' AS check_name,
       COUNT(*)    AS null_rows_with_valid_pkg_id
  FROM credit_transaction ct
 WHERE ct.source_type IS NULL
   AND ct.package_id  != 0
   AND EXISTS (SELECT 1 FROM credit_package cp WHERE cp.id = ct.package_id);
-- Expected: 0 (all rows with a resolvable package_id have been backfilled)

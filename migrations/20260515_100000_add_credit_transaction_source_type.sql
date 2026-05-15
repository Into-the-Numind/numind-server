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
--   * 3075 rows UPDATE is wrapped in a transaction so a mid-run crash leaves
--     the table cleanly at the pre-backfill state (columns added, all NULL).
--     Re-running the UPDATE is safe because of the WHERE source_type IS NULL guard.
--   * ADD COLUMN IF NOT EXISTS (MySQL 8.0.29+) makes the DDL idempotent on re-run.
--   * ADD INDEX does not support IF NOT EXISTS in MySQL; document below how to
--     handle a duplicate-index error on re-run.

-- ── DDL: idempotent via IF NOT EXISTS (MySQL 8.0.29+) ────────────────────────
ALTER TABLE credit_transaction
    ADD COLUMN IF NOT EXISTS source_type VARCHAR(20) NULL COMMENT 'trial/cycle/booster; NULL = legacy path or reconcile_debt row' AFTER package_id,
    ADD COLUMN IF NOT EXISTS source_id BIGINT UNSIGNED NULL COMMENT 'FK to credit_cycle.id / user_booster_balance.user_id / trial_grant.id depending on source_type' AFTER source_type;

-- Note: ADD INDEX does not support IF NOT EXISTS in MySQL. If re-running this
-- migration after a partial failure, suppress the duplicate-key error with:
--   SET @idx_exists = (SELECT COUNT(*) FROM information_schema.STATISTICS
--     WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='credit_transaction'
--     AND INDEX_NAME='idx_ct_source');
-- For simplicity, re-running operators should DROP INDEX first if it exists.
ALTER TABLE credit_transaction ADD INDEX idx_ct_source (source_type, source_id);

-- ── DML: wrap in transaction for atomic backfill ──────────────────────────────
-- Idempotent: WHERE source_type IS NULL ensures only unfilled rows are updated.
-- Re-running after a partial failure is safe — already-filled rows are skipped.
START TRANSACTION;
UPDATE credit_transaction ct
JOIN credit_package cp ON ct.package_id = cp.id
SET ct.source_type = cp.type,
    ct.source_id   = cp.id
WHERE ct.source_type IS NULL;
COMMIT;

-- ── Post-check: expect 0 null rows ───────────────────────────────────────────
-- Counts ALL rows with NULL source_type to give a complete picture.
-- Expected: 0 for valid-package rows; debt rows (package_id=0, no credit_package
-- to join against) will remain NULL by design and are excluded by T7/T8 queries
-- that filter on source_type IS NOT NULL.
SELECT COUNT(*) AS null_rows FROM credit_transaction WHERE source_type IS NULL;
-- Expected: 0

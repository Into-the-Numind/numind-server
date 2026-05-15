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
--   * v2 fix (prod deploy 2026-05-16): MySQL does NOT support `ADD COLUMN IF NOT EXISTS`
--     (MariaDB only). v1 attempted this syntax and errored with ERROR 1064 on prod's
--     mysql:8.4.2 image. v2 uses information_schema pre-check + prepared statements
--     to achieve idempotency on plain MySQL.
--   * Note: in prod, GORM AutoMigrate runs at backend startup and adds these columns
--     automatically once Go model declares them. The DDL here is a fallback for
--     fresh-DB rebuilds without backend bootstrap (e.g., disaster recovery).

-- ── DDL: idempotent via information_schema pre-check (MySQL-compatible) ──────
-- Add source_type column only if missing
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
                      WHERE table_schema = DATABASE()
                        AND table_name = 'credit_transaction'
                        AND column_name = 'source_type');
SET @sql := IF(@col_exists = 0,
               'ALTER TABLE credit_transaction ADD COLUMN source_type VARCHAR(20) NULL COMMENT ''trial/cycle/booster; NULL = legacy path or reconcile_debt row'' AFTER package_id',
               'SELECT ''source_type already exists, skipping (idempotent)'' AS info');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Add source_id column only if missing
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
                      WHERE table_schema = DATABASE()
                        AND table_name = 'credit_transaction'
                        AND column_name = 'source_id');
SET @sql := IF(@col_exists = 0,
               'ALTER TABLE credit_transaction ADD COLUMN source_id BIGINT UNSIGNED NULL COMMENT ''FK to credit_cycle.id / user_booster_balance.user_id / trial_grant.id depending on source_type'' AFTER source_type',
               'SELECT ''source_id already exists, skipping (idempotent)'' AS info');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Add composite index only if missing
SET @idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
                      WHERE table_schema = DATABASE()
                        AND table_name = 'credit_transaction'
                        AND index_name = 'idx_ct_source');
SET @sql := IF(@idx_exists = 0,
               'ALTER TABLE credit_transaction ADD INDEX idx_ct_source (source_type, source_id)',
               'SELECT ''idx_ct_source already exists, skipping (idempotent)'' AS info');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ── DML: wrap in transaction for atomic backfill ──────────────────────────────
-- Idempotent (2 layers):
--   1. WHERE source_type IS NULL ensures only unfilled rows are updated on re-run.
--   2. Check credit_package table exists — if T11 already DROPped it, the backfill
--      was completed earlier (T1 must run before T11), so skip gracefully.
SET @cp_exists := (SELECT COUNT(*) FROM information_schema.TABLES
                     WHERE table_schema = DATABASE()
                       AND table_name = 'credit_package');
SET @sql := IF(@cp_exists > 0,
               'UPDATE credit_transaction ct JOIN credit_package cp ON ct.package_id = cp.id SET ct.source_type = cp.type, ct.source_id = cp.id WHERE ct.source_type IS NULL',
               'SELECT ''credit_package already dropped (T11 ran); backfill was completed earlier — skipping (idempotent)'' AS info');
START TRANSACTION;
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
COMMIT;

-- ── Post-check: expect 0 null rows ───────────────────────────────────────────
-- Counts ALL rows with NULL source_type to give a complete picture.
-- Expected: 0 for valid-package rows; debt rows (package_id=0, no credit_package
-- to join against) will remain NULL by design and are excluded by T7/T8 queries
-- that filter on source_type IS NOT NULL.
SELECT COUNT(*) AS null_rows FROM credit_transaction WHERE source_type IS NULL;
-- Expected: 0

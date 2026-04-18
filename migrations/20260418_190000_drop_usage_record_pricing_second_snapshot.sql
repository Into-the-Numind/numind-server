-- Migration: drop usage_record.pricing_second_snapshot
--
-- This column was never populated: the billing path that would have used it
-- (per_second pricing) was never wired end-to-end because pricing_rule has no
-- price_per_second column. ASR billing uses per_call via pricing_rule.
-- DurationSeconds is intentionally retained as business-analysis metadata.
--
-- Idempotent: guarded by INFORMATION_SCHEMA.COLUMNS check.
--
-- DO NOT execute on dev until T-arch is merged to develop.

SET @col_exists = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME   = 'usage_record'
      AND COLUMN_NAME  = 'pricing_second_snapshot'
);

SET @sql = IF(
    @col_exists > 0,
    'ALTER TABLE usage_record DROP COLUMN pricing_second_snapshot',
    'SELECT "column pricing_second_snapshot already dropped" AS note'
);

PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- =============================================================================
-- ROLLBACK SQL (run manually if needed)
-- =============================================================================
-- ALTER TABLE usage_record
--   ADD COLUMN pricing_second_snapshot DECIMAL(10,6) DEFAULT NULL
--     COMMENT 'records the per-second price at time of call (restored from rollback)'
--     AFTER pricing_call_snapshot;

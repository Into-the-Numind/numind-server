-- Rollback for 20260619_100100_add_cache_creation_tokens_to_usage_record.sql
-- IDEMPOTENT: MySQL 8.x has no `DROP COLUMN IF EXISTS` → information_schema + PREPARE.

SET @c = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record'
    AND COLUMN_NAME = 'cache_creation_tokens');
SET @ddl = IF(@c = 1,
  'ALTER TABLE usage_record DROP COLUMN cache_creation_tokens',
  'SELECT ''usage_record.cache_creation_tokens absent'' AS noop');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

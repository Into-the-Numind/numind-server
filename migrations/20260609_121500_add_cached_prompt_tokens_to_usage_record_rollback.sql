-- Rollback for 20260609_121500_add_cached_prompt_tokens_to_usage_record.sql
-- IDEMPOTENT: MySQL 8.x has no `DROP COLUMN IF EXISTS` → information_schema + PREPARE.

SET @c = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record'
    AND COLUMN_NAME = 'cached_prompt_tokens');
SET @ddl = IF(@c = 1,
  'ALTER TABLE usage_record DROP COLUMN cached_prompt_tokens',
  'SELECT ''usage_record.cached_prompt_tokens absent'' AS noop');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

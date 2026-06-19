-- Rollback for 20260619_100200_add_ai_service_prompt_cache_policy.sql
-- Drops the prompt_cache_policy column from ai_service.
-- IDEMPOTENT: MySQL 8.x has no `DROP COLUMN IF EXISTS` → information_schema + PREPARE.

SET @c1 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service'
    AND COLUMN_NAME = 'prompt_cache_policy');
SET @ddl1 = IF(@c1 = 1,
  'ALTER TABLE ai_service DROP COLUMN prompt_cache_policy',
  'SELECT ''ai_service.prompt_cache_policy absent'' AS noop');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

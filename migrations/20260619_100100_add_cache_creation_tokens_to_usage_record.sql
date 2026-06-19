-- Cache-creation (cache-WRITE) token observability column on usage_record.
--
-- Distinct from cached_prompt_tokens (cache-READ hits): this is the Anthropic
-- cache_creation_input_tokens bucket, written ONLY by the native Claude adapter.
-- Needed for the b2b-billing-report audit trail and per-call observability of the
-- creation/read split (D9). Cost is already captured by the calculator; this is
-- the persisted token count.
--
-- Additive: NOT NULL DEFAULT 0 = no cache creation = identical to pre-cache
-- billing audit. BIGINT to match GORM AutoMigrate
-- (model.UsageRecord.CacheCreationTokens int → bigint).
--
-- IDEMPOTENT: MySQL 8.x has no `ADD COLUMN IF NOT EXISTS` → information_schema + PREPARE.
-- GORM AutoMigrate also adds this column on startup; this is the explicit-apply DDL.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

SET @c = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record'
    AND COLUMN_NAME = 'cache_creation_tokens');
SET @ddl = IF(@c = 0,
  'ALTER TABLE usage_record ADD COLUMN cache_creation_tokens BIGINT NOT NULL DEFAULT 0 COMMENT ''缓存创建（写入）的输入 tokens 数（来自 Anthropic usage.cache_creation_input_tokens）。0=无缓存创建'' AFTER cached_prompt_tokens',
  'SELECT ''usage_record.cache_creation_tokens already exists'' AS noop');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

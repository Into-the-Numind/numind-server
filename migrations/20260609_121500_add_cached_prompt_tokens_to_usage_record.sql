-- Cached prompt-token observability column on usage_record.
--
-- Additive: NOT NULL DEFAULT 0 = no cache = identical to pre-cache billing audit.
-- Source: OpenAI usage.prompt_tokens_details.cached_tokens; DeepSeek
-- prompt_cache_hit_tokens (both via the OpenAI-compatible DMXAPI endpoint).
-- BIGINT to match GORM AutoMigrate (model.UsageRecord.CachedPromptTokens int → bigint).
--
-- IDEMPOTENT: MySQL 8.x has no `ADD COLUMN IF NOT EXISTS` → information_schema + PREPARE.
-- GORM AutoMigrate also adds this column on startup; this is the explicit-apply DDL.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

SET @c = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record'
    AND COLUMN_NAME = 'cached_prompt_tokens');
SET @ddl = IF(@c = 0,
  'ALTER TABLE usage_record ADD COLUMN cached_prompt_tokens BIGINT NOT NULL DEFAULT 0 COMMENT ''缓存命中的输入 tokens 数（来自 provider usage）。0=无缓存命中''',
  'SELECT ''usage_record.cached_prompt_tokens already exists'' AS noop');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

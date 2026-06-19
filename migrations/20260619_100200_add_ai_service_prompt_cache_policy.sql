-- Add the per-model prompt-cache policy column to ai_service (Layer 2 of the
-- 3-layer provider-native cache toggle — see native-cache-adapters-spec §4 D6 / §5E).
--
-- prompt_cache_policy enum: off | claude_ephemeral | gemini_implicit | auto.
--   off              => never use provider-native caching (safe default)
--   claude_ephemeral => Claude cache_control:{ephemeral} when all 3 toggle layers agree
--   gemini_implicit  => route to the Gemini native adapter (implicit cache, no premium)
--   auto             => either path, model-appropriate
-- Caching economics are a property of the MODEL, so the durable knob lives on
-- ai_service next to thinking_style. Mirrored onto ResolvedRoute (registry.go) via
-- the same 3-path JOIN-SELECT pattern thinking_style already follows.
--
-- NOT NULL DEFAULT 'off' is REQUIRED (review finding #8): a NULLable column would let
-- existing rows read NULL, which the resolver treats as "off" (safe) but pollutes
-- admin reads. The explicit NOT NULL DEFAULT 'off' makes intent unambiguous for new
-- inserts; the backfill UPDATE below repairs any rows that predate the column (e.g. if
-- GORM AutoMigrate added it NULLable before this DDL ran).
--
-- IDEMPOTENT: MySQL 8.x does NOT support `ADD COLUMN IF NOT EXISTS` (MariaDB syntax —
-- errors 1064 on MySQL 8.4). Use information_schema pre-check + PREPARE, matching the
-- repo convention (20260609_121500_add_cached_input_price.sql,
-- 20260619_100000_add_cache_creation_price.sql). The backfill UPDATE is naturally
-- idempotent (re-running on already-'off' rows is a no-op).
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

SET @c1 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service'
    AND COLUMN_NAME = 'prompt_cache_policy');
SET @ddl1 = IF(@c1 = 0,
  'ALTER TABLE ai_service ADD COLUMN prompt_cache_policy VARCHAR(16) NOT NULL DEFAULT ''off'' COMMENT ''供应商原生 prompt 缓存策略：off | claude_ephemeral | gemini_implicit | auto。默认 off=不缓存'' AFTER thinking_style',
  'SELECT ''ai_service.prompt_cache_policy already exists'' AS noop');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

-- Backfill (finding #8): guarantee pre-existing rows carry the safe 'off' value
-- whether the column was added by this migration (NOT NULL DEFAULT handles new rows)
-- or earlier by GORM AutoMigrate as NULLable (which would leave old rows NULL/'').
UPDATE ai_service
  SET prompt_cache_policy = 'off'
  WHERE prompt_cache_policy IS NULL OR prompt_cache_policy = '';

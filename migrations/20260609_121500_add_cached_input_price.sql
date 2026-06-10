-- Add cache-hit input price columns to pricing_rule.
--
-- NULLABLE on purpose: NULL = "cached price not configured" => the cached portion
-- of prompt tokens is billed at the FULL input price, making cost byte-identical
-- to pre-cache behavior. Unit: ¥ per 1,000,000 tokens, matching input_price_per_m_tok.
--
-- PAIRED COLUMNS (do not split): cached_input_price_per_m_tok (cost) and
-- sell_cached_input_price_per_m_tok (sell) are logically coupled — set both or
-- leave both NULL. The Go billing code is independently NULL-safe on each side.
--
-- IDEMPOTENT: MySQL 8.x does NOT support `ADD COLUMN IF NOT EXISTS` (MariaDB
-- syntax — errors 1064 on MySQL 8.4). Use information_schema pre-check + PREPARE,
-- matching the repo convention (20260603_140000_agent_run_add_is_test.sql).
-- GORM AutoMigrate (model.PricingRule) also adds these columns on startup; this
-- file is the canonical idempotent DDL for environments that apply SQL explicitly.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

SET @c1 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'cached_input_price_per_m_tok');
SET @ddl1 = IF(@c1 = 0,
  'ALTER TABLE pricing_rule ADD COLUMN cached_input_price_per_m_tok DECIMAL(10,4) NULL COMMENT ''成本价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 input 计费'' AFTER sell_output_price_per_m_tok',
  'SELECT ''pricing_rule.cached_input_price_per_m_tok already exists'' AS noop');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

SET @c2 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'sell_cached_input_price_per_m_tok');
SET @ddl2 = IF(@c2 = 0,
  'ALTER TABLE pricing_rule ADD COLUMN sell_cached_input_price_per_m_tok DECIMAL(10,4) NULL COMMENT ''售价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 sell_input 计费'' AFTER cached_input_price_per_m_tok',
  'SELECT ''pricing_rule.sell_cached_input_price_per_m_tok already exists'' AS noop');
PREPARE s2 FROM @ddl2; EXECUTE s2; DEALLOCATE PREPARE s2;

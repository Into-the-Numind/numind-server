-- Add cache-CREATION (cache-WRITE) input price columns to pricing_rule.
--
-- These are a DISTINCT bucket from the cache-HIT (read) price columns added in
-- 20260609_121500_add_cached_input_price.sql. Anthropic prompt caching returns
-- three disjoint prompt-side token buckets: uncached input, cache-READ (a
-- DISCOUNT, priced by cached_input_price_per_m_tok), and cache-CREATION (a
-- PREMIUM — opus ~1.84×, sonnet ~1.25× over input). A premium CANNOT reuse the
-- discount column or billing would silently under-bill, so it gets its own pair.
--
-- NULLABLE on purpose: NULL = "creation price not configured" => cache-creation
-- tokens are billed at the FULL input price (no premium), making cost
-- byte-identical to pre-cache behavior. Unit: ¥ per 1,000,000 tokens, matching
-- input_price_per_m_tok.
--
-- PAIRED COLUMNS (do not split): cache_creation_input_price_per_m_tok (cost) and
-- sell_cache_creation_input_price_per_m_tok (sell) are logically coupled — set
-- both or leave both NULL. The Go billing code (pricing.CalculateCostWithCacheRW
-- + recorder.computeRevenue 3-bucket carve) is independently NULL-safe on each side.
--
-- IDEMPOTENT: MySQL 8.x does NOT support `ADD COLUMN IF NOT EXISTS` (MariaDB
-- syntax — errors 1064 on MySQL 8.4). Use information_schema pre-check + PREPARE,
-- matching the repo convention (20260609_121500_add_cached_input_price.sql).
-- GORM AutoMigrate (model.PricingRule) also adds these columns on startup; this
-- file is the canonical idempotent DDL for environments that apply SQL explicitly.
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

SET @c1 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'cache_creation_input_price_per_m_tok');
SET @ddl1 = IF(@c1 = 0,
  'ALTER TABLE pricing_rule ADD COLUMN cache_creation_input_price_per_m_tok DECIMAL(10,4) NULL COMMENT ''成本价：每百万缓存创建（写入）输入 tokens（元）。NULL=未设置，按全价 input 计费'' AFTER sell_cached_input_price_per_m_tok',
  'SELECT ''pricing_rule.cache_creation_input_price_per_m_tok already exists'' AS noop');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

SET @c2 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'sell_cache_creation_input_price_per_m_tok');
SET @ddl2 = IF(@c2 = 0,
  'ALTER TABLE pricing_rule ADD COLUMN sell_cache_creation_input_price_per_m_tok DECIMAL(10,4) NULL COMMENT ''售价：每百万缓存创建（写入）输入 tokens（元）。NULL=未设置，按全价 sell_input 计费'' AFTER cache_creation_input_price_per_m_tok',
  'SELECT ''pricing_rule.sell_cache_creation_input_price_per_m_tok already exists'' AS noop');
PREPARE s2 FROM @ddl2; EXECUTE s2; DEALLOCATE PREPARE s2;

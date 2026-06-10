-- Rollback for 20260609_121500_add_cached_input_price.sql
-- Drops the paired cache-hit input price columns from pricing_rule.
-- IDEMPOTENT: MySQL 8.x has no `DROP COLUMN IF EXISTS` → information_schema + PREPARE.

SET @c1 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'sell_cached_input_price_per_m_tok');
SET @ddl1 = IF(@c1 = 1,
  'ALTER TABLE pricing_rule DROP COLUMN sell_cached_input_price_per_m_tok',
  'SELECT ''sell_cached_input_price_per_m_tok absent'' AS noop');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

SET @c2 = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pricing_rule'
    AND COLUMN_NAME = 'cached_input_price_per_m_tok');
SET @ddl2 = IF(@c2 = 1,
  'ALTER TABLE pricing_rule DROP COLUMN cached_input_price_per_m_tok',
  'SELECT ''cached_input_price_per_m_tok absent'' AS noop');
PREPARE s2 FROM @ddl2; EXECUTE s2; DEALLOCATE PREPARE s2;

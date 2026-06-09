-- Rollback for 20260609_121500_add_cached_input_price.sql
-- Drops the paired cache-hit input price columns from pricing_rule.
ALTER TABLE pricing_rule
  DROP COLUMN IF EXISTS sell_cached_input_price_per_m_tok,
  DROP COLUMN IF EXISTS cached_input_price_per_m_tok;

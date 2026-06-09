-- Add cache-hit input price columns to pricing_rule.
--
-- NULLABLE on purpose: NULL = "cached price not configured" => the cached portion
-- of prompt tokens is billed at the FULL input price, making cost byte-identical
-- to pre-cache behavior. Unit: ¥ per 1,000,000 tokens, matching input_price_per_m_tok.
--
-- PAIRED COLUMNS (do not split): cached_input_price_per_m_tok (cost) and
-- sell_cached_input_price_per_m_tok (sell) are logically coupled — set both or
-- leave both NULL. The Go billing code is independently NULL-safe on each side
-- (cost falls back to input_price_per_m_tok, revenue to sell_input_price_per_m_tok),
-- so even an accidental partial degrades to full price on the unset side.
--
-- Column naming convention: _per_m_tok (canonical, matches existing columns).
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.
ALTER TABLE pricing_rule
  ADD COLUMN IF NOT EXISTS cached_input_price_per_m_tok DECIMAL(10,4) NULL
    COMMENT '成本价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 input 计费'
    AFTER sell_output_price_per_m_tok,
  ADD COLUMN IF NOT EXISTS sell_cached_input_price_per_m_tok DECIMAL(10,4) NULL
    COMMENT '售价：每百万缓存命中输入 tokens（元）。NULL=未设置，按全价 sell_input 计费'
    AFTER cached_input_price_per_m_tok;

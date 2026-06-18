-- Rollback for 20260619_100300_seed_native_cache_pricing.sql
-- Reverts the native-adapter cache pricing seed:
--   1. NULL the cache-HIT (read) + cache-CREATION (write) columns (cost + sell)
--      on the native (provider, model) rows.
--   2. DELETE the base pricing_rule rows the seed inserted for the native providers.
-- Scoped strictly to providers 'claude-native' / 'gemini-native' so no other
-- pricing rows are touched. Idempotent (NULLing/deleting absent rows is a no-op).
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH.

-- 1. NULL the cache columns the seed set (read for both, creation for Claude).
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = NULL,
      sell_cached_input_price_per_m_tok = NULL,
      cache_creation_input_price_per_m_tok = NULL,
      sell_cache_creation_input_price_per_m_tok = NULL
  WHERE provider IN ('claude-native', 'gemini-native');

-- 2. DELETE the base flat rows the seed inserted for the native providers.
DELETE FROM pricing_rule
  WHERE service_type = 'llm_chat'
    AND provider IN ('claude-native', 'gemini-native')
    AND LOWER(model) IN ('claude-opus-4-7', 'claude-sonnet-4-6', 'gemini-3.1-pro');

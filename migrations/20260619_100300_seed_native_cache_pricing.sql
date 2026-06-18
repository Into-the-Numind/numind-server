-- Seed cache pricing for the native-adapter provider rows (claude-native /
-- gemini-native). Feature: native-cache-adapters (T8). See spec §5F / §4 D3 / D5.
--
-- # What this seeds
--   1. Base flat pricing_rule rows for each native (provider, model) pair so the
--      pricing lookup resolves once a route is repointed at a native provider
--      (input/output/sell at the real DMXAPI price). INSERT IGNORE → idempotent
--      (uk_pricing_lookup UNIQUE(service_type, provider, model)).
--   2. cache-HIT (read) DISCOUNT price (cost + sell, paired) for BOTH providers.
--   3. cache-CREATION (write) PREMIUM price (cost + sell, paired) for the CLAUDE
--      rows ONLY. Gemini creation is DELIBERATELY left NULL (D5): Gemini implicit
--      caching has no creation premium, so creation tokens (CacheCreationTokens=0
--      for Gemini anyway) bill at the standard input price.
--
-- # Real DMXAPI prices (¥/M tokens, incl. 6% tax, 2026-06 price sheet)
--   reference: memory reference_llm_prompt_caching_mechanics; aihubmix seed
--   (20260416_100000) for base Claude/Gemini input/output cross-check.
--     model              | input(miss) | output | creation(write) | hit(read)
--     claude-opus-4-7     | 24.82       | 124.10 | 45.625          | 2.482
--     claude-sonnet-4-6   | 15.00       | 75.00  | 18.75           | 1.5
--     gemini-3.1-pro      | 10.00       | 60.00  | (NULL — D5)     | 0.993
--   Creation premium ratios match D3 (opus ~1.84×, sonnet ~1.25× over input).
--   cost == sell (透传 pass-through; 积分制下加价由积分映射体现，不在此层加价),
--   matching the Batch-A seed convention (20260609_121500_seed_cached_input_pricing).
--
-- # INERT until activation (finding #1, two-step activation)
--   These rows describe pricing for providers whose llm_provider rows ship
--   is_active=0 (see 20260619_100400_add_native_provider_rows.sql) and which NO
--   ai_service_route points at yet. So this seed changes NO live billing: it only
--   takes effect after the STEP-4 manual activation repoints a Claude/Gemini route
--   at a native provider. Until then every existing route stays on 'dmxapi' and
--   bills exactly as today (zero regression).
--
-- # Idempotent
--   - Base rows: INSERT IGNORE (UNIQUE uk_pricing_lookup).
--   - Cache columns: each UPDATE guarded by `... IS NULL` so re-running is a no-op
--     and never overwrites an operator-tuned value. LOWER(model) tolerates case
--     drift. Guarded by billing_mode='flat' (tiered paths ignore cache columns).
--
-- ⚠ PROD: verify the real DMXAPI Claude/Gemini prices + the exact provider_model_id
--   the chosen route will use BEFORE applying (price sheet & model names drift):
--     SELECT provider, model, billing_mode, input_price_per_m_tok
--       FROM pricing_rule WHERE provider IN ('claude-native','gemini-native');
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.
-- Rollback: 20260619_100300_seed_native_cache_pricing_rollback.sql

-- ============================================================
-- 0. Base flat pricing_rule rows for the native (provider, model) pairs.
--    INSERT IGNORE → idempotent. cost == sell (透传). These are inert until a
--    route is repointed at the native provider (STEP 4).
-- ============================================================
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
   is_active, created_at, updated_at)
VALUES
  -- Claude Opus 4.7 (flat)
  ('llm_chat', 'claude-native', 'claude-opus-4-7', 'flat', 'call',
   24.82, 124.10, 0, 0,
   24.82, 124.10, 0, 0,
   1, NOW(3), NOW(3)),
  -- Claude Sonnet 4.6 (flat)
  ('llm_chat', 'claude-native', 'claude-sonnet-4-6', 'flat', 'call',
   15.00, 75.00, 0, 0,
   15.00, 75.00, 0, 0,
   1, NOW(3), NOW(3)),
  -- Gemini 3.1 Pro (flat)
  ('llm_chat', 'gemini-native', 'gemini-3.1-pro', 'flat', 'call',
   10.00, 60.00, 0, 0,
   10.00, 60.00, 0, 0,
   1, NOW(3), NOW(3));

-- ============================================================
-- 1. cache-HIT (read) DISCOUNT price — BOTH native providers (cost + sell paired).
--    Idempotent: WHERE cached_input_price_per_m_tok IS NULL.
-- ============================================================
-- claude-opus-4-7: read ¥2.482
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 2.4820, sell_cached_input_price_per_m_tok = 2.4820
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'claude-native'
    AND LOWER(model) = 'claude-opus-4-7'
    AND cached_input_price_per_m_tok IS NULL;

-- claude-sonnet-4-6: read ¥1.5
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 1.5000, sell_cached_input_price_per_m_tok = 1.5000
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'claude-native'
    AND LOWER(model) = 'claude-sonnet-4-6'
    AND cached_input_price_per_m_tok IS NULL;

-- gemini-3.1-pro: read ¥0.993 (cachedContentTokenCount discount)
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 0.9930, sell_cached_input_price_per_m_tok = 0.9930
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'gemini-native'
    AND LOWER(model) = 'gemini-3.1-pro'
    AND cached_input_price_per_m_tok IS NULL;

-- ============================================================
-- 2. cache-CREATION (write) PREMIUM price — CLAUDE ONLY (cost + sell paired).
--    Gemini is intentionally absent (D5: no creation premium → leave NULL → full
--    input price). Idempotent: WHERE cache_creation_input_price_per_m_tok IS NULL.
-- ============================================================
-- claude-opus-4-7: creation ¥45.625 (~1.84× input premium)
UPDATE pricing_rule
  SET cache_creation_input_price_per_m_tok = 45.6250, sell_cache_creation_input_price_per_m_tok = 45.6250
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'claude-native'
    AND LOWER(model) = 'claude-opus-4-7'
    AND cache_creation_input_price_per_m_tok IS NULL;

-- claude-sonnet-4-6: creation ¥18.75 (~1.25× input premium)
UPDATE pricing_rule
  SET cache_creation_input_price_per_m_tok = 18.7500, sell_cache_creation_input_price_per_m_tok = 18.7500
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'claude-native'
    AND LOWER(model) = 'claude-sonnet-4-6'
    AND cache_creation_input_price_per_m_tok IS NULL;

-- POST-APPLY VERIFICATION (run manually): native rows present, read set on both,
-- creation set on Claude only, Gemini creation still NULL (D5).
--   SELECT provider, model, input_price_per_m_tok,
--          cached_input_price_per_m_tok, cache_creation_input_price_per_m_tok
--     FROM pricing_rule WHERE provider IN ('claude-native','gemini-native')
--     ORDER BY provider, model;

-- Seed cache-hit input pricing for Batch A models (DeepSeek + OpenAI GPT, FLAT rows).
--
-- ============================================================================
-- TARGET ROW SET — derived from the migration HISTORY (the single source of
-- truth for which (provider, model) flat pricing_rule rows actually exist),
-- NOT from any single migration's prose. Every tuple below was located in a
-- committed seed/INSERT migration in this repo:
--
--   provider      | model                  | input  | source migration
--   --------------+------------------------+--------+--------------------------------------------
--   volc-ark      | deepseek-v3-2-251201   | 1.2184 | seed_pricing_rules.sql:18   (SOP 主力, optional)
--   dmxapi        | deepseek-v3-2-251201   | 1.0000 | seed_pricing_rules.sql:24   (SalesRAG)
--   dmxapi        | deepseek-v4-pro        |14.0000 | 20260424_204500_seed_deepseek_v4_pro.sql:114
--   aihubmix      | deepseek-v4-pro        |14.0000 | 20260424_204500_seed_deepseek_v4_pro.sql:120
--   dmxapi        | DeepSeek-V3.2          | 2.1600 | 20260419_170000_seed_pricing_global_fallback.sql:38
--   dmxapi        | DeepSeek-V3.2-Thinking | 2.1600 | 20260419_170000_seed_pricing_global_fallback.sql:40
--   aihubmix      | deepseek-v3.2          | 2.1600 | 20260416_100000_seed_aihubmix_provider.sql:139
--   aihubmix      | deepseek-v3.2-thinking | 2.1600 | 20260420_030000_seed_drift_pricing_rules.sql:43
--   dmxapi-ssvip  | deepseek-v3.2          | 2.1600 | 20260418_170000_pricing_rule_vocabulary_fix.sql:91 (copied aihubmix)
--   dmxapi        | gpt-5.4                |10.0000 | 20260419_170000_seed_pricing_global_fallback.sql:46
--
-- OUT OF SCOPE (NOT seeded) — tiered_token GPT rows: aihubmix/gpt-5.4,
-- aihubmix/gpt-5.4-thinking, dmxapi-ssvip/gpt-5.4. The tier path
-- (pricing_rule_tier) never reads the cached_input_price_per_m_tok column, so a
-- cached price there would be ignored → those routes bill at full price → zero
-- regression by design. Excluded here via billing_mode='flat' + tuple matching.
-- gpt-5.5 has NO pricing_rule row (test fixture only) → deferred.
--
-- ============================================================================
-- ROBUSTNESS: cached price is derived as a RATIO of the row's OWN stored price
-- (ROUND(input_price_per_m_tok * ratio, 4)), NOT a hardcoded constant — so it is
-- correct whatever the live base price is and can never silently mis-price if an
-- operator repriced the base. Rounded to the column scale DECIMAL(10,4).
--
-- PAIRED COLUMNS (P1 #5): cost (cached_input_price_per_m_tok) and sell
-- (sell_cached_input_price_per_m_tok) are ALWAYS set together in one statement,
-- so a partial (cost-only / sell-only) state can never arise here.
--
-- IDEMPOTENT: WHERE ... AND cached_input_price_per_m_tok IS NULL — never
-- overwrites an existing value (operator edits and re-runs are both safe).
--
-- Ratios (sources cited):
--   DeepSeek cache-hit input ≈ 0.1× normal — DeepSeek API context-caching pricing
--     (cache-hit input ~1/10 of cache-miss). https://api-docs.deepseek.com (context caching)
--   OpenAI GPT cached input ≈ 0.5× normal — OpenAI prompt-caching docs (some tiers
--     0.25×; seed conservatively at 0.5×). https://platform.openai.com/docs/guides/prompt-caching
--
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before
-- deploy, then run the post-apply COUNT check at the bottom of this file.

-- ----------------------------------------------------------------------------
-- DeepSeek family — 0.1× of the row's own price, paired cost+sell, flat only.
-- ----------------------------------------------------------------------------
UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type = 'llm_chat'
    AND billing_mode = 'flat'
    AND cached_input_price_per_m_tok IS NULL
    AND (provider, model) IN (
      ('volc-ark',     'deepseek-v3-2-251201'),   -- 1.2184 -> 0.1218
      ('dmxapi',       'deepseek-v3-2-251201'),   -- 1.0000 -> 0.1000
      ('dmxapi',       'deepseek-v4-pro'),        -- 14.0000 -> 1.4000
      ('aihubmix',     'deepseek-v4-pro'),        -- 14.0000 -> 1.4000
      ('dmxapi',       'DeepSeek-V3.2'),          -- 2.1600 -> 0.2160
      ('dmxapi',       'DeepSeek-V3.2-Thinking'), -- 2.1600 -> 0.2160
      ('aihubmix',     'deepseek-v3.2'),          -- 2.1600 -> 0.2160
      ('aihubmix',     'deepseek-v3.2-thinking'), -- 2.1600 -> 0.2160
      ('dmxapi-ssvip', 'deepseek-v3.2')           -- 2.1600 -> 0.2160
    );

-- ----------------------------------------------------------------------------
-- OpenAI GPT — 0.5× of the row's own price, paired cost+sell, flat only.
-- (aihubmix/gpt-5.4[-thinking] and dmxapi-ssvip/gpt-5.4 are tiered_token →
--  excluded by billing_mode='flat'; only the dmxapi flat gpt-5.4 row matches.)
-- ----------------------------------------------------------------------------
UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.5, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.5, 4)
  WHERE service_type = 'llm_chat'
    AND billing_mode = 'flat'
    AND cached_input_price_per_m_tok IS NULL
    AND (provider, model) IN (
      ('dmxapi', 'gpt-5.4')                       -- 10.0000 -> 5.0000
    );

-- ----------------------------------------------------------------------------
-- POST-APPLY VERIFICATION (run manually after applying — proves WHERE matched):
--   SELECT provider, model, input_price_per_m_tok,
--          cached_input_price_per_m_tok, sell_cached_input_price_per_m_tok
--   FROM pricing_rule WHERE cached_input_price_per_m_tok IS NOT NULL
--   ORDER BY provider, model;
-- Expected: the targeted flat rows that exist on the environment (up to 10:
--   9 deepseek + 1 gpt-5.4). A COUNT of 0 means the WHERE missed every row →
--   STOP and re-run the live-DB discovery SELECT:
--     SELECT provider, model, billing_mode, input_price_per_m_tok
--     FROM pricing_rule
--     WHERE service_type='llm_chat' AND billing_mode='flat'
--       AND (model LIKE '%deepseek%' OR model LIKE '%DeepSeek%' OR model LIKE '%gpt-5%');
-- ----------------------------------------------------------------------------

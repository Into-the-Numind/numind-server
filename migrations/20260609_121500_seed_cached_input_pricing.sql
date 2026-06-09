-- Seed cache-hit input pricing for Batch A models (DeepSeek + OpenAI GPT, flat rows).
--
-- ============================================================================
-- LIVE-DB VERIFIED (mandatory pre-step, spec §4.6 P0 #1): the historical
-- migration files were STALE. The discovery SELECT
--   SELECT provider, model, billing_mode, input_price_per_m_tok, sell_input_price_per_m_tok
--   FROM pricing_rule
--   WHERE service_type='llm_chat' AND billing_mode='flat'
--     AND (model LIKE '%deepseek%' OR model LIKE '%DeepSeek%' OR model LIKE '%gpt-5%');
-- run against BOTH dev (numind-dev) and prod (numind-prod) on 2026-06-09 returned
-- the IDENTICAL set of 6 flat rows below (operators had renamed/repriced rows
-- since the migration history was written; the spec table's
-- dmxapi/deepseek-v3-2-251201, volc-ark/deepseek-v3-2-251201, DeepSeek-V3.2*,
-- dmxapi/gpt-5.4, and the 14.00 deepseek-v4-pro price DO NOT EXIST live):
--
--   provider  | model                  | billing_mode | input  | sell_input
--   ----------+------------------------+--------------+--------+-----------
--   aihubmix  | deepseek-v3.2          | flat         | 2.1600 | 2.1600
--   aihubmix  | deepseek-v3.2-thinking | flat         | 2.1600 | 2.1600
--   aihubmix  | deepseek-v4-pro        | flat         | 4.0000 | 4.0000
--   dmxapi    | deepseek-v3.2-thinking | flat         | 1.5800 | 2.1600
--   dmxapi    | deepseek-v4-pro        | flat         | 4.0000 | 4.0000
--   dmxapi    | gpt-5.5                | flat         |24.8200 |24.8200
--
-- aihubmix/gpt-5.4 and dmxapi/gpt-5.4 are tiered_token (input price 0) — excluded
-- by billing_mode='flat' (the tiered path is not cache-aware in Batch A → full price).
-- ============================================================================
--
-- ROBUSTNESS: cached price is derived as a RATIO of the row's OWN stored price
-- (not a hardcoded constant), so it is correct whatever the stored base is and
-- can never silently mis-price if an operator changed the base. Rounded to the
-- column scale DECIMAL(10,4).
--
-- PAIRED COLUMNS (P1 #5): cost (cached_input_price_per_m_tok) and sell
-- (sell_cached_input_price_per_m_tok) are ALWAYS set together in one statement.
--
-- IDEMPOTENT: WHERE ... AND cached_input_price_per_m_tok IS NULL — never
-- overwrites an existing value (operator edits or a re-run are safe).
--
-- Single-row UPDATEs (one per (provider,model)) for maximum portability and to
-- guarantee each WHERE matches the exact live string.
--
-- Ratios (sources cited):
--   DeepSeek cache-hit input ≈ 0.1× normal — DeepSeek API context-caching pricing
--     (cache-hit input ~1/10 of cache-miss). https://api-docs.deepseek.com (context caching)
--   OpenAI GPT cached input ≈ 0.5× normal — OpenAI prompt-caching docs (some tiers
--     0.25×; seed conservatively at 0.5×). https://platform.openai.com/docs/guides/prompt-caching
--
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy,
-- then run the post-apply COUNT check at the bottom of this file.

-- ----------------------------------------------------------------------------
-- DeepSeek family (0.1× of the row's own price, paired cost+sell)
-- ----------------------------------------------------------------------------

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='aihubmix' AND model='deepseek-v3.2'          -- 2.16 -> 0.2160
    AND cached_input_price_per_m_tok IS NULL;

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='aihubmix' AND model='deepseek-v3.2-thinking' -- 2.16 -> 0.2160
    AND cached_input_price_per_m_tok IS NULL;

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='aihubmix' AND model='deepseek-v4-pro'        -- 4.00 -> 0.4000
    AND cached_input_price_per_m_tok IS NULL;

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='dmxapi' AND model='deepseek-v3.2-thinking'   -- cost 1.58->0.1580, sell 2.16->0.2160
    AND cached_input_price_per_m_tok IS NULL;

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.1, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.1, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='dmxapi' AND model='deepseek-v4-pro'          -- 4.00 -> 0.4000
    AND cached_input_price_per_m_tok IS NULL;

-- ----------------------------------------------------------------------------
-- OpenAI GPT (0.5× of the row's own price, paired cost+sell)
-- ----------------------------------------------------------------------------

UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = ROUND(input_price_per_m_tok      * 0.5, 4),
      sell_cached_input_price_per_m_tok = ROUND(sell_input_price_per_m_tok * 0.5, 4)
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND provider='dmxapi' AND model='gpt-5.5'                  -- 24.82 -> 12.4100
    AND cached_input_price_per_m_tok IS NULL;

-- ----------------------------------------------------------------------------
-- POST-APPLY VERIFICATION (run manually after applying — proves WHERE matched):
--   SELECT provider, model, input_price_per_m_tok,
--          cached_input_price_per_m_tok, sell_cached_input_price_per_m_tok
--   FROM pricing_rule WHERE cached_input_price_per_m_tok IS NOT NULL
--   ORDER BY provider, model;
-- Expected: exactly 6 rows (5 deepseek + 1 gpt). A count of 0 means the WHERE
-- missed → STOP, re-run the live-DB discovery SELECT above.
-- ----------------------------------------------------------------------------

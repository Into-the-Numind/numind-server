-- Rollback for 20260609_121500_seed_cached_input_pricing.sql
-- Resets the paired cached price columns to NULL for the exact (provider, model)
-- flat rows the seed targeted (live-verified set derived from the migration
-- history). aihubmix/gpt-5.4(+thinking) and dmxapi-ssvip/gpt-5.4 are NOT listed
-- (tiered_token — never seeded). gpt-5.5 has no pricing_rule row (deferred).
UPDATE pricing_rule
  SET cached_input_price_per_m_tok      = NULL,
      sell_cached_input_price_per_m_tok = NULL
  WHERE service_type = 'llm_chat'
    AND billing_mode = 'flat'
    AND (provider, model) IN (
      ('volc-ark',     'deepseek-v3-2-251201'),
      ('dmxapi',       'deepseek-v3-2-251201'),
      ('dmxapi',       'deepseek-v4-pro'),
      ('aihubmix',     'deepseek-v4-pro'),
      ('dmxapi',       'DeepSeek-V3.2'),
      ('dmxapi',       'DeepSeek-V3.2-Thinking'),
      ('aihubmix',     'deepseek-v3.2'),
      ('aihubmix',     'deepseek-v3.2-thinking'),
      ('dmxapi-ssvip', 'deepseek-v3.2'),
      ('dmxapi',       'gpt-5.4')
    );

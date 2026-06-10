-- Rollback for 20260609_121500_seed_cached_input_pricing.sql
-- NULLs the paired cached price columns for the exact dmxapi flat Batch-A rows
-- the seed targeted (deepseek-v4-pro / deepseek-v3.2[+thinking] / gpt-5.5 / gpt-5.4).
-- aihubmix never seeded → not listed. LOWER(model) mirrors the seed for case-safety.
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = NULL, sell_cached_input_price_per_m_tok = NULL
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'dmxapi'
    AND LOWER(model) IN (
      'deepseek-v4-pro',
      'deepseek-v3.2',
      'deepseek-v3.2-thinking',
      'gpt-5.5',
      'gpt-5.4'
    );

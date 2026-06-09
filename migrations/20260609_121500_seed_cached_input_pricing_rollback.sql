-- Rollback for 20260609_121500_seed_cached_input_pricing.sql
-- Resets the paired cached price columns to NULL for the exact (provider,model)
-- rows the seed targeted (live-verified set as of 2026-06-09).
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = NULL,
      sell_cached_input_price_per_m_tok = NULL
  WHERE service_type='llm_chat' AND billing_mode='flat'
    AND (
      (provider='aihubmix' AND model='deepseek-v3.2')          OR
      (provider='aihubmix' AND model='deepseek-v3.2-thinking') OR
      (provider='aihubmix' AND model='deepseek-v4-pro')        OR
      (provider='dmxapi'   AND model='deepseek-v3.2-thinking') OR
      (provider='dmxapi'   AND model='deepseek-v4-pro')        OR
      (provider='dmxapi'   AND model='gpt-5.5')
    );

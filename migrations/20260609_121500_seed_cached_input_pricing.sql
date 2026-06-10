-- Seed cache-HIT input pricing for Batch A models — ABSOLUTE real DMXAPI prices.
--
-- 决策 (2026-06-10, user): 透传 DMXAPI 成本价 (用户命中价 = DMXAPI 实际命中价, 非倍数);
--   base 输入价不动; aihubmix 暂不设 (无 aihubmix 价表 → NULL → 全价, 零回归);
--   gpt-5.4 本环境为 tiered_token (tiered 计费路径不读 cached 列) → billing_mode='flat'
--   守卫使其不匹配 → 全价零回归; 若某环境 gpt-5.4 为 flat 则按 ¥1.25 生效.
--
-- 价格来源: DMXAPI 价目表 (含税6%) 2026-06. 见 reference_llm_prompt_caching_mechanics 记忆.
--   deepseek-v4-pro 命中 ¥0.025 | deepseek-v3.2(+thinking) ¥0.2 | gpt-5.5 ¥2.482 | gpt-5.4 ¥1.25
--
-- cached_input_price_per_m_tok = 扣用户积分依据 (CalculateCostWithCache);
-- sell_cached_input_price_per_m_tok = 营收记账. 透传 + cost==sell 惯例 → 同值.
-- 列由 GORM AutoMigrate 建; 本文件是数据 UPDATE.
-- 幂等: WHERE cached_input_price_per_m_tok IS NULL — 不覆盖既有值.
-- LOWER(model) 兼容大小写漂移 (dev 小写 deepseek-v3.2 vs 历史大写 DeepSeek-V3.2).
--
-- ⚠ PROD 上线前先核对真实行 (provider/model 名称/大小写/flat-or-tiered 各环境可能不同):
--     SELECT provider, model, billing_mode, input_price_per_m_tok FROM pricing_rule
--     WHERE service_type='llm_chat'
--       AND (LOWER(model) LIKE '%deepseek%' OR LOWER(model) LIKE '%gpt-5%');
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.

-- deepseek-v4-pro: 命中 ¥0.025
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 0.0250, sell_cached_input_price_per_m_tok = 0.0250
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'dmxapi'
    AND LOWER(model) = 'deepseek-v4-pro'
    AND cached_input_price_per_m_tok IS NULL;

-- deepseek-v3.2 (含 thinking 变体): 命中 ¥0.2
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 0.2000, sell_cached_input_price_per_m_tok = 0.2000
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'dmxapi'
    AND LOWER(model) IN ('deepseek-v3.2', 'deepseek-v3.2-thinking')
    AND cached_input_price_per_m_tok IS NULL;

-- gpt-5.5: 命中 ¥2.482
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 2.4820, sell_cached_input_price_per_m_tok = 2.4820
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'dmxapi'
    AND LOWER(model) = 'gpt-5.5'
    AND cached_input_price_per_m_tok IS NULL;

-- gpt-5.4: 命中 ¥1.25 (仅当该环境为 flat 时生效; dev 为 tiered → 不匹配, 零回归)
UPDATE pricing_rule
  SET cached_input_price_per_m_tok = 1.2500, sell_cached_input_price_per_m_tok = 1.2500
  WHERE service_type = 'llm_chat' AND billing_mode = 'flat' AND provider = 'dmxapi'
    AND LOWER(model) = 'gpt-5.4'
    AND cached_input_price_per_m_tok IS NULL;

-- POST-APPLY VERIFICATION (run manually): 期望看到已存在的 dmxapi flat 行被赋值.
--   SELECT provider, model, input_price_per_m_tok, cached_input_price_per_m_tok
--   FROM pricing_rule WHERE cached_input_price_per_m_tok IS NOT NULL ORDER BY provider, model;

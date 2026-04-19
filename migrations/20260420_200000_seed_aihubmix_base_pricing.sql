-- 20260420_200000_seed_aihubmix_base_pricing.sql
--
-- 修复 aihubmix 4 个 base（非 thinking）模型缺 pricing_rule 的 seed drift。
--
-- 背景：20260420_030000_seed_drift_pricing_rules.sql 只补了 -thinking 变体的
-- 价格行，base 版本（claude-sonnet-4-6 / deepseek-v3.2 / gemini-3.1-pro-preview /
-- gpt-5.4）漏 seed。billing middleware 查 pricing_rule 未命中 → fallback 到全局
-- 兜底 ('llm_chat','','') ¥3/¥10 MTok。aihubmix 官方价 Claude Sonnet 4.6 实际
-- ¥21.60/¥108，被少收 86% → Reconcile 会大幅补扣，用户体验差。
--
-- 修复策略：base 与 thinking 复用同价（aihubmix 两者定价一致），直接镜像
-- 20260420_030000 的 4 条 thinking 行到对应 base model_key。
--
-- ## 幂等性
-- pricing_rule.uk_pricing_lookup UNIQUE (service_type, provider, model) → INSERT IGNORE 静默跳过。
-- pricing_rule_tier 无 UNIQUE → 用 NOT EXISTS 子查询保幂等。
--
-- ## ROLLBACK
-- 见 20260420_200000_seed_aihubmix_base_pricing_rollback.sql

-- ============================================================
-- 1. flat 模式（2 条）
-- ============================================================

INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  -- (1) aihubmix / claude-sonnet-4-6 — flat，与 thinking 同价 ¥21.60/¥108.00
  ('llm_chat', 'aihubmix', 'claude-sonnet-4-6', 'flat', 'call',
   21.60, 108.00, 0, 0,
   21.60, 108.00, 0, 0,
   1, NOW(3), NOW(3)),

  -- (2) aihubmix / deepseek-v3.2 — flat，与 thinking 同价 ¥2.16/¥3.24
  ('llm_chat', 'aihubmix', 'deepseek-v3.2', 'flat', 'call',
   2.16, 3.24, 0, 0,
   2.16, 3.24, 0, 0,
   1, NOW(3), NOW(3));

-- ============================================================
-- 2. tiered_token 父规则（2 条）
-- ============================================================

INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  -- (3) aihubmix / gemini-3.1-pro-preview — tiered，分档同 thinking
  ('llm_chat', 'aihubmix', 'gemini-3.1-pro-preview', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(3), NOW(3)),

  -- (4) aihubmix / gpt-5.4 — tiered，分档同 thinking
  ('llm_chat', 'aihubmix', 'gpt-5.4', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(3), NOW(3));

-- ============================================================
-- 3. tiered_token 子表（8 条 = 2 model × 2 token_type × 2 档）
-- ============================================================
-- 分档结构镜像 20260420_030000 中 -thinking 变体，价格完全相同。

-- Gemini 3.1 Pro Preview — input 档一：0 ~ 200000 → ¥14.40/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 0, 200000, 14.400000, 14.400000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 0
  );

-- Gemini input 档二：200001 ~ 不限 → ¥28.80/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 200001, NULL, 28.800000, 28.800000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 200001
  );

-- Gemini output 档一：0 ~ 200000 → ¥86.40/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 0, 200000, 86.400000, 86.400000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 0
  );

-- Gemini output 档二：200001 ~ 不限 → ¥129.60/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 200001, NULL, 129.600000, 129.600000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 200001
  );

-- GPT 5.4 — input 档一：0 ~ 272000 → ¥18.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 0, 272000, 18.000000, 18.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 0
  );

-- GPT input 档二：272001 ~ 不限 → ¥36.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 272001, NULL, 36.000000, 36.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 272001
  );

-- GPT output 档一：0 ~ 272000 → ¥108.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 0, 272000, 108.000000, 108.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 0
  );

-- GPT output 档二：272001 ~ 不限 → ¥162.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 272001, NULL, 162.000000, 162.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 272001
  );

-- ============================================================
-- 验证查询（手工执行，应返回 4 行父 + 8 行 tier 子）
-- ============================================================
-- SELECT service_type, provider, model, billing_mode FROM pricing_rule
-- WHERE provider='aihubmix' AND model IN (
--   'claude-sonnet-4-6', 'deepseek-v3.2',
--   'gemini-3.1-pro-preview', 'gpt-5.4')
-- ORDER BY model;
--
-- SELECT pr.model, prt.token_type, prt.min_tokens, prt.cost_per_mtok
-- FROM pricing_rule pr JOIN pricing_rule_tier prt ON prt.rule_id = pr.id
-- WHERE pr.provider='aihubmix' AND pr.model IN ('gemini-3.1-pro-preview','gpt-5.4')
-- ORDER BY pr.model, prt.token_type, prt.min_tokens;

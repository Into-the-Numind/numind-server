-- 20260420_030000_seed_drift_pricing_rules.sql
--
-- 修复 pricing_rule seed drift。
--
-- 背景：dev DB 通过管理端人工添加了 5 条价格规则但未回写 seed 文件，
-- 导致 QA/prod 新环境部署时 salesrag.chat / sop / chatbot 调用以下模型
-- 全部报 "pricing lookup: record not found" 500（与 hotfix
-- salesrag-pricing-resolve-route 同根因，不同表现）。
--
-- 本 migration 补 5 条人工漂移行 + 1 条 ('llm_chat','','') 全局兜底
-- 作 defensive layer。
--
-- ## 幂等性
-- - pricing_rule.uk_pricing_lookup UNIQUE (service_type, provider, model)
--   保证 INSERT IGNORE 在已存在时静默跳过。
-- - pricing_rule_tier 无 UNIQUE 约束，INSERT IGNORE 在子表无效会插重复行；
--   故 tier 子行用 INSERT...SELECT...WHERE NOT EXISTS 子查询保幂等。
--
-- ## ROLLBACK
-- 见 20260420_030000_seed_drift_pricing_rules_rollback.sql
-- （DELETE 6 父规则，FK CASCADE 自动清理 8 条 tier 子行）

-- ============================================================
-- 1. flat 模式价格规则（4 条）
-- ============================================================

INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  -- (1) aihubmix / claude-sonnet-4-6-thinking — flat，与 base 同价 ¥21.60/¥108.00
  -- 注意：seed_aihubmix_provider.sql 用的是 -think 短后缀（provider_model_id 维度），
  -- 但 billing middleware 用 model_key 长后缀 -thinking 查 pricing → 必须 seed 此条。
  ('llm_chat', 'aihubmix', 'claude-sonnet-4-6-thinking', 'flat', 'call',
   21.60, 108.00, 0, 0,
   21.60, 108.00, 0, 0,
   1, NOW(), NOW()),

  -- (2) aihubmix / deepseek-v3.2-thinking — flat，与 base 同价 ¥2.16/¥3.24
  -- salesrag.chat 当前路由到此模型（task_profile.default_service_id=16）
  ('llm_chat', 'aihubmix', 'deepseek-v3.2-thinking', 'flat', 'call',
   2.16, 3.24, 0, 0,
   2.16, 3.24, 0, 0,
   1, NOW(), NOW()),

  -- (3) ali-dashscope / qwen3-vl-flash — flat vision，¥0.15/¥1.50
  -- salesrag.profile / salesrag.chatstyle 路由到此模型
  ('llm_vision', 'ali-dashscope', 'qwen3-vl-flash', 'flat', 'call',
   0.15, 1.50, 0, 0,
   0.15, 1.50, 0, 0,
   1, NOW(), NOW()),

  -- (4) 全局 LLM chat 兜底 — defensive layer
  -- 主路径走 ResolveTask + 精确价格规则；本条仅在 ResolveTask 失败 / 未来代码
  -- 漏传 provider/model 时兜住，按保守 ¥3/¥10 估算（Reconcile 按真实成本多退少补）
  ('llm_chat', '', '', 'flat', 'call',
   3.0, 10.0, 0, 0,
   3.0, 10.0, 0, 0,
   1, NOW(), NOW());

-- ============================================================
-- 2. tiered_token 模式父规则（2 条）
-- ============================================================

INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  -- (5) aihubmix / gemini-3.1-pro-preview-thinking — tiered，分档同 base
  ('llm_chat', 'aihubmix', 'gemini-3.1-pro-preview-thinking', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(), NOW()),

  -- (6) aihubmix / gpt-5.4-thinking — tiered，分档同 base
  ('llm_chat', 'aihubmix', 'gpt-5.4-thinking', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(), NOW());

-- ============================================================
-- 3. tiered_token 子表（8 条 = 2 model × 2 token_type × 2 档）
-- ============================================================
-- pricing_rule_tier 无 UNIQUE 约束，用 NOT EXISTS 子查询保幂等。
-- rule_id 通过 SELECT pricing_rule.id 动态绑定，避免硬编码 ID。

-- Gemini 3.1 Pro Preview Thinking — input 档一：0 ~ 200000 → ¥14.40/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 0, 200000, 14.400000, 14.400000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 0
  );

-- Gemini input 档二：200001 ~ 不限 → ¥28.80/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 200001, NULL, 28.800000, 28.800000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 200001
  );

-- Gemini output 档一：0 ~ 200000 → ¥86.40/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 0, 200000, 86.400000, 86.400000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 0
  );

-- Gemini output 档二：200001 ~ 不限 → ¥129.60/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 200001, NULL, 129.600000, 129.600000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gemini-3.1-pro-preview-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 200001
  );

-- GPT 5.4 Thinking — input 档一：0 ~ 272000 → ¥18.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 0, 272000, 18.000000, 18.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 0
  );

-- GPT input 档二：272001 ~ 不限 → ¥36.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'input', 272001, NULL, 36.000000, 36.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'input' AND prt.min_tokens = 272001
  );

-- GPT output 档一：0 ~ 272000 → ¥108.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 0, 272000, 108.000000, 108.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 0
  );

-- GPT output 档二：272001 ~ 不限 → ¥162.00/M
INSERT INTO pricing_rule_tier (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT pr.id, 'output', 272001, NULL, 162.000000, 162.000000
FROM pricing_rule pr
WHERE pr.provider = 'aihubmix' AND pr.model = 'gpt-5.4-thinking'
  AND NOT EXISTS (
    SELECT 1 FROM pricing_rule_tier prt
    WHERE prt.rule_id = pr.id AND prt.token_type = 'output' AND prt.min_tokens = 272001
  );

-- ============================================================
-- 验证查询（手工执行，应返回 6 行父 + 8 行 tier 子）
-- ============================================================
-- SELECT service_type, provider, model, billing_mode FROM pricing_rule
-- WHERE (provider='aihubmix' AND model IN (
--     'claude-sonnet-4-6-thinking', 'deepseek-v3.2-thinking',
--     'gemini-3.1-pro-preview-thinking', 'gpt-5.4-thinking'))
--   OR (provider='ali-dashscope' AND model='qwen3-vl-flash')
--   OR (provider='' AND model='' AND service_type='llm_chat')
-- ORDER BY service_type, provider, model;
--
-- SELECT pr.model, prt.token_type, prt.min_tokens, prt.cost_per_mtok
-- FROM pricing_rule pr JOIN pricing_rule_tier prt ON prt.rule_id = pr.id
-- WHERE pr.provider='aihubmix' AND pr.model LIKE '%-thinking'
-- ORDER BY pr.model, prt.token_type, prt.min_tokens;

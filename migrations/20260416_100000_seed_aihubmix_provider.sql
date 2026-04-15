-- Migration: 20260416_100000_seed_aihubmix_provider.sql
-- Feature:   aihubmix-provider 接入（主路由 + 定价规则）
-- Date:      2026-04-16
-- Refs:      docs/superpowers/plans/aihubmix-provider-plan.md (Task 2)
--            docs/superpowers/specs/2026-04-16-aihubmix-provider-design.md §4
--
-- ROLLBACK（如需回滚，按以下顺序执行）:
--   DELETE FROM pricing_rule_tier
--     WHERE rule_id IN (SELECT id FROM pricing_rule WHERE provider='aihubmix');
--   DELETE FROM pricing_rule WHERE provider='aihubmix';
--   DELETE FROM llm_model_provider
--     WHERE provider_id=(SELECT id FROM llm_provider WHERE name='aihubmix');
--   DELETE FROM llm_provider WHERE name='aihubmix';

-- ============================================================
-- 1. llm_provider — 新增 AiHubMix 供应商（1 行）
-- ============================================================
-- name 列有 UNIQUE 约束，INSERT IGNORE 保证幂等
--
-- 安全豁免：api_key 以字面值直写（违反 CLAUDE.md §3 "禁止硬编码 API 密钥"）。
-- 本次用户已明示豁免，记录于：
--   - docs/superpowers/specs/2026-04-16-aihubmix-provider-design.md §D6
--   - build-manifest.yaml 条目 aihubmix-provider.decisions（2026-04-16 S2）
-- 未来 ai-service-manager 的 SyncProviderCredentials 实装后可清除此字面值，
-- 改由启动时从 config 读取。
INSERT IGNORE INTO llm_provider (name, display_name, base_url, api_key, is_active)
VALUES (
  'aihubmix',
  'AiHubMix',
  'https://aihubmix.com/v1',
  'sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68',
  1
);

-- ============================================================
-- 2. llm_model_provider — 8 条路由（4 base + 4 thinking 变体）
-- ============================================================
-- priority=5：低于 DMXAPI(=10)，Router 升序遍历时 AiHubMix 优先选中（主路由）
-- uk_model_provider UNIQUE(model_id, provider_id)，INSERT IGNORE 保证幂等
--
-- 映射说明（model_key → provider_model_id）：
--   claude-sonnet-4-6                → claude-sonnet-4-6       （同）
--   claude-sonnet-4-6-thinking       → claude-sonnet-4-6-think （注意后缀 -think，无 ing）
--   gemini-3.1-pro-preview           → gemini-3.1-pro-preview  （同）
--   gemini-3.1-pro-preview-thinking  → gemini-3.1-pro-preview  （thinking 由 reasoning_effort 激活）
--   deepseek-v3.2                    → deepseek-v3.2           （同）
--   deepseek-v3.2-thinking           → deepseek-v3.2           （thinking 由 reasoning_effort 激活）
--   gpt-5.4                          → gpt-5.4                 （同）
--   gpt-5.4-thinking                 → gpt-5.4                 （thinking 由 reasoning_effort 激活）
INSERT IGNORE INTO llm_model_provider
  (model_id, provider_id, provider_model_id, priority, input_price_per_mtok, output_price_per_mtok, is_active)
VALUES
  -- Claude Sonnet 4.6（基础）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'claude-sonnet-4-6'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'claude-sonnet-4-6',
    5, 0, 0, 1
  ),
  -- Claude Sonnet 4.6 Thinking（-think 后缀触发 temperature=1，不传 reasoning_effort）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'claude-sonnet-4-6-thinking'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'claude-sonnet-4-6-think',
    5, 0, 0, 1
  ),
  -- Gemini 3.1 Pro Preview（基础）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'gemini-3.1-pro-preview'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'gemini-3.1-pro-preview',
    5, 0, 0, 1
  ),
  -- Gemini 3.1 Pro Preview Thinking（与 base 同 provider_model_id，reasoning_effort 区分）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'gemini-3.1-pro-preview-thinking'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'gemini-3.1-pro-preview',
    5, 0, 0, 1
  ),
  -- DeepSeek V3.2（基础）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'deepseek-v3.2'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'deepseek-v3.2',
    5, 0, 0, 1
  ),
  -- DeepSeek V3.2 Thinking（与 base 同 provider_model_id，reasoning_effort 区分）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'deepseek-v3.2-thinking'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'deepseek-v3.2',
    5, 0, 0, 1
  ),
  -- GPT 5.4（基础）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'gpt-5.4'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'gpt-5.4',
    5, 0, 0, 1
  ),
  -- GPT 5.4 Thinking（与 base 同 provider_model_id，reasoning_effort 区分）
  (
    (SELECT id FROM llm_model    WHERE model_key = 'gpt-5.4-thinking'),
    (SELECT id FROM llm_provider WHERE name      = 'aihubmix'),
    'gpt-5.4',
    5, 0, 0, 1
  );

-- ============================================================
-- 3. pricing_rule — 5 条规则（3 flat + 2 tiered_token）
-- ============================================================
-- 价格单位：元/百万 tokens（per_mtok）
-- sell 价格与 cost 价格相同（积分制下售价通过积分映射体现，不在此层加价）
--
-- Claude: billing_mode=flat，直接在 pricing_rule 记录价格
-- DeepSeek: billing_mode=flat，直接在 pricing_rule 记录价格
-- Gemini/GPT: billing_mode=tiered_token，头表价格填 0，实际价格走 pricing_rule_tier 子表
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
   is_active, created_at, updated_at)
VALUES
  -- Claude Sonnet 4.6（基础，flat）— input ¥21.60/M, output ¥108.00/M
  ('llm_chat', 'aihubmix', 'claude-sonnet-4-6', 'flat', 'call',
   21.60, 108.00, 0, 0,
   21.60, 108.00, 0, 0,
   1, NOW(), NOW()),

  -- Claude Sonnet 4.6 Thinking（-think 变体，flat，与 base 同价）
  -- 注意：model 列记录 provider_model_id 维度（-think 后缀），与 llm_model.model_key 不同
  ('llm_chat', 'aihubmix', 'claude-sonnet-4-6-think', 'flat', 'call',
   21.60, 108.00, 0, 0,
   21.60, 108.00, 0, 0,
   1, NOW(), NOW()),

  -- DeepSeek V3.2（flat）— input ¥2.16/M, output ¥3.24/M
  ('llm_chat', 'aihubmix', 'deepseek-v3.2', 'flat', 'call',
   2.16, 3.24, 0, 0,
   2.16, 3.24, 0, 0,
   1, NOW(), NOW()),

  -- Gemini 3.1 Pro Preview（tiered_token）— 分档价格见 pricing_rule_tier
  ('llm_chat', 'aihubmix', 'gemini-3.1-pro-preview', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(), NOW()),

  -- GPT 5.4（tiered_token）— 分档价格见 pricing_rule_tier
  ('llm_chat', 'aihubmix', 'gpt-5.4', 'tiered_token', 'call',
   0, 0, 0, 0,
   0, 0, 0, 0,
   1, NOW(), NOW());

-- ============================================================
-- 4. pricing_rule_tier — 8 条分档（Gemini × 4 + GPT × 4）
-- ============================================================
-- token_type: 'input' | 'output'
-- 区间语义：以 input token 数所属档位为索引，output 价格也按该档取值
-- NULL max_tokens 表示不设上限
--
-- Gemini 3.1 Pro Preview 定价（汇率 ×7.2 换算为人民币）：
--   input: ≤200K → ¥14.40/M；>200K → ¥28.80/M
--   output: ≤200K → ¥86.40/M；>200K → ¥129.60/M
--
-- GPT 5.4 定价（汇率 ×7.2 换算为人民币）：
--   input: ≤272K → ¥18.00/M；>272K → ¥36.00/M
--   output: ≤272K → ¥108.00/M；>272K → ¥162.00/M
INSERT IGNORE INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
VALUES
  -- Gemini input 档一：0 ~ 200000
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gemini-3.1-pro-preview'),
    'input', 0, 200000, 14.40, 14.40
  ),
  -- Gemini input 档二：200001 ~ 不限
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gemini-3.1-pro-preview'),
    'input', 200001, NULL, 28.80, 28.80
  ),
  -- Gemini output 档一：0 ~ 200000
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gemini-3.1-pro-preview'),
    'output', 0, 200000, 86.40, 86.40
  ),
  -- Gemini output 档二：200001 ~ 不限
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gemini-3.1-pro-preview'),
    'output', 200001, NULL, 129.60, 129.60
  ),
  -- GPT 5.4 input 档一：0 ~ 272000
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gpt-5.4'),
    'input', 0, 272000, 18.00, 18.00
  ),
  -- GPT 5.4 input 档二：272001 ~ 不限
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gpt-5.4'),
    'input', 272001, NULL, 36.00, 36.00
  ),
  -- GPT 5.4 output 档一：0 ~ 272000
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gpt-5.4'),
    'output', 0, 272000, 108.00, 108.00
  ),
  -- GPT 5.4 output 档二：272001 ~ 不限
  (
    (SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model='gpt-5.4'),
    'output', 272001, NULL, 162.00, 162.00
  );

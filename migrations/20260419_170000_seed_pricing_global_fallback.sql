-- 20260419_170000_seed_pricing_global_fallback.sql
--
-- 添加全局 pricing_rule fallback 行 (llm_chat, '', '')。
-- 背景：S6 smoke 发现 "文章优化助手" (模板 3) 空 model_name 节点 +
-- SalesRAG default provider/model 空 + 多个 dmxapi 新模型未 seed，
-- 导致 CheckAndEstimate pricing lookup 失败 → SOP/SalesRAG 全部阻塞。
--
-- 修复：三级 fallback（store 层 GetPricingRule 改动已在同次 commit）
-- 的最底层兜底行。保守定价（¥3/¥10 per MTok），Reconcile 按真实 cost
-- 多退少补。
--
-- 幂等：如果全局行已存在（多次跑 seed），INSERT IGNORE 防止重复。

INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES (
  'llm_chat', '', '', 'flat', 'call',
  3.0, 10.0, 0, 0,
  3.0, 10.0, 0, 0,
  1, NOW(3), NOW(3)
);

-- 顺手补足 dmxapi 路由下几个实际在用但缺 pricing_rule 的模型
-- （从 dmxapi-ssvip 同模型价格 copy）
INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  ('llm_chat', 'dmxapi', 'claude-sonnet-4-6', 'flat', 'call',
    21.6, 108.0, 0, 0, 21.6, 108.0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'claude-sonnet-4-6-thinking', 'flat', 'call',
    21.6, 108.0, 0, 0, 21.6, 108.0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'DeepSeek-V3.2', 'flat', 'call',
    2.16, 3.24, 0, 0, 2.16, 3.24, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'DeepSeek-V3.2-Thinking', 'flat', 'call',
    2.16, 3.24, 0, 0, 2.16, 3.24, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'gemini-3.1-pro-preview', 'flat', 'call',
    10.0, 30.0, 0, 0, 10.0, 30.0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'gemini-3.1-pro-preview-thinking', 'flat', 'call',
    10.0, 30.0, 0, 0, 10.0, 30.0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'dmxapi', 'gpt-5.4', 'flat', 'call',
    10.0, 40.0, 0, 0, 10.0, 40.0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'aihubmix', 'claude-sonnet-4-6-think', 'flat', 'call',
    21.6, 108.0, 0, 0, 21.6, 108.0, 0, 0, 1, NOW(3), NOW(3));

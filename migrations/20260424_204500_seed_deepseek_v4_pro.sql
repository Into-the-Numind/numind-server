-- 20260424_204500_seed_deepseek_v4_pro.sql
--
-- 在 DB Registry 注册新模型 deepseek-v4-pro，双 provider 路由 + flat 定价。
--
-- # 背景
--
-- 用户要求接入 DeepSeek V4 Pro，两个上游 provider 做主备：
--   - dmxapi    (priority=100, 主)
--   - aihubmix  (priority=10,  备)
--
-- 定价：flat 模式，input ¥14/MTok、output ¥28/MTok，cost = sell（两 provider 同价）。
-- capability_json：1M context window / 384K max output / streaming + tool_use。
-- latency_tier + quality_tier：省略两列 → DB DEFAULT 'standard'（两字段零 read，
-- 作为独立 backlog 任务 remove-tier-fields 后续下线）。
--
-- # Router priority 方向
--
-- registry/store.go:334 `ORDER BY r.priority DESC, r.id ASC` —— **数字大 = 优先级高**。
-- 注意：20260416_100000_seed_aihubmix_provider.sql 的 SQL 注释文字 "升序遍历" 是错的，
-- 与代码实际行为相反（MEMORY 记录：project_ai_service_route_priority.md）。
-- 本 migration 按代码实际语义写：dmxapi=100 会被优先选中，失败再 fallback 到 aihubmix=10。
--
-- # 幂等性
--
-- ai_service.model_key UNIQUE → INSERT IGNORE 保证幂等
-- ai_service_route.uk_model_provider UNIQUE(model_id, provider_id) → INSERT IGNORE 保证幂等
-- pricing_rule.uk_pricing_lookup UNIQUE(service_type, provider, model) → INSERT IGNORE 保证幂等
--
-- # Rollback
-- migrations/20260424_204500_seed_deepseek_v4_pro_rollback.sql

-- ============================================================
-- Pre-flight guard: 两个必需 provider 都得在 llm_provider 表里，
-- 否则下面 INSERT ... SELECT JOIN 会 silently 插 0 行。
-- ============================================================
-- 期望 2 个 provider 全在 → 2-1=1 → 1/1 成功；
-- 若缺一个 → 1-1=0 → 1/0 报错，migration abort。
SELECT 1 / (
  (SELECT COUNT(*) FROM llm_provider WHERE name IN ('dmxapi', 'aihubmix'))
  - 1
) AS provider_guard_expect_2_rows_minus_1_equals_1_else_div_by_zero;


-- ============================================================
-- 1. ai_service — 1 行（新模型 deepseek-v4-pro）
-- ============================================================
-- 省略 latency_tier / quality_tier → 走 DB DEFAULT 'standard'
-- capability_json：1M context / 384K output / streaming + tool_use
INSERT IGNORE INTO ai_service (
  model_key,
  display_name,
  service_type,
  capability_json,
  is_thinking,
  supports_thinking,
  thinking_only,
  is_active,
  created_at,
  updated_at
) VALUES (
  'deepseek-v4-pro',
  'DeepSeek V4 pro',
  'llm',
  JSON_OBJECT(
    'input_modalities',  JSON_ARRAY('text'),
    'output_modalities', JSON_ARRAY('text'),
    'context_window',    1000000,
    'max_output_tokens', 384000,
    'capabilities',      JSON_ARRAY('chat'),
    'features',          JSON_OBJECT('streaming', TRUE, 'tool_use', TRUE)
  ),
  0,
  0,
  0,
  1,
  NOW(3),
  NOW(3)
);


-- ============================================================
-- 2. ai_service_route — 2 行（主备）
-- ============================================================
-- provider_model_id 两边都是 'deepseek-v4-pro'（用户在各自平台上注册的模型名）

-- Route 主：dmxapi priority=100
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'deepseek-v4-pro', 100, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'dmxapi'
WHERE s.model_key = 'deepseek-v4-pro';

-- Route 备：aihubmix priority=10
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'deepseek-v4-pro', 10, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'aihubmix'
WHERE s.model_key = 'deepseek-v4-pro';


-- ============================================================
-- 3. pricing_rule — 2 行（flat，两 provider 同价）
-- ============================================================
-- 单位：元/百万 tokens；cost = sell（积分制下售价通过积分映射体现，不在此层加价）
INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  -- dmxapi / deepseek-v4-pro (flat, ¥14 / ¥28 per MTok)
  ('llm_chat', 'dmxapi', 'deepseek-v4-pro', 'flat', 'call',
   14.00, 28.00, 0, 0,
   14.00, 28.00, 0, 0,
   1, NOW(3), NOW(3)),

  -- aihubmix / deepseek-v4-pro (flat, ¥14 / ¥28 per MTok)
  ('llm_chat', 'aihubmix', 'deepseek-v4-pro', 'flat', 'call',
   14.00, 28.00, 0, 0,
   14.00, 28.00, 0, 0,
   1, NOW(3), NOW(3));


-- ============================================================
-- 验证查询（手工执行，应返回 1 service + 2 routes + 2 pricing）
-- ============================================================
-- SELECT id, model_key, display_name, service_type, latency_tier, quality_tier,
--        is_active, capability_json
-- FROM ai_service WHERE model_key='deepseek-v4-pro';
--
-- SELECT r.model_id, p.name AS provider, r.provider_model_id, r.priority, r.is_active
-- FROM ai_service_route r
-- JOIN ai_service s   ON s.id = r.model_id
-- JOIN llm_provider p ON p.id = r.provider_id
-- WHERE s.model_key = 'deepseek-v4-pro'
-- ORDER BY r.priority DESC;
--
-- SELECT service_type, provider, model, billing_mode,
--        input_price_per_m_tok, output_price_per_m_tok, is_active
-- FROM pricing_rule
-- WHERE model = 'deepseek-v4-pro' AND is_active = 1
-- ORDER BY provider;

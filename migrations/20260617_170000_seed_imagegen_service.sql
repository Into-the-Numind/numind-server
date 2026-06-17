-- 20260617_170000_seed_imagegen_service.sql
--
-- 在 DB Registry 注册 image_gen（文生图）服务，使 agent 的 image_gen 工具的
-- provider 调用走统一 aiservice 网关（Langfuse tracing + 路由/降级 + UsageRecord 分析）。
-- Feature: agent-imagegen-via-aiservice (TD3)
-- Spec 关联: tool 通过 aiservice.ImageGen(ctx, "agent.image_gen", ...) 调用。
-- Rollback: 20260617_170000_seed_imagegen_service_rollback.sql
--
-- # 背景
--
-- 之前 image_gen 工具直接裸 HTTP 打 dmxapi 的 Gemini 文生图端点
-- (/v1beta/models/gemini-2.5-flash-image:generateContent + x-goog-api-key)，
-- 绕过了网关的可观测性/路由/分析。本 migration 把该 provider 调用收编进 registry：
--   - ai_service       (model_key='gemini-2.5-flash-image', service_type='image_gen')
--   - ai_service_route (JOIN llm_provider name='dmxapi', provider_model_id=同名)
--   - task_profile     (task_id='agent.image_gen', service_type='image_gen', default_service_id→上面的 service)
--   - pricing_rule     (image_gen / dmxapi / 模型名, flat per-call —— 仅供 UsageRecord 分析记账)
--
-- # ⚠️ 计费安全（不可弄错）
--
-- 本 profile **绝不** 挂 ChargeUser 的 context_budget policy。
-- 真正的积分扣减是 image_gen 工具自己的扁平 Reserve/Reconcile（imageGenCredits()=10），
-- 这是唯一的一次扣费。aiservice 这条链路只产出一条 UsageRecord（分析/记账，非扣费）：
--   - billing middleware 仅写 UsageRecord，不扣积分；
--   - context_budget middleware 只对带 ChargeUser policy 的 ChatRequest 扣费，
--     而 ImageGenRequest 在 ContextBudgetCredits 里被 asChatReq() 判定为非 chat → 直接 passthrough，
--     不会 reserve/扣费。
-- 因此一次 image_gen = 工具的 10 积分扣一次，aiservice 不会二次扣费、也无免费旁路。
-- → 本 profile 不写 task_profile_service 的 ChargeUser policy；image_gen 也没有
--   context_budget_policy 行（context_budget 只读 chat 路径）。
--
-- # Router priority 方向
--
-- registry/store.go `ORDER BY r.priority DESC` —— 数字大 = 优先级高。单一 provider（dmxapi）
-- priority=100。模型在 dmxapi 平台上的注册名同为 'gemini-2.5-flash-image'。
--
-- # 幂等性
--
-- ai_service.model_key UNIQUE                          → INSERT IGNORE 幂等
-- ai_service_route.uk_model_provider UNIQUE(model,provider) → INSERT IGNORE 幂等
-- task_profile.task_id UNIQUE                          → INSERT IGNORE 幂等（default_service_id 见下）
-- pricing_rule.uk_pricing_lookup UNIQUE(service_type,provider,model) → INSERT IGNORE 幂等
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL（CI 不跑 migrations,
--    见 project_dev_deploy_migration_gap memory）。

-- ============================================================
-- Pre-flight guard: dmxapi provider 必须在 llm_provider 表里，
-- 否则下面 INSERT ... SELECT JOIN 会 silently 插 0 行。
-- 期望恰好 1 个 → 1-1=0... 这里用 (COUNT - 0) 不行；用 1/COUNT：缺则 1/0 报错 abort。
-- ============================================================
SELECT 1 / (SELECT COUNT(*) FROM llm_provider WHERE name = 'dmxapi')
  AS provider_guard_expect_dmxapi_present_else_div_by_zero;


-- ============================================================
-- 1. ai_service — 1 行（image_gen 服务）
-- ============================================================
-- service_type='image_gen'（非 'llm'）→ tracing 走 Span 而非 Generation；
-- billing classifyServiceType 把 ImageGenRequest 归类为 'image_gen' → call_count=1。
-- capability_json：输入 text、输出 image，capabilities=['image_gen']。
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
  'gemini-2.5-flash-image',
  'Gemini 2.5 Flash Image (文生图)',
  'image_gen',
  JSON_OBJECT(
    'input_modalities',  JSON_ARRAY('text'),
    'output_modalities', JSON_ARRAY('image'),
    'capabilities',      JSON_ARRAY('image_gen')
  ),
  0,
  0,
  0,
  1,
  NOW(3),
  NOW(3)
);


-- ============================================================
-- 2. ai_service_route — 1 行（dmxapi 主路由）
-- ============================================================
-- provider_model_id='gemini-2.5-flash-image'（dmxapi 平台上的模型名，
-- 适配器拼到 /v1beta/models/<provider_model_id>:generateContent）。
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'gemini-2.5-flash-image', 100, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'dmxapi'
WHERE s.model_key = 'gemini-2.5-flash-image';


-- ============================================================
-- 3. task_profile — 1 行（agent.image_gen → 上面的 service）
-- ============================================================
-- requirements 空对象（无特殊 capability 匹配需求）。
-- user_selectable=0（系统内部 tool task，用户不直接选模型）。
-- 不挂任何 context_budget ChargeUser policy（计费安全，见文件头）。
INSERT IGNORE INTO task_profile
  (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'agent.image_gen',
  'Agent Image Gen',
  'Agent 文生图工具 (agent-imagegen-via-aiservice): image_gen 工具的 provider 调用走统一网关，获得 Langfuse tracing + 路由/降级 + UsageRecord。积分扣减仍由工具自身扁平 Reserve/Reconcile 承担（唯一扣费），本 profile 不挂 ChargeUser policy。',
  'image_gen',
  JSON_OBJECT(),
  s.id,
  0
FROM ai_service s
WHERE s.model_key = 'gemini-2.5-flash-image'
  AND s.is_active = 1
  AND s.deprecated_at IS NULL
LIMIT 1;


-- ============================================================
-- 4. pricing_rule — 1 行（flat per-call，仅供 UsageRecord 分析成本）
-- ============================================================
-- service_type='image_gen'；flat_unit='call'；price_per_call=¥0.30（成本=售价，
-- 积分制下真实扣费由工具 imageGenCredits()=10 决定，本行只影响 UsageRecord 的 cost 字段）。
-- 估值参考: Gemini 2.5 Flash Image 每张约 ¥0.3（保守上限，可后续按真实成本调整）。
INSERT IGNORE INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES
  ('image_gen', 'dmxapi', 'gemini-2.5-flash-image', 'flat', 'call',
   0, 0, 0.30, 0,
   0, 0, 0.30, 0,
   1, NOW(3), NOW(3));


-- ============================================================
-- 验证查询（手工执行，应返回 1 service + 1 route + 1 profile + 1 pricing）
-- ============================================================
-- SELECT id, model_key, display_name, service_type, is_active, capability_json
-- FROM ai_service WHERE model_key='gemini-2.5-flash-image';
--
-- SELECT r.model_id, p.name AS provider, r.provider_model_id, r.priority, r.is_active
-- FROM ai_service_route r
-- JOIN ai_service s   ON s.id = r.model_id
-- JOIN llm_provider p ON p.id = r.provider_id
-- WHERE s.model_key = 'gemini-2.5-flash-image';
--
-- SELECT task_id, service_type, default_service_id, user_selectable
-- FROM task_profile WHERE task_id='agent.image_gen';
--
-- SELECT service_type, provider, model, billing_mode, price_per_call, is_active
-- FROM pricing_rule WHERE service_type='image_gen' AND is_active=1;

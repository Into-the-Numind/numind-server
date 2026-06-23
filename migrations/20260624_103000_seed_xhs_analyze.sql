-- Migration: register xhs.note_analyze task profile + ensure deepseek-v4-flash pricing.
-- Feature: xhs-collector (T4 AI 分析). Rollback: 20260624_103000_seed_xhs_analyze_rollback.sql
--
-- biz/xhs.analyzeNote(ctx, userID, note) calls aiservice.Chat(ctx, "xhs.note_analyze", req)
-- inside billing.WithBilling → a NORMAL billable Reserve/Reconcile call (deepseek-v4-flash
-- has a NON-zero pricing_rule, so it is NOT the IsFreeModel zero-cost exemption; the user is
-- charged per the three-pool deduction). Routed to the cheap non-thinking model
-- deepseek-v4-flash, distinct from chatbot.stream so swapping this model never affects the
-- chatbot product.
--
-- 字段映射 (同 meeting.feedback / meeting.summary profile):
--   task_id            'xhs.note_analyze'
--   service_type       'llm'
--   requirements       JSON_OBJECT()  空对象 — 无特殊 capability 需求
--   default_service_id link to ai_service.id WHERE model_key = 'deepseek-v4-flash'
--   user_selectable    0 — 内部富化流水线 task, 用户不直接选模型
--
-- 路由说明: deepseek-v4-flash(ai_service) 已有 ai_service_route(session.title / salesrag.intent
-- 在用), 故只需建 task_profile 的 default_service_id 绑定即可; ResolveTask 走 default_service_id。
--
-- Idempotent:
--   - task_profile: INSERT IGNORE (UNIQUE on task_id), 安全跑多次。
--   - pricing_rule: WHERE NOT EXISTS 防重复 (deepseek-v4-flash pricing 可能已由
--     20260618_140000_swap_salesrag_intent_to_v4flash.sql 插入; 此处兜底, 确保即使在
--     未跑过 salesrag swap 的 fresh 环境上 xhs.note_analyze 仍有非零 token 价可计费)。
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations, project_dev_deploy_migration_gap)。
-- 若 deepseek-v4-flash 在目标环境 ai_service 不存在/未激活, task_profile SELECT 返回空 →
-- INSERT IGNORE 跳过 → xhs.note_analyze 无 profile → ResolveTask 报错 → AI 富化失败
-- (enrich_status=failed, 不阻塞入库)。部署前 verify:
--   SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-flash' AND is_active = 1 AND deprecated_at IS NULL;

-- 1. 兜底确保 deepseek-v4-flash 的 token 定价存在 (非零 → 普通 Reserve/Reconcile 计费)。
--    cost = sell, 无加价 (同 deepseek-v4-pro / qwen-turbo-latest 约定; cache-hit 0.02)。
INSERT INTO pricing_rule
  (service_type, provider, model, billing_mode, input_price_per_m_tok, output_price_per_m_tok,
   cached_input_price_per_m_tok, sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   sell_cached_input_price_per_m_tok, credit_multiplier, is_active, created_at, updated_at)
SELECT 'llm_chat', 'dmxapi', 'deepseek-v4-flash', 'flat', 0.85, 1.70,
       0.02, 0.85, 1.70, 0.02, 1.00, 1, NOW(3), NOW(3)
FROM DUAL
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule WHERE provider = 'dmxapi' AND model = 'deepseek-v4-flash'
);

-- 2. 注册 xhs.note_analyze task_profile, 绑定到 deepseek-v4-flash。
INSERT IGNORE INTO task_profile
  (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'xhs.note_analyze',
  'XHS Note Analyze',
  '小红书选题采集: 单笔记 AI 分析(6 字段). 路由到便宜非思考模型(deepseek-v4-flash, 非零定价), 走普通 Reserve/Reconcile 计费, 用户扣分.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v4-flash'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;

-- 验证查询 (手工执行):
--   SELECT tp.task_id, s.model_key, tp.user_selectable
--   FROM task_profile tp JOIN ai_service s ON s.id = tp.default_service_id
--   WHERE tp.task_id = 'xhs.note_analyze';
--   SELECT service_type, provider, model, input_price_per_m_tok, output_price_per_m_tok, is_active
--   FROM pricing_rule WHERE model = 'deepseek-v4-flash' AND is_active = 1;

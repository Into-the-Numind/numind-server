-- Migration: register meeting.feedback task profile
-- Feature: meeting-feedback-flash (会议副驾实时反馈切到快模型, 降延迟)
-- Rollback: 20260618_170000_register_meeting_feedback_profile_rollback.sql
--
-- biz/meeting.GenerateFeedback(ctx, ...) calls aiservice.ChatStream(ctx, "meeting.feedback", req)
-- for the real-time judge+generate. Routed to deepseek-v4-flash (thinking_only=0) and the
-- request carries Thinking=false → NON-thinking → low latency. This is intentionally a
-- SEPARATE task profile from chatbot.stream (deepseek-v4-pro, thinking_only) so switching
-- the meeting feedback model never affects the chatbot product. The call is system-internal
-- (internalCallCtx zeroes userID, no ContextFragments) → gateway pass-through, never bills.
--
-- 字段映射 (同 session.title / dialectic / digest profile):
--   task_id            'meeting.feedback'
--   service_type       'llm'
--   requirements       JSON_OBJECT()  空对象 — 无特殊 capability 需求
--   default_service_id link to ai_service.id WHERE model_key = 'deepseek-v4-flash'
--   user_selectable    0 — 系统内部 task, 用户不直接选模型
--
-- 路由说明: deepseek-v4-flash(ai_service) 已有 ai_service_route(session.title 在用), 故只需
-- 建 task_profile 的 default_service_id 绑定即可; ResolveTask 走 default_service_id(无需
-- task_profile_service 可见性行, 与 session.title 一致 — 已验证 flash 无 task_profile_service 行仍可用)。
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id). 安全跑多次.
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations, 见 project_dev_deploy_migration_gap)。
-- 若 deepseek-v4-flash 在目标环境 ai_service 不存在/未激活, SELECT 返回空 → INSERT IGNORE 跳过 →
-- meeting.feedback 无 profile → ResolveTask 报错 → 实时反馈失败(需先在该环境注册 flash 模型)。
-- 部署前 verify:
--   SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-flash' AND is_active = 1 AND deprecated_at IS NULL;

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'meeting.feedback',
  'Meeting Feedback',
  '会议副驾实时反馈(判官+生成): 低延迟, 路由到非思考快模型(deepseek-v4-flash). 系统内部调用, 不扣用户积分.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v4-flash'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;

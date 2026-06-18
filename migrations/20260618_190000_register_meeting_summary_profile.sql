-- Migration: register meeting.summary task profile
-- Feature: meeting-summary-flash (会议副驾滚动摘要折叠切到快模型, 省时省钱)
-- Rollback: 20260618_190000_register_meeting_summary_profile_rollback.sql
--
-- biz/meeting.updateRunningSummary(ctx, ...) calls aiservice.Chat(ctx, "meeting.summary", req)
-- 后台每 ~90s/1500 字把新转写折进 running_summary。路由到 deepseek-v4-flash(thinking_only=0)
-- 且 Thinking=false → 非思考：摘要不需要推理，又是高频后台调用，flash 快且便宜。
-- 独立于 chatbot.stream(deepseek-v4-pro)。注意：会后【最终纪要】(generateSummary /
-- generateFinalSummary) 仍走 chatbot.stream(pro) 不变，本 profile 只服务滚动摘要折叠(③)。
-- 系统内部调用(internalCallCtx, userID=0), 无 fragment → 网关直通, 不计费。
--
-- 字段映射 (同 session.title / meeting.feedback): service_type=llm, requirements=空,
--   default_service_id→deepseek-v4-flash, user_selectable=0. ResolveTask 走 default_service_id
--   (flash 已有 ai_service_route, 无需 task_profile_service 行)。
-- Idempotent: INSERT IGNORE (UNIQUE task_id). 安全跑多次。
-- ⚠️ Dev/Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations)。verify:
--   SELECT id FROM ai_service WHERE model_key='deepseek-v4-flash' AND is_active=1 AND deprecated_at IS NULL;

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'meeting.summary',
  'Meeting Rolling Summary',
  '会议副驾滚动摘要折叠(running memory): 后台高频, 路由到非思考快模型(deepseek-v4-flash). 系统内部调用, 不扣用户积分.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v4-flash'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;

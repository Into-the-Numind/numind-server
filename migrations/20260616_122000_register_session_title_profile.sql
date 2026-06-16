-- Migration: register session.title task profile
-- Feature: adaptive-session-titles (auto session title generation)
-- Spec: numind-server/docs/superpowers/specs/2026-06-16-adaptive-session-titles-design.md §2.2
-- Rollback: 20260616_122000_register_session_title_profile_rollback.sql
--
-- biz/sessiontitle.Generate(ctx, ...) calls aiservice.Chat(ctx, "session.title", req)
-- after the first conversation turn (chatbot ChatStream / agent finalizeRun) to
-- summarise the exchange into a short 6-12 char title. The call carries NO
-- ContextFragments and strips its billing context, so the gateway takes the
-- no-fragment pass-through branch and never reserves credits — title generation
-- is system-internal and never bills the user.
--
-- Model choice: agnes-2.0-flash (the 0-price model). Rationale:
--   - FREE — titles run on every new session; a 0-price model keeps it free.
--   - Member-only free-model gate is BYPASSED here because Generate zeroes the
--     userID (system call), so a non-member user's session still gets a title.
--   - Reliable on dev AND prod (it is the default chat model in both).
--   - Ali qwen-turbo was rejected: dev hits AllocationQuota.FreeTierOnly (HTTP 403).
--   Code no longer hardcodes a model (ModelOverride removed) so ops can repoint
--   this profile's default to any compatible LLM via the admin console — e.g.
--   back to qwen-turbo once the Ali paid tier is enabled, for lower latency.
--
-- 字段映射 (与 dialectic/digest/sanitize profile 一致):
--   task_id            'session.title'
--   service_type       'llm'
--   requirements       JSON_OBJECT()  空对象 — 无特殊 capability 需求
--   default_service_id link to ai_service.id WHERE model_key = 'agnes-2.0-flash'
--   user_selectable    0 — 系统内部 task, 用户不直接选模型
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id). 安全跑多次.
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations, 见
-- project_dev_deploy_migration_gap memory). 若 agnes-2.0-flash 在目标环境
-- ai_service 表不存在, SELECT 返回空, INSERT IGNORE 跳过 → 标题生成 graceful no-op
-- (best-effort, 不报错不扣费). 部署前 verify:
--   SELECT id FROM ai_service WHERE model_key = 'agnes-2.0-flash' AND is_active = 1 AND deprecated_at IS NULL;

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'session.title',
  'Session Title',
  '会话自适应标题生成 (adaptive-session-titles): 首轮对话后用便宜/免费模型生成 6-12 字内容标题. 系统内部调用, 不扣用户积分.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'agnes-2.0-flash'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;

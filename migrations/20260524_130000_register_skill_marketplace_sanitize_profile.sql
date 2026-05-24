-- Migration: register skill.marketplace.sanitize task profile
-- Feature: agent-mode-v2-skill-marketplace (T3 sanitize pipeline — T7 closing S4-T3-D2 tech debt)
-- Spec: numind-server/docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md §3.2
-- Rollback: 20260524_130000_register_skill_marketplace_sanitize_profile_rollback.sql
--
-- 调用 aiservice.Chat(ctx, "skill.marketplace.sanitize", req) 时 task_profile
-- 把请求路由到默认服务 (deepseek-v4-pro via DMXAPI). 管理端可后续切换到其它兼容 LLM.
--
-- 选 deepseek-v4-pro (DMXAPI) 理由 (spec §3.2 决策修订):
--   - body 通常 <5KB, latency budget <3s
--   - 输出确定性要求高（脱敏一致性优先于多样化），不需要 thinking model
--   - 阿里云 DashScope 账户在 "free tier only" 模式下 qwen-turbo/plus 配额耗尽
--     (2026-05-24 dev 验证: HTTP 403 AllocationQuota.FreeTierOnly), DMXAPI 聚合
--     平台无此限制
--   - dev 实际验证 sanitize-preview HTTP 200 通过
--
-- 历史对照 (同模式 migration):
--   - 20260524_141000_register_digest_profile.sql (agent.digest → deepseek-v3.2)
--   - 20260523_154000_register_dialectic_profile.sql (agent.dialectic → qwen-plus)
--
-- 字段映射 (与 #1.5 agent-memory 4 个 profile 一致):
--   task_id            'skill.marketplace.sanitize'
--   display_name       人类可读名（管理端列表展示）
--   description        用途说明
--   service_type       'llm' (text generation, no images/audio)
--   requirements       JSON_OBJECT() 空对象 — 无特殊 capability 需求
--   default_service_id link to ai_service.id WHERE model_key = 'deepseek-v4-pro'
--   user_selectable    0 — 系统内部 task，用户不直接选模型 (与 dialectic / digest 同模式)
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id 拦重复). 安全跑多次.
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations, 见 project_dev_deploy_migration_gap memory).
-- 若 deepseek-v4-pro 在目标环境 ai_service 表中不存在, SELECT 返回空, INSERT IGNORE 跳过 — sanitize
-- 调用会 surface ErrSanitizeUnavailable (T7 errno.Marketplace.SanitizeUnavailable, HTTP 503),
-- 这是预期 graceful degradation. 部署前 verify:
--   SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-pro' AND is_active = 1 AND deprecated_at IS NULL;

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'skill.marketplace.sanitize',
  'Skill Marketplace Sanitize',
  'V2 跨租户 Skill 发布脱敏：LLM 实体识别去除人名/机构/产品名 (spec §3.2 Stage 2). 前端 diff 视图人工 review gate 是最终防线.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v4-pro'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;

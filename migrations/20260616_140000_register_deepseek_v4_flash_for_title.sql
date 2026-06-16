-- Migration: register deepseek-v4-flash ai_service (dmxapi route) + repoint session.title to it
-- Feature: instant-title-ux (session title model switch agnes → deepseek-v4-flash)
-- Rollback: 20260616_140000_register_deepseek_v4_flash_for_title_rollback.sql
--
-- User chose deepseek-v4-flash (via DMXAPI) for adaptive session-title generation,
-- replacing the free agnes-2.0-flash (registered in 20260616_122000). Reason: better
-- title quality; the per-token cost is borne by the company (title generation is still
-- FREE to the user — sessiontitle.Generate strips billing ctx + sends no fragments → the
-- gateway passes through without reserving credits).
--
-- Model is a thinking/non-thinking hybrid; titles use NON-thinking:
--   is_thinking=0 (default off), supports_thinking=1, thinking_only=0 (NOT forced thinking,
--   unlike deepseek-v4-pro which is thinking_only=1). sessiontitle.Generate never sets the
--   Thinking flag, so the call runs non-thinking.
--
-- Pricing reference (¥, per 1M tokens): input 0.85 / output 1.7 / cache-hit 0.02. NOTE: the
-- billing middleware reads price from the pricing_rule table, NOT ai_service_route — the
-- route price columns below are reference/display only. session.title is a no-bill path
-- (Generate strips billing ctx) so no pricing_rule row is required here; if this model is
-- later bound to a BILLABLE task_profile, add an INSERT INTO pricing_rule then.
--
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations, project_dev_deploy_migration_gap).
-- Verify provider exists first: SELECT id FROM llm_provider WHERE name='dmxapi' AND is_active=1;

-- 1. Register the service (idempotent on UNIQUE model_key).
INSERT IGNORE INTO ai_service
  (model_key, display_name, service_type, capability_json, latency_tier, quality_tier,
   is_thinking, supports_thinking, thinking_only, thinking_style, is_active)
VALUES
  ('deepseek-v4-flash', 'DeepSeek V4 Flash', 'llm',
   JSON_OBJECT(
     'capabilities', JSON_ARRAY('chat'),
     'features', JSON_OBJECT('tool_use', true, 'streaming', true),
     'context_window', 1000000,
     'max_output_tokens', 384000,
     'input_modalities', JSON_ARRAY('text'),
     'output_modalities', JSON_ARRAY('text')
   ),
   'fast', 'standard', 0, 1, 0, '', 1);

-- 2. Route to dmxapi. INSERT IGNORE is idempotent via uk_model_provider(model_id, provider_id).
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, input_price_per_mtok, output_price_per_mtok, pricing_unit, is_active, created_at, updated_at)
SELECT s.id, p.id, 'deepseek-v4-flash', 10, 0.85, 1.7, 'per_1m_tokens', 1, NOW(3), NOW(3)
FROM ai_service s
JOIN llm_provider p ON p.name = 'dmxapi' AND p.is_active = 1
WHERE s.model_key = 'deepseek-v4-flash';

-- 3. Repoint the session.title task profile to deepseek-v4-flash.
UPDATE task_profile tp
JOIN ai_service s ON s.model_key = 'deepseek-v4-flash' AND s.is_active = 1 AND s.deprecated_at IS NULL
SET tp.default_service_id = s.id
WHERE tp.task_id = 'session.title';

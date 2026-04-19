-- Rollback for 20260417_180000_drop_llm_compat_views.sql
--
-- Re-creates the llm_model + llm_model_provider read-only compat VIEWs with the
-- exact DDL originally shipped in migrations/20260416_000001_ai_service_manager.sql.
-- Use this only if the DROP turned out to have missed a consumer.
--
-- Note: if you reach for this rollback, you also need to revert the code
-- commits in each repo (run `git revert` per hash within each repo's working
-- copy, then push). VIEWs alone being back is not sufficient if the code
-- doesn't read them anymore:
--   numind-admin-web: revert 461e5ee (T1)
--   numind-server:    revert 315b1a0 (T6), 3dd04a8 (T3), f67e066 (T2)
--   numind-web-v3:    revert 7cd83c5 (T4)
-- Revert in reverse-dependency order (T6 first, T2 last) to keep compile green
-- at each step.

CREATE OR REPLACE VIEW llm_model AS
  SELECT id, model_key, display_name, is_thinking, base_model_id,
         supports_thinking, icon, sort_order, is_active,
         created_at, updated_at
  FROM ai_service
  WHERE service_type = 'llm' AND deprecated_at IS NULL;

CREATE OR REPLACE VIEW llm_model_provider AS
  SELECT r.id, r.model_id, r.provider_id, r.provider_model_id, r.priority,
         r.input_price_per_mtok, r.output_price_per_mtok, r.is_active,
         r.created_at, r.updated_at
  FROM ai_service_route r
  JOIN ai_service s ON s.id = r.model_id
  WHERE s.service_type = 'llm' AND s.deprecated_at IS NULL;

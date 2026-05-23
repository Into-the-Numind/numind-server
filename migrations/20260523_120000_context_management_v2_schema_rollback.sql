-- Rollback: Context Management V2 schema (Agent Mode V1.5 板块 2 Task 2.1)
-- Pairs with: 20260523_120000_context_management_v2_schema.sql
-- Strategy: drop V2-only objects; V1 fields `compact_state` / `compact_summary` are untouched.

DROP TABLE IF EXISTS agent_tool_artifact;

-- 用 `DROP COLUMN IF EXISTS` 保 idempotent（MySQL 8.0.29+）— 重跑 rollback 不会因列已删除报错。
ALTER TABLE agent_run
  DROP COLUMN IF EXISTS compact_state_v2,
  DROP COLUMN IF EXISTS total_tokens_used_v2,
  DROP COLUMN IF EXISTS use_compact_v2,
  DROP COLUMN IF EXISTS context_window_limit_v2;

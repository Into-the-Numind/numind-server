-- agent-mode-billing-integration #12/14
-- 2026-05-21
-- Rollback: drop terminal_metadata column from agent_run

ALTER TABLE agent_run DROP COLUMN terminal_metadata;

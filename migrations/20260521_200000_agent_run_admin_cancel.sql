-- Agent Mode #14/14 — Phase C admin features support (C3 force-cancel + C4 monitoring join).
-- Adds two nullable columns to agent_run; safe to apply to any environment.
-- Idempotent: uses ADD COLUMN IF NOT EXISTS where supported (MySQL 8.0.29+).
-- For older MySQL run wrapped in a procedure that checks information_schema.

-- C3: cancellation_requested_at — written by POST /v1/admin/agent-runs/:id/cancel.
-- terminal_reason='cancelled' + terminal_metadata JSON records who cancelled (I2 unchanged).
ALTER TABLE agent_run
  ADD COLUMN cancellation_requested_at DATETIME NULL DEFAULT NULL COMMENT '#14 admin force-cancel timestamp';

-- C4: agent_definition_id — join key for monitoring queries (filter by parent_user_id).
-- runner.go (M-A1) writes this from req.AgentDefID on run creation.
ALTER TABLE agent_run
  ADD COLUMN agent_definition_id BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '#14 join key to agent_definition.id';

ALTER TABLE agent_run
  ADD INDEX idx_ar_agent_def_id (agent_definition_id);

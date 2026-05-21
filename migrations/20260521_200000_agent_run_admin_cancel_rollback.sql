-- Rollback for 20260521_200000_agent_run_admin_cancel.sql
-- Removes the 2 new columns + the index.

ALTER TABLE agent_run DROP INDEX idx_ar_agent_def_id;
ALTER TABLE agent_run DROP COLUMN agent_definition_id;
ALTER TABLE agent_run DROP COLUMN cancellation_requested_at;

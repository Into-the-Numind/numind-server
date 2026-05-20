-- agent-mode-billing-integration #12/14
-- 2026-05-21
-- Add terminal_metadata JSON column to agent_run for BudgetExceeded structured detail

ALTER TABLE agent_run
  ADD COLUMN terminal_metadata JSON NULL COMMENT 'Terminal 时机的结构化元数据（如 budget_dimension）'
  AFTER state_reason;

-- Rollback: context_budget_compression
-- Reverts: 20260425_172000_context_budget_compression.sql
-- Date: 2026-04-25
-- Description: Drop new tables and new credit_reservation columns added by context-budget-compression migration.

-- ---------------------------------------------------------------------------
-- Revert credit_reservation ALTER
-- Restore coefficient_id NOT NULL and drop all new columns/indexes.
-- ---------------------------------------------------------------------------
ALTER TABLE credit_reservation
  DROP INDEX idx_cr_budget_event,
  DROP INDEX idx_cr_token_profile,
  DROP COLUMN context_budget_event_id,
  DROP COLUMN model,
  DROP COLUMN provider,
  DROP COLUMN estimated_completion_tokens,
  DROP COLUMN estimated_prompt_tokens,
  DROP COLUMN token_profile_id,
  DROP COLUMN estimation_source,
  MODIFY coefficient_id BIGINT UNSIGNED NOT NULL;

-- ---------------------------------------------------------------------------
-- Drop new tables (reverse creation order to respect soft FK dependencies)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS context_budget_event;
DROP TABLE IF EXISTS context_summary;
DROP TABLE IF EXISTS context_budget_policy;
DROP TABLE IF EXISTS token_estimation_profile;

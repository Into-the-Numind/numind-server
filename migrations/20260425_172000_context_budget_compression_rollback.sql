-- Rollback: context_budget_compression
-- Reverts: 20260425_172000_context_budget_compression.sql
-- Date: 2026-04-25
-- Description: Drop new tables and new credit_reservation columns added by context-budget-compression migration.

-- ---------------------------------------------------------------------------
-- Revert credit_reservation ALTER
-- Restore coefficient_id NOT NULL and drop all new columns/indexes.
-- ---------------------------------------------------------------------------
-- 修复：将所有 NULL coefficient_id 兜底为 0，避免恢复 NOT NULL 时失败
-- 场景：生产若已有 estimation_source='context_budget' 的行，其 coefficient_id IS NULL
UPDATE credit_reservation SET coefficient_id = 0 WHERE coefficient_id IS NULL;

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
  MODIFY coefficient_id BIGINT UNSIGNED NOT NULL DEFAULT 0;

-- ---------------------------------------------------------------------------
-- Drop new tables (reverse creation order to respect soft FK dependencies)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS context_budget_event;
DROP TABLE IF EXISTS context_summary;
DROP TABLE IF EXISTS context_budget_policy;
DROP TABLE IF EXISTS token_estimation_profile;

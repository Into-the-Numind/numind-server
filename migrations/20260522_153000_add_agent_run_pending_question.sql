-- Migration: add pending_question_* columns to agent_run table.
-- Feature: agent-mode-p0-tools T4 (ask_user_question tool yield protocol).
-- Date: 2026-05-22
--
-- The application's GORM AutoMigrate adds these columns automatically on startup
-- (environments where AutoMigrate runs: local / dev / qa).
-- This SQL file is for prod environments where AutoMigrate is disabled
-- and must be applied manually before deploying the binary that includes T4.
--
-- Idempotent: uses IF NOT EXISTS / conditional ADD so it can be re-run safely.

ALTER TABLE `agent_run`
  ADD COLUMN IF NOT EXISTS `pending_question_json` JSON          NULL COMMENT 'ask_user_question YieldPayload JSON; non-null when state_reason=waiting_for_user_choice',
  ADD COLUMN IF NOT EXISTS `pending_question_at`   TIMESTAMP(3) NULL COMMENT 'timestamp when the question was enqueued; used for SLA/timeout tracking';

-- Composite index speeds up admin queries that filter by state_reason + queue time.
CREATE INDEX IF NOT EXISTS `idx_ar_state_pending`
  ON `agent_run`(`state_reason`, `pending_question_at`);

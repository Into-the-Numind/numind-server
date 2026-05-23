-- Rollback: Drop legacy compact columns from agent_run
-- Pairs with: 20260523_180000_drop_legacy_compact_columns.sql
--
-- 重新加回 5 列以恢复 schema。所有列允许 NULL（恢复后无数据写入，全空）。
-- 列的精确类型 / 注释 取自原 task 2.1 migration（20260523_120000）与 task 2.1 之前
-- 既有 schema 定义。

ALTER TABLE agent_run
  ADD COLUMN IF NOT EXISTS compact_state           JSON         NULL                COMMENT 'legacy V1 compact state (deprecated; restored by rollback)',
  ADD COLUMN IF NOT EXISTS compact_summary         LONGTEXT     NULL                COMMENT 'legacy V1 compact summary (deprecated; restored by rollback)',
  ADD COLUMN IF NOT EXISTS compact_state_v2        JSON         NULL                COMMENT 'legacy V2 compactv2 state (deprecated; restored by rollback)',
  ADD COLUMN IF NOT EXISTS total_tokens_used_v2    BIGINT       NOT NULL DEFAULT 0  COMMENT 'legacy V2 token usage counter (deprecated; restored by rollback)',
  ADD COLUMN IF NOT EXISTS context_window_limit_v2 INT          NULL                COMMENT 'legacy V2 context window override (deprecated; restored by rollback)';

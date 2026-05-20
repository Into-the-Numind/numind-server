-- agent-mode #9 compact: ALTER agent_run table to add compact tracking columns
-- - compact_state JSON nullable: CompactStateV1 序列化（last_compact_at / last_boundary_message_id /
--   total_compact_attempts / consecutive_failures / summary_token_count / strategy_used）
-- - compact_summary LONGTEXT nullable: 最新 CompactSummary 全文，恢复时快速读避免遍历 messages
-- 不动既有列、不动 CHECK constraint；旧 agent_run 行保持兼容（NULL 默认）
-- AutoMigrate 自动同步 schema 加列；本 SQL 主要用于上线时手工 SSH 部署执行（dev/prod CI 不跑 migration）

ALTER TABLE agent_run
  ADD COLUMN compact_state    JSON     NULL COMMENT 'CompactStateV1 JSON: last_compact_at / last_boundary_message_id / total_compact_attempts / consecutive_failures / summary_token_count / strategy_used',
  ADD COLUMN compact_summary  LONGTEXT NULL COMMENT '最新 CompactSummary 全文，恢复时快速读避免遍历 messages';

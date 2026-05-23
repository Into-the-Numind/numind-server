-- Migration: add user_memory_profile.extraction_count_since_rebuild
-- Feature: agent-mode-v15-memory-layer-a (Task 3.3 — LLM Extraction Async Pipeline)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-03-llm-extraction.md
-- Rollback: 20260523_150000_add_profile_extraction_count_rollback.sql
--
-- Scope:
--   ExtractorService 每次抽取后给 user_memory_profile.extraction_count_since_rebuild 自增 1.
--   累计到阈值 (默认 5) 后触发 RebuildNarrative -> 重写 work_context/personal_context/top_of_mind
--   三段叙事并把计数重置为 0.
--
-- Idempotent guard:
--   MySQL 8 不支持 IF NOT EXISTS on ALTER COLUMN,只能 SHOW COLUMNS + 动态 SQL.
--   生产环境 schema migration 工具 (numind-server/scripts/db/migrate.sh) 会前置检测列存在,
--   所以这里直接 ADD COLUMN; 重复执行会返回 ER_DUP_FIELDNAME, 工具会忽略.

ALTER TABLE user_memory_profile
  ADD COLUMN extraction_count_since_rebuild INT NOT NULL DEFAULT 0
    COMMENT 'Task 3.3 ExtractorService 累计抽取次数; 达阈值 5 触发 RebuildNarrative 后重置 0';

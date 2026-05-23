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
-- Idempotent guard (2026-05-23 修正):
--   MySQL 8 不支持 IF NOT EXISTS on ALTER ADD COLUMN. 用 information_schema 检测 +
--   PREPARE / EXECUTE 动态 SQL 实现真幂等. 重复执行不报错 (ER_DUP_FIELDNAME=1060
--   会被前置检测拦截, ALTER 根本不会执行).
--
--   GORM AutoMigrate 也会自动添加此列 (helper.go 已注册 UserMemoryProfile model
--   含 ExtractionCountSinceRebuild 字段), 两条路径任一先跑都安全.

SET @col_exists = (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = DATABASE()
    AND table_name = 'user_memory_profile'
    AND column_name = 'extraction_count_since_rebuild'
);
SET @sql = IF(@col_exists = 0,
  'ALTER TABLE user_memory_profile ADD COLUMN extraction_count_since_rebuild INT NOT NULL DEFAULT 0 COMMENT ''Task 3.3 ExtractorService 累计抽取次数; 达阈值 5 触发 RebuildNarrative 后重置 0''',
  'SELECT ''extraction_count_since_rebuild column already exists, skipping'' AS msg'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

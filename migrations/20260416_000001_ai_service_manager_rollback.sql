-- ============================================================
-- Rollback: AI Service Manager Schema
-- File: 20260416_000001_ai_service_manager_rollback.sql
-- 对应 Migration: 20260416_000001_ai_service_manager.sql
-- 执行方向: 倒序（Appendix A 原文顺序）
-- ⚠ 执行前必须停服，防止在线流量打到已部分回滚的 schema
-- ============================================================

-- Step 1: 清除 seed 数据（倒序：先删 task_profile_service，再 task_profile，再 OCR/ASR 服务行）
DELETE FROM task_profile_service;
DELETE FROM task_profile;
DELETE FROM ai_service WHERE service_type IN ('ocr', 'asr');

-- Step 2: 删除新增的三张表
DROP TABLE IF EXISTS ai_service_audit_log;
DROP TABLE IF EXISTS task_profile_service;
DROP TABLE IF EXISTS task_profile;

-- Step 3: 恢复 usage_record（先删索引，再删列；MySQL 要求顺序）
ALTER TABLE usage_record
  DROP INDEX idx_task_created,
  DROP INDEX idx_ur_service_type,
  DROP COLUMN is_estimated,
  DROP COLUMN pricing_second_snapshot,
  DROP COLUMN pricing_call_snapshot,
  DROP COLUMN pricing_output_snapshot,
  DROP COLUMN pricing_input_snapshot,
  DROP COLUMN duration_seconds,
  DROP COLUMN call_count,
  DROP COLUMN unit,
  DROP COLUMN task_id,
  DROP COLUMN service_type;

-- Step 4: 恢复 ai_service_route → llm_model_provider

-- 4a: 删除读兼容 VIEW
DROP VIEW IF EXISTS llm_model_provider;

-- 4b: 删除新增列
ALTER TABLE ai_service_route
  DROP COLUMN price_per_second,
  DROP COLUMN price_per_call,
  DROP COLUMN pricing_unit;

-- 4c: 改名回原表名
ALTER TABLE ai_service_route RENAME TO llm_model_provider;

-- Step 5: 恢复 ai_service → llm_model

-- 5a: 删除读兼容 VIEW
DROP VIEW IF EXISTS llm_model;

-- 5b: 删除新增索引（必须在删列之前）
ALTER TABLE ai_service
  DROP INDEX idx_deprecated,
  DROP INDEX idx_service_type;

-- 5c: 删除新增列
ALTER TABLE ai_service
  DROP COLUMN deprecated_at,
  DROP COLUMN tags,
  DROP COLUMN quality_tier,
  DROP COLUMN latency_tier,
  DROP COLUMN capability_json,
  DROP COLUMN service_type;

-- 5d: 改名回原表名
ALTER TABLE ai_service RENAME TO llm_model;

-- Step 6: 恢复 llm_provider

-- 6a: 删除 migration 新增的 provider 行
DELETE FROM llm_provider WHERE provider_type IN ('ocr', 'asr', 'file_service');
-- 注：ali-dashscope 和 volc-ark 的 provider_type='llm'，也是本 migration 新增的
-- 通过 name 字段精确删除
DELETE FROM llm_provider WHERE name IN ('ali-dashscope', 'volc-ark');

-- 6b: 删除新增索引（必须在删列之前）
ALTER TABLE llm_provider
  DROP INDEX idx_provider_type;

-- 6c: 删除新增列
ALTER TABLE llm_provider
  DROP COLUMN supports_streaming,
  DROP COLUMN provider_type;

-- ============================================================
-- Rollback 完成
-- 验证查询：
--   SHOW TABLES LIKE 'llm_model';                  -- 应存在（原表名）
--   SHOW TABLES LIKE 'llm_model_provider';         -- 应存在（原表名）
--   SHOW TABLES LIKE 'ai_service';                 -- 应不存在
--   SHOW TABLES LIKE 'ai_service_route';           -- 应不存在
--   SHOW TABLES LIKE 'task_profile';               -- 应不存在
--   SHOW TABLES LIKE 'task_profile_service';       -- 应不存在
--   SHOW TABLES LIKE 'ai_service_audit_log';       -- 应不存在
--   SHOW COLUMNS FROM usage_record LIKE 'task_id'; -- 应不存在
--   SHOW COLUMNS FROM llm_provider LIKE 'provider_type'; -- 应不存在
-- ============================================================

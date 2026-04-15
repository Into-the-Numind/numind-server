-- ============================================================
-- Rollback: AI Service Manager Schema
-- File: 20260416_000001_ai_service_manager_rollback.sql
-- 对应 Migration: 20260416_000001_ai_service_manager.sql
-- 执行方向: 倒序（Appendix A 原文顺序）
-- ⚠ 执行前必须停服，防止在线流量打到已部分回滚的 schema
-- ⚠ 本 rollback 使用 PROCEDURE 条件 DDL，可重复执行（幂等）
-- ⚠ 请仅在隔离 docker 环境演练；不要直接在 dev 环境执行（dev 的
--    usage_record.service_type 来自老 billing schema，不应被删除）
-- ============================================================

-- Step 1: 清除 seed 数据（倒序：先删 task_profile_service，再 task_profile，再 OCR/ASR 服务行）
-- 使用条件 PROCEDURE 避免表不存在时报错（幂等）
DROP PROCEDURE IF EXISTS _rb_seed_cleanup;
DELIMITER //
CREATE PROCEDURE _rb_seed_cleanup()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task_profile_service'
  ) THEN
    DELETE FROM task_profile_service;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task_profile'
  ) THEN
    DELETE FROM task_profile;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service'
  ) THEN
    DELETE FROM ai_service WHERE service_type IN ('ocr', 'asr');
  END IF;
END//
DELIMITER ;
CALL _rb_seed_cleanup();
DROP PROCEDURE IF EXISTS _rb_seed_cleanup;

-- Step 2: 删除新增的三张表
DROP TABLE IF EXISTS ai_service_audit_log;
DROP TABLE IF EXISTS task_profile_service;
DROP TABLE IF EXISTS task_profile;

-- Step 3: 恢复 usage_record（先删索引，再删列；MySQL 要求顺序）
-- 使用 PROCEDURE 条件检查，幂等可重复执行
-- service_type 列通过 COMMENT tag [ai-service-manager:v1] 精确判断：
--   仅当列存在且 COMMENT 包含 tag 时才删除（避免误删 dev 已有的 service_type 列）

DROP PROCEDURE IF EXISTS _rb_usage_record;
DELIMITER //
CREATE PROCEDURE _rb_usage_record()
BEGIN
  -- idx_task_created
  IF EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND INDEX_NAME = 'idx_task_created'
  ) THEN
    ALTER TABLE usage_record DROP INDEX idx_task_created;
  END IF;

  -- idx_ur_service_type
  IF EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND INDEX_NAME = 'idx_ur_service_type'
  ) THEN
    ALTER TABLE usage_record DROP INDEX idx_ur_service_type;
  END IF;

  -- is_estimated
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'is_estimated'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN is_estimated;
  END IF;

  -- pricing_second_snapshot
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_second_snapshot'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN pricing_second_snapshot;
  END IF;

  -- pricing_call_snapshot
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_call_snapshot'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN pricing_call_snapshot;
  END IF;

  -- pricing_output_snapshot
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_output_snapshot'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN pricing_output_snapshot;
  END IF;

  -- pricing_input_snapshot
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_input_snapshot'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN pricing_input_snapshot;
  END IF;

  -- duration_seconds
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'duration_seconds'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN duration_seconds;
  END IF;

  -- call_count
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'call_count'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN call_count;
  END IF;

  -- unit
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'unit'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN unit;
  END IF;

  -- task_id
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'task_id'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN task_id;
  END IF;

  -- service_type: 仅当 COMMENT 包含 [ai-service-manager:v1] tag 时才删除
  -- 这样能精确区分本次 migration 新增的列与 dev 环境已有的 billing schema 列
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'service_type'
      AND COLUMN_COMMENT LIKE '%ai-service-manager:v1%'
  ) THEN
    ALTER TABLE usage_record DROP COLUMN service_type;
  END IF;
END//
DELIMITER ;
CALL _rb_usage_record();
DROP PROCEDURE IF EXISTS _rb_usage_record;

-- Step 4: 恢复 ai_service_route → llm_model_provider

-- 4a: 删除读兼容 VIEW（仅当它是 VIEW 时才删除）
DROP PROCEDURE IF EXISTS _rb_drop_lmp_view;
DELIMITER //
CREATE PROCEDURE _rb_drop_lmp_view()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND TABLE_TYPE = 'VIEW'
  ) THEN
    DROP VIEW llm_model_provider;
  END IF;
END//
DELIMITER ;
CALL _rb_drop_lmp_view();
DROP PROCEDURE IF EXISTS _rb_drop_lmp_view;

-- 4b: 删除新增列（条件检查，幂等）
DROP PROCEDURE IF EXISTS _rb_ai_service_route;
DELIMITER //
CREATE PROCEDURE _rb_ai_service_route()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'price_per_second'
  ) THEN
    ALTER TABLE ai_service_route DROP COLUMN price_per_second;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'price_per_call'
  ) THEN
    ALTER TABLE ai_service_route DROP COLUMN price_per_call;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'pricing_unit'
  ) THEN
    ALTER TABLE ai_service_route DROP COLUMN pricing_unit;
  END IF;
END//
DELIMITER ;
CALL _rb_ai_service_route();
DROP PROCEDURE IF EXISTS _rb_ai_service_route;

-- 4c: 改名回原表名（仅当 ai_service_route 存在时）
DROP PROCEDURE IF EXISTS _rb_rename_route;
DELIMITER //
CREATE PROCEDURE _rb_rename_route()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND TABLE_TYPE = 'BASE TABLE'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND TABLE_TYPE = 'BASE TABLE'
  ) THEN
    SET @rename_sql = 'RENAME TABLE ai_service_route TO llm_model_provider';
    PREPARE rename_stmt FROM @rename_sql;
    EXECUTE rename_stmt;
    DEALLOCATE PREPARE rename_stmt;
  END IF;
END//
DELIMITER ;
CALL _rb_rename_route();
DROP PROCEDURE IF EXISTS _rb_rename_route;

-- Step 5: 恢复 ai_service → llm_model

-- 5a: 删除读兼容 VIEW（仅当它是 VIEW 时才删除）
DROP PROCEDURE IF EXISTS _rb_drop_lm_view;
DELIMITER //
CREATE PROCEDURE _rb_drop_lm_view()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND TABLE_TYPE = 'VIEW'
  ) THEN
    DROP VIEW llm_model;
  END IF;
END//
DELIMITER ;
CALL _rb_drop_lm_view();
DROP PROCEDURE IF EXISTS _rb_drop_lm_view;

-- 5b: 删除新增索引 + 列（条件检查，幂等）
DROP PROCEDURE IF EXISTS _rb_ai_service;
DELIMITER //
CREATE PROCEDURE _rb_ai_service()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND INDEX_NAME = 'idx_as_deprecated'
  ) THEN
    ALTER TABLE ai_service DROP INDEX idx_as_deprecated;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND INDEX_NAME = 'idx_as_service_type'
  ) THEN
    ALTER TABLE ai_service DROP INDEX idx_as_service_type;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'deprecated_at'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN deprecated_at;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'tags'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN tags;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'quality_tier'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN quality_tier;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'latency_tier'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN latency_tier;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'capability_json'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN capability_json;
  END IF;

  -- service_type: 仅当 COMMENT 包含 [ai-service-manager:v1] tag 时才删除
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'service_type'
      AND COLUMN_COMMENT LIKE '%ai-service-manager:v1%'
  ) THEN
    ALTER TABLE ai_service DROP COLUMN service_type;
  END IF;
END//
DELIMITER ;
CALL _rb_ai_service();
DROP PROCEDURE IF EXISTS _rb_ai_service;

-- 5c: 改名回原表名（仅当 ai_service 存在时）
DROP PROCEDURE IF EXISTS _rb_rename_service;
DELIMITER //
CREATE PROCEDURE _rb_rename_service()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND TABLE_TYPE = 'BASE TABLE'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND TABLE_TYPE = 'BASE TABLE'
  ) THEN
    SET @rename_sql = 'RENAME TABLE ai_service TO llm_model';
    PREPARE rename_stmt FROM @rename_sql;
    EXECUTE rename_stmt;
    DEALLOCATE PREPARE rename_stmt;
  END IF;
END//
DELIMITER ;
CALL _rb_rename_service();
DROP PROCEDURE IF EXISTS _rb_rename_service;

-- Step 6: 恢复 llm_provider

-- 6a: 删除 migration 新增的 provider 行（条件检查，幂等）
DROP PROCEDURE IF EXISTS _rb_llm_provider_seed;
DELIMITER //
CREATE PROCEDURE _rb_llm_provider_seed()
BEGIN
  -- 仅当 provider_type 列存在时才按 provider_type 过滤删除
  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND COLUMN_NAME = 'provider_type'
  ) THEN
    DELETE FROM llm_provider WHERE provider_type IN ('ocr', 'asr', 'file_service');
  END IF;
  -- ali-dashscope 和 volc-ark 的 provider_type='llm'，也是本 migration 新增的
  -- 通过 name 字段精确删除（不依赖 provider_type 列）
  DELETE FROM llm_provider WHERE name IN ('ali-dashscope', 'volc-ark');
END//
DELIMITER ;
CALL _rb_llm_provider_seed();
DROP PROCEDURE IF EXISTS _rb_llm_provider_seed;

-- 6b + 6c: 删除新增索引和列（条件检查，幂等）
DROP PROCEDURE IF EXISTS _rb_llm_provider;
DELIMITER //
CREATE PROCEDURE _rb_llm_provider()
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND INDEX_NAME = 'idx_provider_type'
  ) THEN
    ALTER TABLE llm_provider DROP INDEX idx_provider_type;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND COLUMN_NAME = 'supports_streaming'
  ) THEN
    ALTER TABLE llm_provider DROP COLUMN supports_streaming;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND COLUMN_NAME = 'provider_type'
  ) THEN
    ALTER TABLE llm_provider DROP COLUMN provider_type;
  END IF;
END//
DELIMITER ;
CALL _rb_llm_provider();
DROP PROCEDURE IF EXISTS _rb_llm_provider;

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

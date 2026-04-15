-- ============================================================
-- Migration: AI Service Manager Schema
-- File: 20260416_000001_ai_service_manager.sql
-- Spec: docs/superpowers/specs/2026-04-15-ai-service-manager-design.md §2
-- MySQL 版本要求: 8.0.13+（JSON DEFAULT 表达式语法）
-- 部署约束: 需短暂停服 2-3 分钟（DDL 非事务性，见 spec §2.5）
-- 幂等性: 所有 DDL 通过 PROCEDURE 条件检查，可重复执行
-- Rollback: migrations/20260416_000001_ai_service_manager_rollback.sql
-- ============================================================

-- ============================================================
-- Group A: 扩展 llm_provider 表
-- ============================================================

DROP PROCEDURE IF EXISTS _mig_group_a;
DELIMITER //
CREATE PROCEDURE _mig_group_a()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND COLUMN_NAME = 'provider_type'
  ) THEN
    ALTER TABLE llm_provider
      ADD COLUMN provider_type VARCHAR(20) NOT NULL DEFAULT 'llm'
        COMMENT 'llm | ocr | asr | file_service';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND COLUMN_NAME = 'supports_streaming'
  ) THEN
    ALTER TABLE llm_provider
      ADD COLUMN supports_streaming TINYINT(1) DEFAULT 1;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_provider' AND INDEX_NAME = 'idx_provider_type'
  ) THEN
    ALTER TABLE llm_provider ADD INDEX idx_provider_type (provider_type);
  END IF;
END//
DELIMITER ;
CALL _mig_group_a();
DROP PROCEDURE IF EXISTS _mig_group_a;


-- ============================================================
-- Group B: 扩展 llm_model + llm_model_provider 列
-- （在 RENAME 前加列；若表已被 RENAME 为 ai_service/ai_service_route，则跳过）
-- ============================================================

DROP PROCEDURE IF EXISTS _mig_group_b;
DELIMITER //
CREATE PROCEDURE _mig_group_b()
BEGIN
  -- 仅当 llm_model 还是基表（未被 RENAME）时才操作
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND TABLE_TYPE = 'BASE TABLE'
  ) THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'service_type'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN service_type VARCHAR(20) NOT NULL DEFAULT 'llm'
          COMMENT 'llm | ocr | asr（区分能力大类）';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'capability_json'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN capability_json JSON NOT NULL DEFAULT (JSON_OBJECT())
          COMMENT '按 service_type 存不同 schema 的能力字段';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'latency_tier'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN latency_tier VARCHAR(20) DEFAULT 'standard'
          COMMENT 'fast | standard | slow';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'quality_tier'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN quality_tier VARCHAR(20) DEFAULT 'standard'
          COMMENT 'basic | standard | pro | flagship';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'tags'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN tags JSON DEFAULT (JSON_ARRAY())
          COMMENT '自由标签，如 ["chinese-optimized", "cheap"]';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND COLUMN_NAME = 'deprecated_at'
    ) THEN
      ALTER TABLE llm_model
        ADD COLUMN deprecated_at DATETIME DEFAULT NULL
          COMMENT '软删除时间；非 NULL 表示已下架';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND INDEX_NAME = 'idx_service_type'
    ) THEN
      ALTER TABLE llm_model ADD INDEX idx_service_type (service_type);
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND INDEX_NAME = 'idx_deprecated'
    ) THEN
      ALTER TABLE llm_model ADD INDEX idx_deprecated (deprecated_at);
    END IF;
  END IF;

  -- llm_model_provider 同理
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND TABLE_TYPE = 'BASE TABLE'
  ) THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND COLUMN_NAME = 'pricing_unit'
    ) THEN
      ALTER TABLE llm_model_provider
        ADD COLUMN pricing_unit VARCHAR(20) NOT NULL DEFAULT 'per_1m_tokens'
          COMMENT 'per_1m_tokens | per_call | per_second';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND COLUMN_NAME = 'price_per_call'
    ) THEN
      ALTER TABLE llm_model_provider
        ADD COLUMN price_per_call DECIMAL(10,6) DEFAULT NULL
          COMMENT 'OCR 类：元/次（null = 不适用）';
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND COLUMN_NAME = 'price_per_second'
    ) THEN
      ALTER TABLE llm_model_provider
        ADD COLUMN price_per_second DECIMAL(10,6) DEFAULT NULL
          COMMENT 'ASR 类：元/秒（null = 不适用）';
    END IF;
  END IF;
END//
DELIMITER ;
CALL _mig_group_b();
DROP PROCEDURE IF EXISTS _mig_group_b;


-- ============================================================
-- Group C: 原子 RENAME + 创建读兼容 VIEW
-- （停服期间执行，无业务流量）
-- ============================================================

DROP PROCEDURE IF EXISTS _mig_group_c;
DELIMITER //
CREATE PROCEDURE _mig_group_c()
BEGIN
  -- 仅当 llm_model 和 llm_model_provider 还是基表时执行 RENAME
  IF EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model' AND TABLE_TYPE = 'BASE TABLE'
  ) AND EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'llm_model_provider' AND TABLE_TYPE = 'BASE TABLE'
  ) THEN
    -- 原子改名（单语句，MySQL 保证原子性）
    SET @rename_sql = 'RENAME TABLE llm_model TO ai_service, llm_model_provider TO ai_service_route';
    PREPARE rename_stmt FROM @rename_sql;
    EXECUTE rename_stmt;
    DEALLOCATE PREPARE rename_stmt;
  END IF;
END//
DELIMITER ;
CALL _mig_group_c();
DROP PROCEDURE IF EXISTS _mig_group_c;

-- C2: 为旧代码路径创建只读兼容 VIEW（llm_model）
-- ⚠ VIEW 仅只读使用。所有 INSERT/UPDATE/DELETE 走新 GORM model 直指 ai_service。
CREATE OR REPLACE VIEW llm_model AS
  SELECT id, model_key, display_name, is_thinking, base_model_id,
         supports_thinking, icon, sort_order, is_active,
         created_at, updated_at
  FROM ai_service
  WHERE service_type = 'llm' AND deprecated_at IS NULL;

-- C3: 为旧代码路径创建只读兼容 VIEW（llm_model_provider）
CREATE OR REPLACE VIEW llm_model_provider AS
  SELECT r.id, r.model_id, r.provider_id, r.provider_model_id, r.priority,
         r.input_price_per_mtok, r.output_price_per_mtok, r.is_active,
         r.created_at, r.updated_at
  FROM ai_service_route r
  JOIN ai_service s ON s.id = r.model_id
  WHERE s.service_type = 'llm' AND s.deprecated_at IS NULL;

-- 同样处理 ai_service 新列（如果 dev 环境未加）
DROP PROCEDURE IF EXISTS _mig_ai_service_cols;
DELIMITER //
CREATE PROCEDURE _mig_ai_service_cols()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'service_type'
  ) THEN
    ALTER TABLE ai_service
      ADD COLUMN service_type VARCHAR(20) NOT NULL DEFAULT 'llm'
        COMMENT 'llm | ocr | asr（区分能力大类）';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'capability_json'
  ) THEN
    ALTER TABLE ai_service
      ADD COLUMN capability_json JSON NOT NULL DEFAULT (JSON_OBJECT())
        COMMENT '按 service_type 存不同 schema 的能力字段';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'latency_tier'
  ) THEN
    ALTER TABLE ai_service ADD COLUMN latency_tier VARCHAR(20) DEFAULT 'standard' COMMENT 'fast | standard | slow';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'quality_tier'
  ) THEN
    ALTER TABLE ai_service ADD COLUMN quality_tier VARCHAR(20) DEFAULT 'standard' COMMENT 'basic | standard | pro | flagship';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'tags'
  ) THEN
    ALTER TABLE ai_service ADD COLUMN tags JSON DEFAULT (JSON_ARRAY()) COMMENT '自由标签，如 ["chinese-optimized", "cheap"]';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND COLUMN_NAME = 'deprecated_at'
  ) THEN
    ALTER TABLE ai_service ADD COLUMN deprecated_at DATETIME DEFAULT NULL COMMENT '软删除时间；非 NULL 表示已下架';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND INDEX_NAME = 'idx_service_type'
  ) THEN
    ALTER TABLE ai_service ADD INDEX idx_service_type (service_type);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service' AND INDEX_NAME = 'idx_deprecated'
  ) THEN
    ALTER TABLE ai_service ADD INDEX idx_deprecated (deprecated_at);
  END IF;

  -- ai_service_route 新列
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'pricing_unit'
  ) THEN
    ALTER TABLE ai_service_route
      ADD COLUMN pricing_unit VARCHAR(20) NOT NULL DEFAULT 'per_1m_tokens'
        COMMENT 'per_1m_tokens | per_call | per_second';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'price_per_call'
  ) THEN
    ALTER TABLE ai_service_route
      ADD COLUMN price_per_call DECIMAL(10,6) DEFAULT NULL
        COMMENT 'OCR 类：元/次（null = 不适用）';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_service_route' AND COLUMN_NAME = 'price_per_second'
  ) THEN
    ALTER TABLE ai_service_route
      ADD COLUMN price_per_second DECIMAL(10,6) DEFAULT NULL
        COMMENT 'ASR 类：元/秒（null = 不适用）';
  END IF;
END//
DELIMITER ;
CALL _mig_ai_service_cols();
DROP PROCEDURE IF EXISTS _mig_ai_service_cols;


-- ============================================================
-- Group D: 新表 + usage_record 扩展
-- ============================================================

-- D1: task_profile（任务-服务绑定的核心配置表）
CREATE TABLE IF NOT EXISTS task_profile (
    id                      BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id                 VARCHAR(80) NOT NULL UNIQUE
                             COMMENT '如 sop.text / salesrag.embed',
    display_name            VARCHAR(100) NOT NULL,
    description             TEXT,
    service_type            VARCHAR(20) NOT NULL
                             COMMENT 'llm | ocr | asr（限定允许绑定的服务类型）',
    requirements            JSON NOT NULL DEFAULT (JSON_OBJECT())
                             COMMENT '能力需求，如 {"input_modalities":["text","image"],"min_context":8192}',
    default_service_id      BIGINT UNSIGNED NULL,
    user_selectable         TINYINT(1) DEFAULT 0
                             COMMENT 'C 端 ModelSelector 是否暴露此 profile；默认 0',
    extra_metadata          JSON DEFAULT (JSON_OBJECT())
                             COMMENT '逃生舱字段',
    created_at              DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at              DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_service_type (service_type),
    CONSTRAINT fk_default_service
      FOREIGN KEY (default_service_id) REFERENCES ai_service(id)
      ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- D2: task_profile_service（绑定 fallback + allowed 关系）
CREATE TABLE IF NOT EXISTS task_profile_service (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_profile_id   BIGINT UNSIGNED NOT NULL,
    service_id        BIGINT UNSIGNED NOT NULL,
    role              VARCHAR(20) NOT NULL
                       COMMENT 'fallback | allowed',
    priority          INT DEFAULT 0
                       COMMENT 'fallback 优先级（0 最高）；allowed 下用于排序展示',
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_profile_service_role (task_profile_id, service_id, role),
    INDEX idx_profile_role (task_profile_id, role),
    CONSTRAINT fk_tps_profile FOREIGN KEY (task_profile_id) REFERENCES task_profile(id) ON DELETE CASCADE,
    CONSTRAINT fk_tps_service FOREIGN KEY (service_id) REFERENCES ai_service(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- D3: ai_service_audit_log（管理操作审计）
CREATE TABLE IF NOT EXISTS ai_service_audit_log (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    actor_id      BIGINT UNSIGNED NOT NULL COMMENT '操作人 admin_user.id',
    actor_name    VARCHAR(100) NOT NULL,
    action        VARCHAR(50) NOT NULL
                   COMMENT 'service.create | service.update | service.deprecate | task.bind | pricing.update | capability.override',
    target_type   VARCHAR(20) NOT NULL COMMENT 'service | task_profile',
    target_id     BIGINT UNSIGNED NOT NULL,
    diff_json     JSON COMMENT '变更前/后的 diff',
    reason        TEXT COMMENT '可选原因（capability override 时必填）',
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_actor_created (actor_id, created_at),
    INDEX idx_target (target_type, target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- D4: usage_record 扩展（支持 OCR/ASR 计费维度 + 任务溯源）
-- 使用 PROCEDURE 做条件 DDL（MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS）
-- 注：dev 环境 usage_record 已有 service_type 列（来自老 billing schema），跳过已存在的列
DROP PROCEDURE IF EXISTS _mig_usage_record_cols;
DELIMITER //
CREATE PROCEDURE _mig_usage_record_cols()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'task_id'
  ) THEN
    ALTER TABLE usage_record
      ADD COLUMN task_id VARCHAR(80) DEFAULT NULL
        COMMENT 'Task Profile id；null = 历史数据';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'unit'
  ) THEN
    ALTER TABLE usage_record
      ADD COLUMN unit VARCHAR(20) DEFAULT NULL
        COMMENT 'per_1m_tokens | per_call | per_second';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'call_count'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN call_count INT DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'duration_seconds'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN duration_seconds DECIMAL(10,3) DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_input_snapshot'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN pricing_input_snapshot DECIMAL(10,6) DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_output_snapshot'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN pricing_output_snapshot DECIMAL(10,6) DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_call_snapshot'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN pricing_call_snapshot DECIMAL(10,6) DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'pricing_second_snapshot'
  ) THEN
    ALTER TABLE usage_record ADD COLUMN pricing_second_snapshot DECIMAL(10,6) DEFAULT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'is_estimated'
  ) THEN
    ALTER TABLE usage_record
      ADD COLUMN is_estimated TINYINT(1) DEFAULT 0
        COMMENT '流式中断估算补记时为 1';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND INDEX_NAME = 'idx_task_created'
  ) THEN
    ALTER TABLE usage_record ADD INDEX idx_task_created (task_id, created_at);
  END IF;

  -- idx_ur_service_type: service_type 列可能已存在且有其他索引名，条件检查索引名
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND INDEX_NAME = 'idx_ur_service_type'
  ) THEN
    -- 仅当 service_type 列存在时才加索引
    IF EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_record' AND COLUMN_NAME = 'service_type'
    ) THEN
      ALTER TABLE usage_record ADD INDEX idx_ur_service_type (service_type);
    END IF;
  END IF;
END//
DELIMITER ;
CALL _mig_usage_record_cols();
DROP PROCEDURE IF EXISTS _mig_usage_record_cols;


-- ============================================================
-- Group E: Seed 数据
-- ============================================================

-- E1: 新增 Provider 行（ali/volc/baidu/bailian/funasr）
-- api_key 留空字符串；启动时由 aiservice.SyncProviderCredentials 从 config.ai_providers 读取填充
-- 使用 INSERT IGNORE 避免重复插入（name 列有 UNIQUE 约束）
INSERT IGNORE INTO llm_provider (name, display_name, base_url, api_key, provider_type, supports_streaming, is_active)
VALUES
  ('ali-dashscope',  '阿里云百炼',         'https://dashscope.aliyuncs.com/compatible-mode/v1', '', 'llm',          1, 1),
  ('volc-ark',       '火山方舟',            'https://ark.cn-beijing.volces.com/api/v3',           '', 'llm',          1, 1),
  ('baidu-ocr',      '百度 OCR',            'https://aip.baidubce.com/rest/2.0/ocr/v1',           '', 'ocr',          0, 1),
  ('bailian-file',   '阿里百炼文件服务',    'https://bailian.aliyuncs.com',                       '', 'file_service',  0, 1),
  ('funasr-local',   '本地 FunASR',         '',                                                   '', 'asr',          0, 1);
-- 注：funasr-local 的 base_url 也由启动时 SyncProviderCredentials 同步填充

-- E2: 新增 ai_service 行（OCR + ASR 服务）
-- 使用 INSERT IGNORE 避免重复插入（model_key 有 UNIQUE 约束）
INSERT IGNORE INTO ai_service (model_key, display_name, service_type, capability_json, is_active)
VALUES
  ('baidu-ocr-accurate', '百度 OCR 高精度含位置版', 'ocr',
    '{"image_formats":["jpg","png","bmp"],"max_resolution":4096,"max_file_size_mb":10,"capabilities":["ocr"]}', 1),
  ('funasr-paraformer',  'FunASR Paraformer',        'asr',
    '{"audio_formats":["wav","mp3","m4a"],"max_duration_sec":3600,"languages":["zh","en"],"realtime":false,"capabilities":["asr"]}', 1);

-- E3: 插入 14 条 Task Profile（按 spec §5.1 详表）
-- 使用 INSERT IGNORE 避免重复插入（task_id 有 UNIQUE 约束）
-- user_selectable=1 的两个 profile：chatbot.stream、salesrag.chat（见 spec §5.3）
INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, user_selectable) VALUES
  ('sop.text',
   'SOP 文本节点',
   'SOP 工作流纯文本节点，需要 tool_use 支持函数调用 + streaming 流式输出',
   'llm',
   '{"input_modalities":["text"],"min_context":8192,"features":["tool_use","streaming"]}',
   0),
  ('sop.vision',
   'SOP 视觉节点',
   'SOP 工作流视觉节点，需要 image 输入模态 + vision 能力 + streaming',
   'llm',
   '{"input_modalities":["text","image"],"min_context":8192,"features":["streaming","vision"]}',
   0),
  ('chatbot.stream',
   '对话助手（流式）',
   'ChatBot 流式问答，C 端用户可在 ModelSelector 选择模型',
   'llm',
   '{"input_modalities":["text"],"min_context":8192,"features":["streaming"]}',
   1),
  ('salesrag.intent',
   'SalesRAG 意图识别',
   '销售知识库问答意图分类，需要 json_mode 输出结构化结果',
   'llm',
   '{"input_modalities":["text"],"min_context":4096,"features":["json_mode"]}',
   0),
  ('salesrag.chat',
   'SalesRAG 问答（流式）',
   '销售知识库流式问答，需要大上下文窗口，C 端用户可选模型',
   'llm',
   '{"input_modalities":["text"],"min_context":16384,"features":["streaming"]}',
   1),
  ('salesrag.rerank',
   'SalesRAG 重排序',
   '销售知识库检索结果重排序',
   'llm',
   '{"capability":"rerank"}',
   0),
  ('salesrag.embed',
   'SalesRAG 向量嵌入',
   '销售知识库文档向量化，embedding 维度必须为 1024',
   'llm',
   '{"capability":"embedding","dimension":1024}',
   0),
  ('salesrag.tagging',
   'SalesRAG 标签提取',
   '销售知识库文档自动标签，需要 json_mode',
   'llm',
   '{"input_modalities":["text"],"features":["json_mode"]}',
   0),
  ('salesrag.profile',
   'SalesRAG 销售画像',
   '客户销售画像生成，支持文本和图片输入，需要 vision + streaming',
   'llm',
   '{"input_modalities":["text","image"],"features":["streaming","vision"]}',
   0),
  ('salesrag.chatstyle',
   'SalesRAG 话术风格',
   '销售话术风格生成，支持文本和图片输入，需要 vision + streaming',
   'llm',
   '{"input_modalities":["text","image"],"features":["streaming","vision"]}',
   0),
  ('monitor.briefing',
   '舆情简报',
   '舆情监控简报生成，需要大上下文窗口处理长文本',
   'llm',
   '{"input_modalities":["text"],"min_context":16384}',
   0),
  ('monitor.analyze',
   '舆情分析',
   '舆情内容结构化分析，需要 json_mode 输出',
   'llm',
   '{"input_modalities":["text"],"features":["json_mode"]}',
   0),
  ('monitor.transcribe',
   '音频转写',
   '视频/音频内容语音转文字，支持 wav/mp3/m4a',
   'asr',
   '{"audio_formats":["wav","mp3","m4a"],"max_duration_sec":3600}',
   0),
  ('ocr.baidu',
   '百度 OCR 识别',
   '图片文字识别，高精度含位置版，支持 jpg/png/bmp',
   'ocr',
   '{"image_formats":["jpg","png","bmp"],"max_resolution":4096}',
   0);

-- E4: task_profile.default_service_id 绑定
-- AMBIGUITY: spec §5.1 中"默认 service"列引用的模型名（deepseek-v3, qwen-plus, qwen-vl 等）
-- 在现有 ai_service 表中并不一定存在对应 model_key（现有 seed 是占位模型名）。
-- 保守决定：default_service_id 留 NULL（INSERT IGNORE 时已是 NULL 值）。
-- 由 Task 8 的 SyncProviderCredentials 或运营通过管理端完成绑定。
-- PENDING: 待 spec §5.1 中的模型 model_key 与实际 ai_service 表数据对齐后补设。

-- E5: task_profile_service（fallback + allowed 绑定）
-- 同 E4，fallback 绑定依赖运行时模型数据存在；本 migration 暂不插入 task_profile_service 行。
-- 由运营通过管理端 Task Profile 编辑页（PUT /v1/admin/ai/tasks/:id）完成绑定。

-- ============================================================
-- Migration 完成
-- 验证查询：
--   SHOW TABLES LIKE 'ai_service';                               -- 应存在（BASE TABLE）
--   SHOW TABLES LIKE 'ai_service_route';                         -- 应存在（BASE TABLE）
--   SHOW TABLES LIKE 'task_profile';                             -- 应存在（BASE TABLE）
--   SHOW TABLES LIKE 'task_profile_service';                     -- 应存在（BASE TABLE）
--   SHOW TABLES LIKE 'ai_service_audit_log';                     -- 应存在（BASE TABLE）
--   SELECT COUNT(*) FROM task_profile;                           -- 应 = 14
--   SELECT COUNT(*) FROM llm_provider WHERE provider_type!='llm'; -- 应 >= 3（ocr/file_service/asr）
--   SELECT * FROM llm_model LIMIT 1;                             -- VIEW 应可读
--   SELECT * FROM llm_model_provider LIMIT 1;                    -- VIEW 应可读（若有路由数据）
--   EXPLAIN SELECT * FROM usage_record
--     WHERE task_id='sop.text' AND created_at > NOW() - INTERVAL 7 DAY;
--     -- key 应命中 idx_task_created
-- ============================================================

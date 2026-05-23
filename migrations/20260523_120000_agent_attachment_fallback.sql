-- 20260523_120000_agent_attachment_fallback.sql
--
-- Creates the agent_attachment table and adds the attachment.vision_describe
-- ai_service_route for the V1.5 multimodal fallback feature (task 1.2).
--
-- Background: agent_attachment did NOT previously exist as a DB table.
-- The prior UploadService only stored files to COS and returned a URL in-memory.
-- This migration creates the table from scratch (not ALTER TABLE as the spec
-- draft assumed). The spec's ALTER TABLE was written under the incorrect premise
-- that the table already existed from the context.md §5 schema block — that
-- block described the intended schema, not an existing one.
--
-- Idempotent via IF NOT EXISTS / INSERT IGNORE.

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. Create agent_attachment table
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS `agent_attachment` (
  `id`                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`               INT UNSIGNED    NOT NULL,
  `url`                   TEXT            NOT NULL COMMENT 'COS public URL',
  `filename`              VARCHAR(255)    NOT NULL DEFAULT '',
  `mime_type`             VARCHAR(128)    NOT NULL DEFAULT '',
  `size`                  BIGINT          NOT NULL DEFAULT 0 COMMENT 'bytes',

  -- Modality detection (task 1.2)
  `modality`              VARCHAR(32)     NOT NULL DEFAULT 'unknown'
                          COMMENT 'image | pdf | audio | unknown',

  -- Image dimensions (populated during fallback generation for image modality)
  `width`                 INT             NULL,
  `height`                INT             NULL,

  -- Async fallback fields (task 1.2)
  `ocr_text`              TEXT            NULL COMMENT 'Baidu OCR extraction (image only)',
  `vision_description`    TEXT            NULL COMMENT 'VLM visual description (image only)',
  `text_fallback`         TEXT            NULL COMMENT 'Composed fallback text for non-multimodal models',
  `fallback_ready`        TINYINT(1)      NOT NULL DEFAULT 0
                          COMMENT '0=pending, 1=completed (success or terminal failure)',
  `fallback_error`        TEXT            NULL COMMENT 'Non-NULL means generation failed; text_fallback still populated with degraded message',
  `fallback_started_at`   DATETIME        NULL,
  `fallback_completed_at` DATETIME        NULL,
  `retry_count`           TINYINT         NOT NULL DEFAULT 0,

  `created_at`            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

  PRIMARY KEY (`id`),
  INDEX `idx_aa_user` (`user_id`),
  INDEX `idx_aa_ready` (`fallback_ready`, `user_id`),
  INDEX `idx_aa_modality` (`modality`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Agent attachment upload records with async multimodal fallback fields';

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. Register attachment.vision_describe ai_service_route
--
-- The qwen3-vl-flash ai_service row (model_key='qwen3-vl-flash') was
-- inserted by an earlier manual SQL session (see migration 20260419_230000).
-- We only need to ensure a task_profile row links to that service so that
-- aiservice.Chat(ctx, "attachment.vision_describe", req) resolves correctly.
--
-- task_profile table contract (from registry package):
--   task_id VARCHAR(64) PK, default_service_id INT FK ai_service.id
--
-- Insert IGNORE is safe for re-runs. The JOIN against ai_service ensures we
-- reference the correct id without hard-coding it.
-- ─────────────────────────────────────────────────────────────────────────────

-- Guard: qwen3-vl-flash must exist in ai_service, otherwise the INSERT SELECT
-- below inserts 0 rows and the profile silently has no route.
-- Division by zero if the service is missing → migration aborts clearly.
SELECT 1 / (
  SELECT COUNT(*) FROM ai_service WHERE model_key = 'qwen3-vl-flash'
) AS guard_qwen3_vl_flash_must_exist;

-- Insert the task_profile row for attachment.vision_describe, routing to
-- qwen3-vl-flash (the same VLM already used by salesrag.profile / salesrag.chatstyle).
--
-- task_profile required columns (see model/task_profile.go):
--   task_id VARCHAR(80) NOT NULL UNIQUE
--   display_name VARCHAR(100) NOT NULL
--   service_type VARCHAR(20) NOT NULL  (llm | ocr | asr)
--   default_service_id → ai_service.id
INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, default_service_id, user_selectable)
SELECT
  'attachment.vision_describe',
  '附件视觉描述',
  '上传图片时异步用 VLM 生成图片文字描述，供单模态模型 fallback 使用',
  'llm',
  s.id,
  0
FROM ai_service s
WHERE s.model_key = 'qwen3-vl-flash';

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. Register attachment.pdf_extract ai_service_route (P1 #4 fix, task 1.2 review)
--
-- PDF text extraction uses qwen-long, which is a dedicated long-context model.
-- This corrects a bug where generatePDF was misusing profile.AgentRun +
-- ModelOverride which mixes PDF extraction costs with the ReAct agent budget.
--
-- qwen-long is registered in ai_service with model_key='qwen-long'
-- (inserted by migration 20260419_230000 or equivalent).
-- ─────────────────────────────────────────────────────────────────────────────

-- Guard: qwen-long must exist in ai_service.
SELECT 1 / (
  SELECT COUNT(*) FROM ai_service WHERE model_key = 'qwen-long'
) AS guard_qwen_long_must_exist;

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, default_service_id, user_selectable)
SELECT
  'attachment.pdf_extract',
  '附件PDF提取',
  '上传PDF时异步用 qwen-long 提取全文文字，供单模态模型 fallback 使用',
  'llm',
  s.id,
  0
FROM ai_service s
WHERE s.model_key = 'qwen-long';

-- Final additive schema/config reconcile for the full Dev -> Prod rollout.
-- This file is intentionally safe to run more than once.
-- It never copies Dev data and never changes an existing subscription column.

SET @reconcile_db := DATABASE();

DROP PROCEDURE IF EXISTS `_prod_schema_reconcile_assert`;
DELIMITER //
CREATE PROCEDURE `_prod_schema_reconcile_assert`()
BEGIN
  IF (SELECT COUNT(*) FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user') <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: required user table missing';
  END IF;
  IF (SELECT COUNT(*) FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription') <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: required subscription table missing';
  END IF;
  IF (SELECT COUNT(*) FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment') <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: required agent_attachment table missing';
  END IF;
  IF (SELECT COUNT(*) FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_run') <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: required agent_run table missing';
  END IF;
  IF (SELECT COUNT(*) FROM `llm_provider` WHERE `name` = 'ali-dashscope') <> 1 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: ali-dashscope provider must exist exactly once';
  END IF;
  IF (SELECT COUNT(*) FROM `skill_template`) <> 0 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: unexpected skill_template rows; refusing automatic deletion';
  END IF;
  IF (SELECT COUNT(*) FROM `skill`
      WHERE `visibility` = 'official'
        AND `parent_user_id` <> 0
        AND `source_type` = 'imported_from_template') <> 0 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: unexpected tenant official-template imports require manual review';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `announcement_read` child
    LEFT JOIN `announcement` parent ON parent.`id` = child.`announcement_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan announcement_read.announcement_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `announcement_read` child
    LEFT JOIN `user` parent ON parent.`id` = child.`user_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan announcement_read.user_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `survey_question` child
    LEFT JOIN `announcement` parent ON parent.`id` = child.`announcement_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan survey_question.announcement_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `survey_response` child
    LEFT JOIN `announcement` parent ON parent.`id` = child.`announcement_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan survey_response.announcement_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `survey_response` child
    LEFT JOIN `user` parent ON parent.`id` = child.`user_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan survey_response.user_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `survey_answer` child
    LEFT JOIN `survey_response` parent ON parent.`id` = child.`response_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan survey_answer.response_id';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM `survey_answer` child
    LEFT JOIN `survey_question` parent ON parent.`id` = child.`question_id`
    WHERE parent.`id` IS NULL
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'prod reconcile: orphan survey_answer.question_id';
  END IF;
END//
DELIMITER ;
CALL `_prod_schema_reconcile_assert`();
DROP PROCEDURE IF EXISTS `_prod_schema_reconcile_assert`;

-- --------------------------------------------------------------------------
-- Document system and Feishu personal workspace final-state tables.
-- --------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS `document` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `parent_user_id` BIGINT UNSIGNED NULL,
  `source_object_key` VARCHAR(512) NOT NULL,
  `source_run_id` BIGINT UNSIGNED NULL,
  `source_mime` VARCHAR(128) NULL,
  `title` VARCHAR(255) NOT NULL,
  `content_md` MEDIUMTEXT NOT NULL,
  `parse_method` VARCHAR(32) NOT NULL DEFAULT 'direct',
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_doc_user_source` (`user_id`, `source_object_key`),
  KEY `idx_doc_user_updated` (`user_id`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `user_third_party_account` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `provider` VARCHAR(32) NOT NULL,
  `app_id` VARCHAR(64) NOT NULL,
  `app_secret_enc` BLOB NULL,
  `access_token_enc` BLOB NULL,
  `refresh_token_enc` BLOB NULL,
  `token_expires_at` DATETIME(3) NULL,
  `scopes` VARCHAR(512) NULL,
  `connected` TINYINT(1) NOT NULL DEFAULT 0,
  `connected_at` DATETIME(3) NULL,
  `connection_state` VARCHAR(32) NOT NULL DEFAULT 'none',
  `lark_cli_version` VARCHAR(32) NULL,
  `granted_scopes_json` JSON NULL,
  `capability_state_json` JSON NULL,
  `last_success_at` DATETIME(3) NULL,
  `last_error_code` VARCHAR(128) NULL,
  `generation` BIGINT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_user_provider` (`user_id`, `provider`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `feishu_cli_vault` (
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `ciphertext` LONGBLOB NOT NULL,
  `key_version` VARCHAR(32) NOT NULL,
  `checksum` VARCHAR(64) NOT NULL,
  `revision` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `feishu_auth_session` (
  `id` CHAR(36) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `operation_id` CHAR(36) NULL,
  `phase` VARCHAR(32) NOT NULL,
  `requested_scopes_json` JSON NOT NULL,
  `state` VARCHAR(32) NOT NULL,
  `lease_owner` VARCHAR(128) NULL,
  `lease_until` DATETIME(3) NULL,
  `expires_at` DATETIME(3) NOT NULL,
  `protocol_version` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `resume_credential_ciphertext` LONGBLOB NULL,
  `resume_key_version` VARCHAR(32) NULL,
  `resume_expires_at` DATETIME(3) NULL,
  `scope_hash` CHAR(64) NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  `completed_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  KEY `idx_feishu_auth_session_user_generation` (`user_id`, `generation`),
  KEY `idx_feishu_auth_session_operation` (`operation_id`),
  KEY `idx_feishu_auth_session_lease` (`state`, `lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `feishu_operation` (
  `id` CHAR(36) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `agent_run_id` BIGINT UNSIGNED NOT NULL,
  `tool_call_id` VARCHAR(128) NOT NULL,
  `idempotency_key` VARCHAR(191) NOT NULL,
  `command_path` VARCHAR(255) NOT NULL,
  `domain` VARCHAR(32) NOT NULL,
  `risk_level` VARCHAR(32) NOT NULL,
  `request_ciphertext` LONGBLOB NOT NULL,
  `key_version` VARCHAR(32) NOT NULL,
  `request_fingerprint` VARCHAR(64) NOT NULL,
  `state` VARCHAR(32) NOT NULL,
  `attempt_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `lease_owner` VARCHAR(128) NULL,
  `lease_until` DATETIME(3) NULL,
  `error_type` VARCHAR(64) NULL,
  `error_subtype` VARCHAR(128) NULL,
  `error_code` VARCHAR(128) NULL,
  `result_ciphertext` LONGBLOB NULL,
  `result_summary_json` JSON NULL,
  `created_at` DATETIME(3) NOT NULL,
  `started_at` DATETIME(3) NULL,
  `updated_at` DATETIME(3) NOT NULL,
  `finished_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_feishu_operation_user_key` (`user_id`, `idempotency_key`),
  KEY `idx_feishu_operation_user_generation` (`user_id`, `generation`),
  KEY `idx_feishu_operation_agent_tool` (`agent_run_id`, `tool_call_id`),
  KEY `idx_feishu_operation_lease` (`state`, `lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `feishu_operation_proof_consumption` (
  `source_operation_id` CHAR(36) NOT NULL,
  `consumer_operation_id` CHAR(36) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `agent_run_id` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`source_operation_id`),
  UNIQUE KEY `uniq_feishu_proof_consumer` (`consumer_operation_id`),
  KEY `idx_feishu_proof_audit` (`user_id`, `generation`, `agent_run_id`),
  CONSTRAINT `fk_feishu_proof_source_operation`
    FOREIGN KEY (`source_operation_id`) REFERENCES `feishu_operation` (`id`) ON DELETE RESTRICT,
  CONSTRAINT `fk_feishu_proof_consumer_operation`
    FOREIGN KEY (`consumer_operation_id`) REFERENCES `feishu_operation` (`id`) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS `feishu_operation_execution_gate` (
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `lease_owner` VARCHAR(128) NOT NULL DEFAULT '',
  `operation_id` CHAR(36) NOT NULL DEFAULT '',
  `lease_until` DATETIME(3) NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`user_id`),
  KEY `idx_feishu_execution_gate_lease` (`lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- --------------------------------------------------------------------------
-- Existing-table additive columns and indexes.
-- --------------------------------------------------------------------------

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'plan_type'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `subscription` ADD COLUMN `plan_type` VARCHAR(20) NOT NULL DEFAULT ''monthly'' COMMENT ''Subscription plan type: monthly or weekly'' AFTER `total_months_purchased`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'subscription' AND COLUMN_NAME = 'cycle_credits'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `subscription` ADD COLUMN `cycle_credits` INT NOT NULL DEFAULT 2000 COMMENT ''Credits granted per subscription cycle'' AFTER `plan_type`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_attachment' AND COLUMN_NAME = 'parsed_content'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_attachment` ADD COLUMN `parsed_content` LONGTEXT NULL COMMENT ''Canonical normalized UTF-8 content paged by file_read'' AFTER `fallback_error`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_attachment' AND COLUMN_NAME = 'parsed_content_sha256'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_attachment` ADD COLUMN `parsed_content_sha256` VARCHAR(71) NOT NULL DEFAULT '''' COMMENT ''sha256 continuation token for parsed_content'' AFTER `parsed_content`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_attachment' AND COLUMN_NAME = 'parsed_content_byte_size'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_attachment` ADD COLUMN `parsed_content_byte_size` BIGINT NOT NULL DEFAULT 0 COMMENT ''UTF-8 byte length of parsed_content'' AFTER `parsed_content_sha256`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_attachment' AND COLUMN_NAME = 'parsed_page_count'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_attachment` ADD COLUMN `parsed_page_count` INT NOT NULL DEFAULT 0 COMMENT ''Parser page count; zero when unknown'' AFTER `parsed_content_byte_size`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_attachment' AND COLUMN_NAME = 'parsed_at'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_attachment` ADD COLUMN `parsed_at` DATETIME(3) NULL COMMENT ''Time canonical parsed content was persisted'' AFTER `parsed_page_count`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Only new parsed fields are backfilled. Existing attachment metadata is untouched.
UPDATE `agent_attachment`
SET `parsed_content` = `text_fallback`,
    `parsed_content_sha256` = CONCAT('sha256:', SHA2(`text_fallback`, 256)),
    `parsed_content_byte_size` = OCTET_LENGTH(`text_fallback`),
    `parsed_at` = COALESCE(`fallback_completed_at`, `created_at`)
WHERE `fallback_ready` = 1
  AND `fallback_error` IS NULL
  AND `text_fallback` IS NOT NULL
  AND `parsed_content` IS NULL;

UPDATE `agent_attachment`
SET `parsed_content_sha256` = ''
WHERE `parsed_content_sha256` IS NULL;

UPDATE `agent_attachment`
SET `parsed_content_byte_size` = 0
WHERE `parsed_content_byte_size` IS NULL;

UPDATE `agent_attachment`
SET `parsed_page_count` = 0
WHERE `parsed_page_count` IS NULL;

ALTER TABLE `agent_attachment`
  MODIFY COLUMN `parsed_content_sha256` VARCHAR(71) NOT NULL DEFAULT '',
  MODIFY COLUMN `parsed_content_byte_size` BIGINT NOT NULL DEFAULT 0,
  MODIFY COLUMN `parsed_page_count` INT NOT NULL DEFAULT 0,
  MODIFY COLUMN `parsed_at` DATETIME(3) NULL;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_run'
    AND COLUMN_NAME = 'pending_external_action_json'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_run` ADD COLUMN `pending_external_action_json` JSON NULL AFTER `pending_question_at`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_column := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_run'
    AND COLUMN_NAME = 'pending_external_action_at'
);
SET @ddl := IF(
  @has_column = 0,
  'ALTER TABLE `agent_run` ADD COLUMN `pending_external_action_at` DATETIME(3) NULL AFTER `pending_external_action_json`',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_index := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = @reconcile_db AND TABLE_NAME = 'agent_run'
    AND INDEX_NAME = 'idx_ar_state_pending'
);
SET @ddl := IF(
  @has_index = 0,
  'ALTER TABLE `agent_run` ADD INDEX `idx_ar_state_pending` (`state_reason`, `pending_question_at`)',
  'SELECT 1'
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- --------------------------------------------------------------------------
-- Notification uniqueness and FK constraints.
-- --------------------------------------------------------------------------

DROP PROCEDURE IF EXISTS `_prod_schema_reconcile_notification`;
DELIMITER //
CREATE PROCEDURE `_prod_schema_reconcile_notification`()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read' AND INDEX_NAME = 'uk_annread'
  ) THEN
    ALTER TABLE `announcement_read` ADD UNIQUE KEY `uk_annread` (`announcement_id`, `user_id`);
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response' AND INDEX_NAME = 'uk_sr'
  ) THEN
    ALTER TABLE `survey_response` ADD UNIQUE KEY `uk_sr` (`announcement_id`, `user_id`);
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read'
      AND CONSTRAINT_NAME = 'fk_annread_announcement'
  ) THEN
    ALTER TABLE `announcement_read`
      ADD CONSTRAINT `fk_annread_announcement`
      FOREIGN KEY (`announcement_id`) REFERENCES `announcement` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'announcement_read'
      AND CONSTRAINT_NAME = 'fk_annread_user'
  ) THEN
    ALTER TABLE `announcement_read`
      ADD CONSTRAINT `fk_annread_user`
      FOREIGN KEY (`user_id`) REFERENCES `user` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_question'
      AND CONSTRAINT_NAME = 'fk_sq_announcement'
  ) THEN
    ALTER TABLE `survey_question`
      ADD CONSTRAINT `fk_sq_announcement`
      FOREIGN KEY (`announcement_id`) REFERENCES `announcement` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response'
      AND CONSTRAINT_NAME = 'fk_sr_announcement'
  ) THEN
    ALTER TABLE `survey_response`
      ADD CONSTRAINT `fk_sr_announcement`
      FOREIGN KEY (`announcement_id`) REFERENCES `announcement` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_response'
      AND CONSTRAINT_NAME = 'fk_sr_user'
  ) THEN
    ALTER TABLE `survey_response`
      ADD CONSTRAINT `fk_sr_user`
      FOREIGN KEY (`user_id`) REFERENCES `user` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_answer'
      AND CONSTRAINT_NAME = 'fk_sa_response'
  ) THEN
    ALTER TABLE `survey_answer`
      ADD CONSTRAINT `fk_sa_response`
      FOREIGN KEY (`response_id`) REFERENCES `survey_response` (`id`) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'survey_answer'
      AND CONSTRAINT_NAME = 'fk_sa_question'
  ) THEN
    ALTER TABLE `survey_answer`
      ADD CONSTRAINT `fk_sa_question`
      FOREIGN KEY (`question_id`) REFERENCES `survey_question` (`id`) ON DELETE CASCADE;
  END IF;
END//
DELIMITER ;
CALL `_prod_schema_reconcile_notification`();
DROP PROCEDURE IF EXISTS `_prod_schema_reconcile_notification`;

-- --------------------------------------------------------------------------
-- Qwen 3.5 Flash attachment vision routing (no provider credential changes).
-- --------------------------------------------------------------------------

INSERT INTO `ai_service` (
  `model_key`, `display_name`, `service_type`, `capability_json`,
  `latency_tier`, `quality_tier`, `is_thinking`, `supports_thinking`,
  `thinking_only`, `is_active`, `created_at`, `updated_at`
) VALUES (
  'qwen3.5-flash',
  'qwen3.5-flash',
  'llm',
  JSON_OBJECT(
    'input_modalities', JSON_ARRAY('text', 'image'),
    'output_modalities', JSON_ARRAY('text'),
    'accepts_image_inline', TRUE,
    'accepts_pdf_inline', FALSE,
    'accepts_audio_inline', FALSE,
    'max_inline_size_bytes', 20971520,
    'supports_vision_tool_calling', TRUE,
    'preferred_image_format', 'base64',
    'capabilities', JSON_ARRAY('chat')
  ),
  'fast', 'standard', 0, 0, 0, 1, NOW(3), NOW(3)
) ON DUPLICATE KEY UPDATE
  `display_name` = VALUES(`display_name`),
  `service_type` = VALUES(`service_type`),
  `capability_json` = VALUES(`capability_json`),
  `latency_tier` = VALUES(`latency_tier`),
  `quality_tier` = VALUES(`quality_tier`),
  `is_thinking` = VALUES(`is_thinking`),
  `supports_thinking` = VALUES(`supports_thinking`),
  `thinking_only` = VALUES(`thinking_only`),
  `is_active` = 1,
  `deprecated_at` = NULL,
  `updated_at` = NOW(3);

INSERT INTO `ai_service_route`
  (`model_id`, `provider_id`, `provider_model_id`, `priority`, `is_active`, `created_at`, `updated_at`)
SELECT service.`id`, provider.`id`, 'qwen3.5-flash', 100, 1, NOW(3), NOW(3)
FROM `ai_service` service
JOIN `llm_provider` provider ON provider.`name` = 'ali-dashscope'
WHERE service.`model_key` = 'qwen3.5-flash'
ON DUPLICATE KEY UPDATE
  `provider_model_id` = VALUES(`provider_model_id`),
  `priority` = 100,
  `is_active` = 1,
  `updated_at` = NOW(3);

INSERT INTO `task_profile` (
  `task_id`, `display_name`, `description`, `service_type`,
  `requirements`, `default_service_id`, `user_selectable`
)
SELECT
  'attachment.vision_describe',
  '附件视觉描述',
  '上传图片时异步用 Qwen 视觉模型生成图片文字描述，供单模态模型 fallback 使用',
  'llm',
  JSON_OBJECT('input_modalities', JSON_ARRAY('text', 'image')),
  service.`id`,
  0
FROM `ai_service` service
WHERE service.`model_key` = 'qwen3.5-flash'
ON DUPLICATE KEY UPDATE
  `description` = VALUES(`description`),
  `service_type` = VALUES(`service_type`),
  `requirements` = VALUES(`requirements`),
  `default_service_id` = VALUES(`default_service_id`),
  `user_selectable` = 0,
  `updated_at` = NOW();

DELETE old_binding
FROM `task_profile_service` old_binding
JOIN `ai_service` old_service
  ON old_service.`id` = old_binding.`service_id`
 AND old_service.`model_key` = 'qwen3-vl-flash'
JOIN `ai_service` new_service
  ON new_service.`model_key` = 'qwen3.5-flash'
JOIN `task_profile_service` new_binding
  ON new_binding.`task_profile_id` = old_binding.`task_profile_id`
 AND new_binding.`service_id` = new_service.`id`
 AND new_binding.`role` = old_binding.`role`;

UPDATE `task_profile_service` binding
JOIN `ai_service` old_service
  ON old_service.`id` = binding.`service_id`
 AND old_service.`model_key` = 'qwen3-vl-flash'
JOIN `ai_service` new_service
  ON new_service.`model_key` = 'qwen3.5-flash'
SET binding.`service_id` = new_service.`id`
WHERE binding.`service_id` = old_service.`id`;

UPDATE `ai_service_route` route
JOIN `ai_service` service ON service.`id` = route.`model_id`
JOIN `llm_provider` provider ON provider.`id` = route.`provider_id`
SET route.`is_active` = 0,
    route.`updated_at` = NOW(3)
WHERE service.`model_key` = 'qwen3-vl-flash'
  AND provider.`name` = 'ali-dashscope';

UPDATE `ai_service`
SET `is_active` = 0,
    `deprecated_at` = COALESCE(`deprecated_at`, NOW(3)),
    `updated_at` = NOW(3)
WHERE `model_key` = 'qwen3-vl-flash';

INSERT INTO `pricing_rule` (
  `service_type`, `provider`, `model`, `billing_mode`, `flat_unit`,
  `input_price_per_m_tok`, `output_price_per_m_tok`, `price_per_call`, `price_per_gb`,
  `sell_input_price_per_m_tok`, `sell_output_price_per_m_tok`,
  `sell_price_per_call`, `sell_price_per_gb`,
  `is_active`, `created_at`, `updated_at`
) VALUES (
  'llm_vision', 'ali-dashscope', 'qwen3.5-flash', 'flat', 'call',
  0.15, 1.50, 0, 0,
  0.15, 1.50, 0, 0,
  1, NOW(3), NOW(3)
) ON DUPLICATE KEY UPDATE
  `input_price_per_m_tok` = VALUES(`input_price_per_m_tok`),
  `output_price_per_m_tok` = VALUES(`output_price_per_m_tok`),
  `sell_input_price_per_m_tok` = VALUES(`sell_input_price_per_m_tok`),
  `sell_output_price_per_m_tok` = VALUES(`sell_output_price_per_m_tok`),
  `is_active` = 1,
  `updated_at` = NOW(3);

-- Exact project-owned placeholder seed only. No customer Skill matches this key.
DELETE FROM `skill`
WHERE `visibility` = 'official'
  AND `parent_user_id` = 0
  AND `owner_user_id` = 0
  AND `name` = '官方示例技能'
  AND `source_type` = 'custom';

SELECT 'prod_schema_reconcile_apply_complete' AS `status`;

-- Synthetic MySQL 8 baseline matching only the Prod structures touched or read
-- by the reconcile package. Contains no real customer data.

CREATE TABLE `user` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username` VARCHAR(100) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `user` (`id`, `username`) VALUES (101, 'synthetic-parent'), (102, 'synthetic-child');

CREATE TABLE `subscription` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `first_started_at` DATETIME NOT NULL,
  `current_started_at` DATETIME NOT NULL,
  `expires_at` DATETIME NOT NULL,
  `total_months_purchased` INT NOT NULL,
  `source` VARCHAR(20) NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id` BIGINT UNSIGNED NULL,
  `created_at` DATETIME NOT NULL,
  `updated_at` DATETIME NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_sub_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `subscription` (
  `id`, `user_id`, `first_started_at`, `current_started_at`, `expires_at`,
  `total_months_purchased`, `source`, `granter_user_id`, `created_at`, `updated_at`
) VALUES
  (1, 101, '2026-01-01 00:00:00', '2026-07-01 00:00:00', '2026-08-01 00:00:00',
   7, 'b2b_grant', NULL, '2026-01-01 00:00:00', '2026-07-01 00:00:00'),
  (2, 102, '2026-02-15 09:30:00', '2026-07-15 09:30:00', '2026-08-15 09:30:00',
   6, 'b2b_grant', 101, '2026-02-15 09:30:00', '2026-07-15 09:30:00');

CREATE TABLE `agent_attachment` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `url` TEXT NOT NULL,
  `filename` VARCHAR(255) NULL,
  `mime_type` VARCHAR(128) NULL,
  `size` BIGINT NULL DEFAULT 0,
  `modality` VARCHAR(32) NULL DEFAULT 'unknown',
  `width` BIGINT NULL,
  `height` BIGINT NULL,
  `ocr_text` TEXT NULL,
  `vision_description` TEXT NULL,
  `text_fallback` TEXT NULL,
  `fallback_ready` TINYINT(1) NULL DEFAULT 0,
  `fallback_error` TEXT NULL,
  `fallback_started_at` DATETIME(3) NULL,
  `fallback_completed_at` DATETIME(3) NULL,
  `retry_count` TINYINT UNSIGNED NULL DEFAULT 0,
  `created_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  KEY `idx_aa_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `agent_attachment` (
  `id`, `user_id`, `url`, `filename`, `mime_type`, `modality`,
  `text_fallback`, `fallback_ready`, `fallback_completed_at`, `created_at`
) VALUES
  (1, 101, 'https://invalid.example/a.txt', 'a.txt', 'text/plain', 'text',
   'synthetic parsed text', 1, '2026-07-20 10:00:00.000', '2026-07-20 09:59:00.000'),
  (2, 102, 'https://invalid.example/b.pdf', 'b.pdf', 'application/pdf', 'document',
   NULL, 0, NULL, '2026-07-20 10:01:00.000');

CREATE TABLE `agent_run` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `status` VARCHAR(32) NOT NULL,
  `state_reason` VARCHAR(64) NULL,
  `terminal_metadata` JSON NULL,
  `messages` JSON NOT NULL,
  `reservation_id` BIGINT UNSIGNED NULL,
  `started_at` DATETIME(3) NOT NULL,
  `ended_at` DATETIME(3) NULL,
  `cancellation_requested_at` DATETIME(3) NULL,
  `agent_definition_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `pending_question_json` JSON NULL,
  `pending_question_at` DATETIME(3) NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  `use_compact_v2` TINYINT(1) NOT NULL DEFAULT 0,
  `is_pinned` TINYINT(1) NOT NULL DEFAULT 0,
  `session_name` VARCHAR(255) NOT NULL DEFAULT '',
  `is_deleted` TINYINT(1) NOT NULL DEFAULT 0,
  `is_test` TINYINT(1) NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_ar_state_reason` CHECK (
    `state_reason` IS NULL OR
    `state_reason` IN (
      'completed', 'blocking_limit', 'image_error', 'model_error',
      'aborted_streaming', 'prompt_too_long', 'stop_hook_prevented',
      'aborted_tools', 'hook_stopped', 'max_turns', 'error_max_budget',
      'error_max_retries', 'next_turn', 'collapse_drain_retry',
      'reactive_compact_retry', 'max_output_escalate', 'max_output_recovery',
      'stop_hook_blocking', 'token_budget_continue', 'running',
      'waiting_for_user_choice', 'permission_denied', 'context_exhausted',
      'cancelled'
    )
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `agent_run` (
  `id`, `session_id`, `user_id`, `status`, `state_reason`, `messages`,
  `started_at`, `created_at`, `updated_at`
) VALUES (
  1, 'synthetic-session', 101, 'terminated', 'completed', JSON_ARRAY(),
  '2026-07-20 10:00:00.000', '2026-07-20 10:00:00.000', '2026-07-20 10:00:01.000'
);

CREATE TABLE `announcement` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `type` VARCHAR(16) NOT NULL DEFAULT 'plain',
  `title` VARCHAR(200) NOT NULL,
  `content` LONGTEXT NOT NULL,
  `is_important` TINYINT(1) NOT NULL DEFAULT 0,
  `audience` VARCHAR(32) NOT NULL DEFAULT 'all',
  `status` VARCHAR(16) NOT NULL DEFAULT 'draft',
  `published_at` DATETIME NULL,
  `expires_at` DATETIME NULL,
  `created_by` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `announcement_read` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `announcement_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `read_at` DATETIME NOT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_annread` (`announcement_id`, `user_id`),
  KEY `idx_annread_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `survey_question` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `announcement_id` BIGINT UNSIGNED NOT NULL,
  `order_index` INT NOT NULL DEFAULT 0,
  `question_type` VARCHAR(16) NOT NULL,
  `title` VARCHAR(500) NOT NULL,
  `options` JSON NULL,
  `rating_max` INT NULL,
  `rating_style` VARCHAR(10) NULL,
  `required` TINYINT(1) NOT NULL DEFAULT 1,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_sq_ann` (`announcement_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `survey_response` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `announcement_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `submitted_at` DATETIME NOT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_sr` (`announcement_id`, `user_id`),
  KEY `idx_sr_ann` (`announcement_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `survey_answer` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `response_id` BIGINT UNSIGNED NOT NULL,
  `question_id` BIGINT UNSIGNED NOT NULL,
  `answer_options` JSON NULL,
  `answer_rating` INT NULL,
  `answer_text` TEXT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_sa_response` (`response_id`),
  KEY `idx_sa_question` (`question_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `llm_provider` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(100) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `llm_provider` (`id`, `name`) VALUES (1, 'ali-dashscope');

CREATE TABLE `ai_service` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `model_key` VARCHAR(100) NOT NULL,
  `display_name` VARCHAR(100) NOT NULL,
  `is_thinking` TINYINT(1) DEFAULT 0,
  `supports_thinking` TINYINT(1) DEFAULT 0,
  `thinking_only` TINYINT(1) DEFAULT 0,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `service_type` VARCHAR(20) NOT NULL DEFAULT 'llm',
  `capability_json` JSON NOT NULL DEFAULT (JSON_OBJECT()),
  `latency_tier` VARCHAR(20) DEFAULT 'standard',
  `quality_tier` VARCHAR(20) DEFAULT 'standard',
  `deprecated_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `model_key` (`model_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `ai_service` (
  `id`, `model_key`, `display_name`, `is_active`, `service_type`,
  `capability_json`, `deprecated_at`
) VALUES (
  10, 'qwen3-vl-flash', 'qwen3-vl-flash', 1, 'llm',
  JSON_OBJECT('input_modalities', JSON_ARRAY('text', 'image')), NULL
);

CREATE TABLE `ai_service_route` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `model_id` BIGINT UNSIGNED NOT NULL,
  `provider_id` BIGINT UNSIGNED NOT NULL,
  `provider_model_id` VARCHAR(100) NOT NULL,
  `priority` INT DEFAULT 0,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_model_provider` (`model_id`, `provider_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `ai_service_route` (
  `model_id`, `provider_id`, `provider_model_id`, `priority`, `is_active`
) VALUES (10, 1, 'qwen3-vl-flash-2026-01-22', 5, 1);

CREATE TABLE `task_profile` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id` VARCHAR(80) NOT NULL,
  `display_name` VARCHAR(100) NOT NULL,
  `description` TEXT NULL,
  `service_type` VARCHAR(20) NOT NULL,
  `requirements` JSON NOT NULL DEFAULT (JSON_OBJECT()),
  `default_service_id` BIGINT UNSIGNED NULL,
  `user_selectable` TINYINT(1) DEFAULT 0,
  `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `task_id` (`task_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `task_profile_service` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_profile_id` BIGINT UNSIGNED NOT NULL,
  `service_id` BIGINT UNSIGNED NOT NULL,
  `role` VARCHAR(20) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_profile_service_role` (`task_profile_id`, `service_id`, `role`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `pricing_rule` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `service_type` VARCHAR(50) NOT NULL,
  `provider` VARCHAR(50) NOT NULL,
  `model` VARCHAR(100) NULL,
  `billing_mode` VARCHAR(20) NOT NULL DEFAULT 'flat',
  `flat_unit` VARCHAR(10) NOT NULL DEFAULT 'call',
  `input_price_per_m_tok` DECIMAL(10,4) DEFAULT 0,
  `output_price_per_m_tok` DECIMAL(10,4) DEFAULT 0,
  `price_per_call` DECIMAL(10,4) DEFAULT 0,
  `price_per_gb` DECIMAL(10,4) DEFAULT 0,
  `sell_input_price_per_m_tok` DECIMAL(10,4) DEFAULT 0,
  `sell_output_price_per_m_tok` DECIMAL(10,4) DEFAULT 0,
  `sell_price_per_call` DECIMAL(10,4) DEFAULT 0,
  `sell_price_per_gb` DECIMAL(10,4) DEFAULT 0,
  `is_active` TINYINT(1) DEFAULT 1,
  `created_at` DATETIME(3) NULL,
  `updated_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pricing_lookup` (`service_type`, `provider`, `model`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `skill_template` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(100) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `skill` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(100) NOT NULL,
  `visibility` VARCHAR(32) NOT NULL,
  `parent_user_id` BIGINT UNSIGNED NOT NULL,
  `owner_user_id` BIGINT UNSIGNED NOT NULL,
  `source_type` VARCHAR(64) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `skill` (
  `name`, `visibility`, `parent_user_id`, `owner_user_id`, `source_type`
) VALUES ('官方示例技能', 'official', 0, 0, 'custom');

CREATE TABLE `credit_account` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `credit_cycle` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `trial_grant` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `user_booster_balance` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `membership_event` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `credit_reservation` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `credit_reservation_item` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `credit_transaction` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `sop_run` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `sop_node_run` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `chatbot_session` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `chatbot_message` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `sales_session` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE `sales_message` (`id` BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;

INSERT INTO `credit_account` VALUES (1);
INSERT INTO `credit_cycle` VALUES (1);
INSERT INTO `trial_grant` VALUES (1);
INSERT INTO `user_booster_balance` VALUES (1);
INSERT INTO `membership_event` VALUES (1);
INSERT INTO `credit_reservation` VALUES (1);
INSERT INTO `credit_reservation_item` VALUES (1);
INSERT INTO `credit_transaction` VALUES (1);
INSERT INTO `sop_run` VALUES (1);
INSERT INTO `sop_node_run` VALUES (1);
INSERT INTO `chatbot_session` VALUES (1);
INSERT INTO `chatbot_message` VALUES (1);
INSERT INTO `sales_session` VALUES (1);
INSERT INTO `sales_message` VALUES (1);

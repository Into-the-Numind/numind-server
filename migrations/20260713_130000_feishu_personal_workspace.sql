-- Feishu personal workspace persistence foundation (MySQL 8).
-- The feature remains gated by features.feishu_integration.enabled.

ALTER TABLE `user_third_party_account`
  ADD COLUMN `connection_state` VARCHAR(32) NOT NULL DEFAULT 'none' AFTER `connected_at`,
  ADD COLUMN `lark_cli_version` VARCHAR(32) NULL AFTER `connection_state`,
  ADD COLUMN `granted_scopes_json` JSON NULL AFTER `lark_cli_version`,
  ADD COLUMN `capability_state_json` JSON NULL AFTER `granted_scopes_json`,
  ADD COLUMN `last_success_at` DATETIME(3) NULL AFTER `capability_state_json`,
  ADD COLUMN `last_error_code` VARCHAR(128) NULL AFTER `last_success_at`,
  ADD COLUMN `generation` BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER `last_error_code`;

-- Preserve the compatibility boolean while making the new state immediately
-- truthful for accounts connected before this migration.
UPDATE `user_third_party_account`
SET `connection_state` = 'connected'
WHERE `connected` = 1;

CREATE TABLE `feishu_cli_vault` (
  `user_id` INT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `ciphertext` LONGBLOB NOT NULL,
  `key_version` VARCHAR(32) NOT NULL,
  `checksum` VARCHAR(64) NOT NULL,
  `revision` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE `feishu_auth_session` (
  `id` CHAR(36) NOT NULL,
  `user_id` INT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `operation_id` CHAR(36) NULL,
  `phase` VARCHAR(32) NOT NULL,
  `requested_scopes_json` JSON NOT NULL,
  `state` VARCHAR(32) NOT NULL,
  `lease_owner` VARCHAR(128) NULL,
  `lease_until` DATETIME(3) NULL,
  `expires_at` DATETIME(3) NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  `completed_at` DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  KEY `idx_feishu_auth_session_user_generation` (`user_id`, `generation`),
  KEY `idx_feishu_auth_session_operation` (`operation_id`),
  KEY `idx_feishu_auth_session_lease` (`state`, `lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE `feishu_operation` (
  `id` CHAR(36) NOT NULL,
  `user_id` INT UNSIGNED NOT NULL,
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

ALTER TABLE `agent_run`
  ADD COLUMN `pending_external_action_json` JSON NULL AFTER `pending_question_at`,
  ADD COLUMN `pending_external_action_at` DATETIME(3) NULL AFTER `pending_external_action_json`;

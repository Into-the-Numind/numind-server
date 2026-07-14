-- Pre-2026-07-13 compatibility surface for the MySQL 8 Feishu gate.
-- It is deliberately limited to the two existing tables that the feature
-- migration alters, using the checked-in historical column contracts.

CREATE TABLE `user_third_party_account` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` INT UNSIGNED NOT NULL,
  `provider` VARCHAR(32) NOT NULL,
  `app_id` VARCHAR(64) NOT NULL,
  `app_secret_enc` BLOB NULL,
  `access_token_enc` BLOB NULL,
  `refresh_token_enc` BLOB NULL,
  `token_expires_at` DATETIME(3) NULL,
  `scopes` VARCHAR(512) NULL,
  `connected` TINYINT(1) NOT NULL DEFAULT 0,
  `connected_at` DATETIME(3) NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_user_provider` (`user_id`, `provider`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE `agent_run` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` INT UNSIGNED NOT NULL,
  `session_id` VARCHAR(64) NULL,
  `status` VARCHAR(20) NOT NULL DEFAULT 'running',
  `state_reason` VARCHAR(50) NULL,
  `messages` JSON NOT NULL,
  `reservation_id` BIGINT UNSIGNED NULL,
  `started_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `ended_at` DATETIME(3) NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `terminal_metadata` JSON NULL,
  `cancellation_requested_at` DATETIME NULL DEFAULT NULL,
  `agent_definition_id` BIGINT UNSIGNED NULL DEFAULT NULL,
  `pending_question_json` JSON NULL,
  `pending_question_at` TIMESTAMP(3) NULL,
  `use_compact_v2` BOOLEAN NOT NULL DEFAULT FALSE,
  `is_pinned` BOOLEAN NOT NULL DEFAULT FALSE,
  `session_name` VARCHAR(255) NOT NULL DEFAULT '',
  `is_deleted` BOOLEAN NOT NULL DEFAULT FALSE,
  `is_test` BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (`id`),
  KEY `idx_ar_user_started` (`user_id`, `started_at`),
  KEY `idx_ar_session` (`session_id`),
  KEY `idx_ar_status_started` (`status`, `started_at`),
  KEY `idx_ar_agent_def_id` (`agent_definition_id`),
  KEY `idx_ar_state_pending` (`state_reason`, `pending_question_at`),
  KEY `idx_ar_pinned` (`is_pinned`),
  KEY `idx_ar_deleted` (`is_deleted`),
  KEY `idx_ar_is_test` (`is_test`),
  CONSTRAINT `chk_ar_status` CHECK (`status` IN ('running', 'terminated')),
  CONSTRAINT `chk_ar_state_reason` CHECK (
    `state_reason` IS NULL OR `state_reason` IN (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

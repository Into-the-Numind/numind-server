-- Migration: context_budget_compression
-- Feature: context-budget-compression
-- Date: 2026-04-25
-- Description: Add durable storage for token estimation profiles, budget policies,
--   summary cache, budget events, and nullable budget metadata on credit_reservation.
-- Rollback: 20260425_172000_context_budget_compression_rollback.sql

-- ---------------------------------------------------------------------------
-- Table: token_estimation_profile
-- Purpose: model/provider-level call-before-token estimation profiles.
-- See spec §3.2.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS token_estimation_profile (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  provider VARCHAR(50) NOT NULL DEFAULT '',
  model VARCHAR(100) NOT NULL DEFAULT '',
  model_family VARCHAR(80) NOT NULL DEFAULT '',
  service_type VARCHAR(30) NOT NULL DEFAULT 'llm_chat',
  profile_json JSON NOT NULL,
  safety_multiplier DECIMAL(8,4) NOT NULL DEFAULT 1.1500,
  calibration_multiplier DECIMAL(8,4) NOT NULL DEFAULT 1.0000,
  calibration_sample_count INT NOT NULL DEFAULT 0,
  calibration_p50_abs_error DECIMAL(8,4) DEFAULT NULL,
  calibration_p90_abs_error DECIMAL(8,4) DEFAULT NULL,
  calibration_p99_under_ratio DECIMAL(8,4) DEFAULT NULL,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  is_active TINYINT(1) NOT NULL DEFAULT 1,
  is_fallback TINYINT(1) NOT NULL DEFAULT 0,
  change_reason VARCHAR(255) DEFAULT NULL,
  updated_by VARCHAR(80) DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_tep_lookup (provider, model, service_type, is_active),
  INDEX idx_tep_family (provider, model_family, service_type, is_active),
  INDEX idx_tep_fallback (is_fallback, service_type, is_active)
);

-- ---------------------------------------------------------------------------
-- Table: context_budget_policy
-- Purpose: operation-level budget policies. Versioning is append-only.
-- See spec §3.3.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS context_budget_policy (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  operation VARCHAR(80) NOT NULL,
  reserved_output_tokens INT NOT NULL,
  safe_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  fixed_overhead_tokens INT NOT NULL DEFAULT 512,
  soft_threshold_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.7000,
  hard_threshold_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  charge_user TINYINT(1) NOT NULL DEFAULT 1,
  description VARCHAR(255) DEFAULT NULL,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  is_active TINYINT(1) NOT NULL DEFAULT 1,
  change_reason VARCHAR(255) DEFAULT NULL,
  updated_by VARCHAR(80) DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_cbp_operation_active (operation, is_active)
);

-- ---------------------------------------------------------------------------
-- Table: context_summary
-- Purpose: stores async/sync compression results; scoped per user and scope.
-- Must not store cross-user content. All queries must filter by owner_user_id.
-- See spec §3.4.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS context_summary (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL,
  owner_user_id BIGINT UNSIGNED DEFAULT NULL
    COMMENT 'parent account owner for B2B2C scopes; equals user_id for standalone users',
  scope_type VARCHAR(40) NOT NULL COMMENT 'sop_run | chatbot_session | salesrag_session | document | internal',
  scope_id VARCHAR(100) NOT NULL,
  source_hash CHAR(64) NOT NULL,
  source_fragment_ids JSON NOT NULL,
  summary_text MEDIUMTEXT NOT NULL,
  summary_token_estimate INT NOT NULL DEFAULT 0,
  original_token_estimate INT NOT NULL DEFAULT 0,
  model VARCHAR(100) NOT NULL DEFAULT '',
  provider VARCHAR(50) NOT NULL DEFAULT '',
  status VARCHAR(20) NOT NULL DEFAULT 'ready' COMMENT 'pending | processing | ready | failed',
  error_message VARCHAR(500) DEFAULT NULL,
  created_by_operation VARCHAR(80) NOT NULL DEFAULT '',
  expires_at DATETIME DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_summary_scope_hash (owner_user_id, scope_type, scope_id, source_hash),
  INDEX idx_summary_user_scope (owner_user_id, scope_type, scope_id, status),
  INDEX idx_summary_status (status, updated_at)
);

-- ---------------------------------------------------------------------------
-- Table: context_budget_event
-- Purpose: audit/observability table; stores metadata only, not prompt content.
-- See spec §3.5.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS context_budget_event (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED DEFAULT NULL,
  operation VARCHAR(80) NOT NULL,
  task_id VARCHAR(80) DEFAULT NULL,
  provider VARCHAR(50) NOT NULL DEFAULT '',
  model VARCHAR(100) NOT NULL DEFAULT '',
  context_window INT NOT NULL DEFAULT 0,
  max_output_tokens INT NOT NULL DEFAULT 0,
  reserved_output_tokens INT NOT NULL DEFAULT 0,
  fixed_overhead_tokens INT NOT NULL DEFAULT 0,
  safe_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  safe_input_budget INT NOT NULL DEFAULT 0,
  estimated_before INT NOT NULL DEFAULT 0,
  estimated_after INT NOT NULL DEFAULT 0,
  actual_prompt_tokens INT DEFAULT NULL,
  actual_completion_tokens INT DEFAULT NULL,
  reserve_amount BIGINT DEFAULT NULL,
  reconcile_delta BIGINT DEFAULT NULL,
  compression_actions JSON DEFAULT NULL,
  dropped_fragment_count INT NOT NULL DEFAULT 0,
  summarized_fragment_count INT NOT NULL DEFAULT 0,
  critical_fragment_count INT NOT NULL DEFAULT 0,
  calibration_ratio DECIMAL(10,4) DEFAULT NULL,
  token_profile_id BIGINT UNSIGNED DEFAULT NULL,
  budget_policy_id BIGINT UNSIGNED DEFAULT NULL,
  reservation_id BIGINT UNSIGNED DEFAULT NULL,
  usage_record_id BIGINT UNSIGNED DEFAULT NULL,
  status VARCHAR(30) NOT NULL DEFAULT 'ok' COMMENT 'ok | compressed | failed | skipped',
  error_code VARCHAR(80) DEFAULT NULL,
  metadata JSON DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_cbe_user_created (user_id, created_at),
  INDEX idx_cbe_operation_created (operation, created_at),
  INDEX idx_cbe_status_created (status, created_at)
);

-- ---------------------------------------------------------------------------
-- Alter: credit_reservation
-- Make coefficient_id nullable; add context-budget estimation metadata columns.
-- Existing rows remain valid with estimation_source='credit_coefficient'.
-- See spec §3.6.
-- ---------------------------------------------------------------------------
ALTER TABLE credit_reservation
  MODIFY coefficient_id BIGINT UNSIGNED NULL,
  ADD COLUMN estimation_source VARCHAR(30) NOT NULL DEFAULT 'credit_coefficient'
    COMMENT 'credit_coefficient | context_budget',
  ADD COLUMN token_profile_id BIGINT UNSIGNED DEFAULT NULL,
  ADD COLUMN estimated_prompt_tokens INT NOT NULL DEFAULT 0,
  ADD COLUMN estimated_completion_tokens INT NOT NULL DEFAULT 0,
  ADD COLUMN provider VARCHAR(50) NOT NULL DEFAULT '',
  ADD COLUMN model VARCHAR(100) NOT NULL DEFAULT '',
  ADD COLUMN context_budget_event_id BIGINT UNSIGNED DEFAULT NULL,
  ADD INDEX idx_cr_token_profile (token_profile_id),
  ADD INDEX idx_cr_budget_event (context_budget_event_id);

-- ---------------------------------------------------------------------------
-- Seeds: default context_budget_policy rows (spec §2.3)
-- Using INSERT IGNORE to remain idempotent.
-- ---------------------------------------------------------------------------
INSERT IGNORE INTO context_budget_policy
  (operation, reserved_output_tokens, safe_ratio, fixed_overhead_tokens, soft_threshold_ratio, hard_threshold_ratio, charge_user, description, version, is_active, change_reason, updated_by)
VALUES
  ('sop_run',             16384, 0.8500, 512,  0.7000, 0.8500, 1, 'Default policy for SOP node execution',          1, 1, 'initial seed', 'system'),
  ('sop_chat',             8192, 0.8500, 512,  0.7000, 0.8500, 1, 'Default policy for SOP inline chat',             1, 1, 'initial seed', 'system'),
  ('chatbot_chat',         8192, 0.8500, 512,  0.7000, 0.8500, 1, 'Default policy for chatbot chat',                1, 1, 'initial seed', 'system'),
  ('salesrag_chat',        8192, 0.8500, 1024, 0.7000, 0.8500, 1, 'Default policy for SalesRAG chat',               1, 1, 'initial seed', 'system'),
  ('context_compression',  4096, 0.8500, 512,  0.7000, 0.8500, 0, 'Internal compression LLM call; not user-billed', 1, 1, 'initial seed', 'system'),
  ('default_llm_chat',     8192, 0.8500, 512,  0.7000, 0.8500, 1, 'Fallback policy for untagged internal LLM calls', 1, 1, 'initial seed', 'system');

-- agent-mode-billing T10: agent_run.is_test audit column.
-- Marks a parent-account Builder 试聊 (test-chat) run. Billing routing keys off
-- RunRequest.IsTest → admin_test pool; this column is the persisted audit marker
-- so admin monitoring can distinguish test runs from real student runs.
--
-- GORM AutoMigrate also adds this column on startup (model.AgentRun.IsTest); this
-- migration is the canonical, idempotent DDL for environments that apply SQL
-- migrations explicitly. default:false → no GORM default:true Create gotcha.

SET @col_exists = (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_run' AND COLUMN_NAME = 'is_test'
);
SET @ddl = IF(@col_exists = 0,
  'ALTER TABLE agent_run ADD COLUMN is_test BOOLEAN NOT NULL DEFAULT 0, ADD INDEX idx_ar_is_test (is_test)',
  'SELECT "agent_run.is_test already exists" AS noop');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

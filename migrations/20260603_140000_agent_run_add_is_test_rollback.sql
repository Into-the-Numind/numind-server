-- Rollback for agent-mode-billing T10 agent_run.is_test column.
SET @col_exists = (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_run' AND COLUMN_NAME = 'is_test'
);
SET @ddl = IF(@col_exists = 1,
  'ALTER TABLE agent_run DROP INDEX idx_ar_is_test, DROP COLUMN is_test',
  'SELECT "agent_run.is_test absent" AS noop');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

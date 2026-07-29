-- Roll back weekly membership subscription plan metadata.

SET @db_name = DATABASE();

SET @has_cycle_credits = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'subscription'
    AND COLUMN_NAME = 'cycle_credits'
);
SET @sql = IF(
  @has_cycle_credits > 0,
  'ALTER TABLE `subscription` DROP COLUMN `cycle_credits`',
  'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_plan_type = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'subscription'
    AND COLUMN_NAME = 'plan_type'
);
SET @sql = IF(
  @has_plan_type > 0,
  'ALTER TABLE `subscription` DROP COLUMN `plan_type`',
  'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

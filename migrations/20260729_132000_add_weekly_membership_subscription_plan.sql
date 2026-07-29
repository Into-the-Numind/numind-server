-- Add subscription plan metadata for the 7-day weekly membership.
--
-- Existing rows default to monthly/2000 so the historical monthly behavior
-- remains byte-compatible. Weekly rows store plan_type='weekly' and
-- cycle_credits=500.

SET @db_name = DATABASE();

SET @has_plan_type = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'subscription'
    AND COLUMN_NAME = 'plan_type'
);
SET @sql = IF(
  @has_plan_type = 0,
  'ALTER TABLE `subscription` ADD COLUMN `plan_type` VARCHAR(20) NOT NULL DEFAULT ''monthly'' COMMENT ''Subscription plan type: monthly or weekly'' AFTER `total_months_purchased`',
  'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_cycle_credits = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'subscription'
    AND COLUMN_NAME = 'cycle_credits'
);
SET @sql = IF(
  @has_cycle_credits = 0,
  'ALTER TABLE `subscription` ADD COLUMN `cycle_credits` INT NOT NULL DEFAULT 2000 COMMENT ''Credits granted per subscription cycle'' AFTER `plan_type`',
  'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE `subscription`
SET `plan_type` = 'monthly'
WHERE `plan_type` IS NULL OR `plan_type` = '';

UPDATE `subscription`
SET `cycle_credits` = 2000
WHERE `cycle_credits` IS NULL OR `cycle_credits` <= 0;

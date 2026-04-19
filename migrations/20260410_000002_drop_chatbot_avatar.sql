-- 移除 chatbot_config.avatar 字段：该功能未被使用且决定下线
-- 注：使用 PREPARE 动态 SQL 实现幂等是因为 MySQL 8.0.28- 不支持 DROP COLUMN IF EXISTS
-- Rollback: ALTER TABLE chatbot_config ADD COLUMN avatar VARCHAR(500) DEFAULT '' AFTER description;

SET @col_exists := (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'chatbot_config'
      AND COLUMN_NAME = 'avatar'
);

SET @sql := IF(@col_exists > 0,
    'ALTER TABLE chatbot_config DROP COLUMN avatar',
    'SELECT "column avatar already dropped" AS note'
);

PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

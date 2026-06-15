-- org-branding: User 表新增机构品牌名字段 company_name
-- 仅父账户（parent_user_id IS NULL）有意义，子账户继承父账户的值；
-- 空串=未设置（展示层兜底"有数AI"）。
-- MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS，用 information_schema 守卫保证幂等。
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'user'
    AND COLUMN_NAME = 'company_name'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `user` ADD COLUMN `company_name` VARCHAR(100) NOT NULL DEFAULT '''' AFTER `avatar_url`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

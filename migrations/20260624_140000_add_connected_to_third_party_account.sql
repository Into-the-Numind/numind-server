-- 飞书集成 G2-authorize device-code 重做（2026-06-24）：
-- user_third_party_account 追加连接状态元信息列 connected / connected_at。
--
-- 背景：device-code 方案下 token 由 lark-cli 在每用户持久 HOME 里保管并自动刷新，
-- 不再入库。原有 *_enc 密文列与 token_expires_at / scopes 成为历史遗留（保留不删，
-- 避免 migration 复杂度），改由 connected / connected_at 作为连接状态的权威 DB 标志。
--
-- 幂等：用 IF NOT EXISTS 形式的存在性判断避免重复执行报错（MySQL 8.0 ADD COLUMN 无
-- 原生 IF NOT EXISTS，用 information_schema 守卫）。本表 feature flag 关闭时休眠不可达；
-- AutoMigrate 在 flag on 时也会自动加这两列（helper.go 条件迁移），本文件为权威 schema。

-- connected：是否已完成 device-code 授权。
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'user_third_party_account'
    AND COLUMN_NAME = 'connected'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `user_third_party_account` ADD COLUMN `connected` TINYINT(1) NOT NULL DEFAULT 0 AFTER `app_id`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- connected_at：完成授权的时间，未连接时 NULL。
SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'user_third_party_account'
    AND COLUMN_NAME = 'connected_at'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `user_third_party_account` ADD COLUMN `connected_at` DATETIME(3) NULL AFTER `connected`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ROLLBACK for 20260519_120000_drop_billing_account_table.sql
--
-- Recreates billing_account with the exact original schema from
-- migrations/add_billing_tables.sql so that AutoMigrate / GORM model
-- (if restored alongside this rollback) finds the expected structure.
--
-- Data restoration:
--   Pre-drop verification confirmed dev/qa/prod were all 0 rows. If a
--   mysqldump backup was nonetheless taken (recommended), restore it after
--   the CREATE below:
--     mysql <db> < /root/backups/billing_account_pre_drop_<date>.sql

CREATE TABLE IF NOT EXISTS `billing_account` (
  `id` int unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL COMMENT '用户ID',
  `balance_cents` bigint NOT NULL DEFAULT 0 COMMENT '当前余额（分）',
  `total_consumed_cents` bigint NOT NULL DEFAULT 0 COMMENT '累计消费（分）',
  `total_recharged_cents` bigint NOT NULL DEFAULT 0 COMMENT '累计充值（分）',
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT 'active, suspended, frozen',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_ba_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户计费账户';

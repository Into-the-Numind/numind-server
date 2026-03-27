-- 积分制计费系统 — 数据库迁移
-- 注意：GORM AutoMigrate 会自动创建表和索引，此文件用于手动执行或生产环境部署

-- 1. 积分账户表
CREATE TABLE IF NOT EXISTS `credit_account` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL,
  `balance` bigint NOT NULL DEFAULT 0,
  `status` varchar(20) NOT NULL DEFAULT 'active',
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_credit_account_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 2. 积分包表
CREATE TABLE IF NOT EXISTS `credit_package` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL,
  `type` varchar(20) NOT NULL,
  `total_credits` bigint NOT NULL,
  `remain_credits` bigint NOT NULL,
  `activated_at` datetime(3) NOT NULL,
  `expires_at` datetime(3) NOT NULL,
  `order_id` bigint unsigned DEFAULT NULL,
  `status` varchar(20) NOT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_cp_user_status_expires` (`user_id`, `status`, `expires_at`),
  KEY `idx_cp_order` (`order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 3. 积分流水表
CREATE TABLE IF NOT EXISTS `credit_transaction` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL,
  `package_id` bigint unsigned NOT NULL,
  `amount` bigint NOT NULL,
  `operation` varchar(100) NOT NULL,
  `usage_record_id` bigint unsigned DEFAULT NULL,
  `biz_ref_type` varchar(50) DEFAULT NULL,
  `biz_ref_id` varchar(100) DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_ct_user_created` (`user_id`, `created_at`),
  KEY `idx_ct_package` (`package_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 4. 给 usage_record 表增加积分扣减字段
ALTER TABLE `usage_record`
  ADD COLUMN IF NOT EXISTS `credits_deducted` bigint NOT NULL DEFAULT 0;

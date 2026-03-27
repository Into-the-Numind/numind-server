-- 支付订单表
-- 注意：GORM AutoMigrate 会自动创建表和索引，此文件用于手动执行或生产环境部署

CREATE TABLE IF NOT EXISTS `payment_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) NOT NULL,
  `user_id` int unsigned NOT NULL,
  `payer_id` int unsigned NOT NULL,
  `product_type` varchar(20) NOT NULL,
  `months` int NOT NULL DEFAULT 0,
  `amount` bigint NOT NULL,
  `pay_channel` varchar(20) DEFAULT NULL,
  `pay_status` varchar(20) NOT NULL DEFAULT 'pending',
  `trade_no` varchar(128) DEFAULT NULL,
  `code_url` varchar(512) DEFAULT NULL,
  `paid_at` datetime(3) DEFAULT NULL,
  `expired_at` datetime(3) NOT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order_no` (`order_no`),
  KEY `idx_order_user` (`user_id`),
  KEY `idx_order_payer` (`payer_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 创建payments表
CREATE TABLE IF NOT EXISTS `payments` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `out_trade_no` varchar(64) NOT NULL COMMENT '商户订单号',
  `transaction_id` varchar(64) DEFAULT NULL COMMENT '微信支付订单号',
  `user_id` bigint unsigned NOT NULL COMMENT '用户ID',
  `amount` bigint NOT NULL COMMENT '支付金额(分)',
  `description` varchar(255) DEFAULT NULL COMMENT '商品描述',
  `channel` varchar(20) NOT NULL COMMENT '支付渠道(wechat,alipay)',
  `status` varchar(20) NOT NULL DEFAULT 'pending' COMMENT '支付状态(pending,success,failed,cancelled)',
  `pay_method` varchar(20) DEFAULT NULL COMMENT '支付方式(native,miniprogram,jsapi)',
  `openid` varchar(64) DEFAULT NULL COMMENT '用户openid',
  `prepay_id` varchar(64) DEFAULT NULL COMMENT '预支付ID',
  `code_url` varchar(512) DEFAULT NULL COMMENT '二维码链接',
  `notify_data` text COMMENT '回调数据',
  `paid_at` timestamp NULL DEFAULT NULL COMMENT '支付时间',
  `expire_at` timestamp NULL DEFAULT NULL COMMENT '过期时间',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_out_trade_no` (`out_trade_no`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_status` (`status`),
  KEY `idx_channel` (`channel`),
  KEY `idx_created_at` (`created_at`),
  KEY `idx_transaction_id` (`transaction_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付记录表';

-- 添加索引
CREATE INDEX IF NOT EXISTS `idx_payments_user_status` ON `payments` (`user_id`, `status`);
CREATE INDEX IF NOT EXISTS `idx_payments_expire_at` ON `payments` (`expire_at`);

-- 插入示例数据（可选）
-- INSERT INTO `payments` (`out_trade_no`, `user_id`, `amount`, `description`, `channel`, `status`, `pay_method`) 
-- VALUES ('TEST_001', 1, 100, '测试商品', 'wechat', 'pending', 'native');

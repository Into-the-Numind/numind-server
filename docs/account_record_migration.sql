-- 账户记录表迁移文件
-- 用于记录用户的支付历史

-- 创建账户记录表
CREATE TABLE IF NOT EXISTS account_records (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    order_id BIGINT UNSIGNED NOT NULL,
    out_trade_no VARCHAR(64) NOT NULL,
    amount BIGINT NOT NULL COMMENT '支付金额（分）',
    amount_yuan DECIMAL(10,2) NOT NULL COMMENT '支付金额（元）',
    type VARCHAR(32) NOT NULL COMMENT '记录类型：payment(支付), refund(退款), bonus(赠送)',
    status VARCHAR(32) NOT NULL COMMENT '状态：success, failed, pending',
    description VARCHAR(255) COMMENT '描述信息',
    payment_at TIMESTAMP NOT NULL COMMENT '支付时间',
    channel VARCHAR(32) NOT NULL COMMENT '支付渠道：wechat, alipay等',
    remark VARCHAR(500) COMMENT '备注信息',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL,
    
    INDEX idx_user_id (user_id),
    INDEX idx_order_id (order_id),
    INDEX idx_out_trade_no (out_trade_no),
    INDEX idx_type (type),
    INDEX idx_status (status),
    INDEX idx_payment_at (payment_at),
    INDEX idx_channel (channel),
    INDEX idx_created_at (created_at),
    
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账户记录表';

-- 添加索引优化查询性能
CREATE INDEX idx_user_type_status ON account_records(user_id, type, status);
CREATE INDEX idx_user_payment_time ON account_records(user_id, payment_at DESC);

-- 插入示例数据（可选）
-- INSERT INTO account_records (user_id, order_id, out_trade_no, amount, amount_yuan, type, status, description, payment_at, channel, remark)
-- VALUES (1, 1, 'wx_1_1234567890', 9900, 99.00, 'payment', 'success', '开通会员服务', NOW(), 'wechat', '微信支付成功');

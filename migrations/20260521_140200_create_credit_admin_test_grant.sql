-- agent-mode-billing-integration #12/14
-- 2026-05-21
-- Create credit_admin_test_grant table for Agent 试聊独立测试配额（每月 5000，月底作废）

CREATE TABLE credit_admin_test_grant (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_user_id    INT UNSIGNED NOT NULL COMMENT 'B2B 父账户 user.id（独立账户也算父，即 parent_user_id = self.id）',
    granted_amount    INT UNSIGNED NOT NULL DEFAULT 5000 COMMENT '当月赠送积分（运营可调）',
    used_amount       INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '当月已用积分',
    remaining_amount  INT GENERATED ALWAYS AS (CAST(granted_amount AS SIGNED) - CAST(used_amount AS SIGNED)) STORED COMMENT '剩余积分（生成列，覆盖索引避免回表）',
    period_start      DATE NOT NULL COMMENT '当月起始日（YYYY-MM-01）',
    period_end        DATE NOT NULL COMMENT '当月最后一天（月底失效）',
    granted_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '入账时间',
    last_used_at      DATETIME NULL COMMENT '最近一次试聊扣费时间',
    UNIQUE KEY uq_parent_period (parent_user_id, period_start),
    INDEX idx_period_remaining (period_end, remaining_amount)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #12 — 配置者试聊独立测试配额（每月赠送 5000，月底作废，不累积）';

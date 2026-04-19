-- Credits System Q1: Add grant-source fields to credit_package
-- Part of credits-system-q1-grant feature (B2B2C 会员赋予流程)
--
-- 背景: 原 credits-system 假设"C 端用户自购会员"，实际业务是 B2B2C:
--   - 会员由 B 端父账户（parent user）通过用户端"帮开通"功能赋予，不走支付流程
--   - 月末对公转账结算
--   - 加量包保持 C 端自购（grant_source='self_purchase'）
--
-- 字段语义:
--   grant_source    : self_purchase=C端自购(原 booster 路径) / b2b_grant=B端父账户赋予
--   granter_user_id : 赋予者(parent user)ID，b2b_grant 时必填，self_purchase 为 NULL
--
-- 复合索引 idx_grant_source_granter 支撑 B2B 月度结算报表查询（按 granter + 时间窗）

ALTER TABLE `credit_package`
    ADD COLUMN `grant_source` ENUM('self_purchase', 'b2b_grant')
        NOT NULL DEFAULT 'self_purchase'
        COMMENT 'self_purchase=C端自购(booster) / b2b_grant=B端父账户赋予',
    ADD COLUMN `granter_user_id` INT UNSIGNED NULL
        COMMENT '赋予者(parent user)ID，b2b_grant 时必填',
    ADD INDEX `idx_grant_source_granter` (`grant_source`, `granter_user_id`, `activated_at`);

-- Credits System: Add billing_mode field to user table
-- Part of credits-system feature (Phase 0 契约冻结, Track A will execute)
-- See spec: numind-server/docs/superpowers/specs/2026-04-18-credits-system-design.md §2.2

ALTER TABLE `user`
    ADD COLUMN billing_mode ENUM('legacy_tier', 'credits')
    NOT NULL DEFAULT 'credits'
    COMMENT 'legacy_tier=旧次数制老会员（Grandfathering）；credits=新积分制'
    AFTER tier_expires;

CREATE INDEX idx_user_billing_mode ON `user`(billing_mode, tier_expires)
    COMMENT '复合索引：billing_mode 分布 + cron 扫 legacy_tier 到期用户';

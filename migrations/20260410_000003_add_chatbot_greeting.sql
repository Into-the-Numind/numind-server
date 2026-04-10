-- 20260410_000003_add_chatbot_greeting.sql
-- 智能体打招呼配置：greeting_enabled 开关 + greeting_message 文案
-- 历史行默认关闭（greeting_enabled=0），行为完全不变
-- Rollback:
--   ALTER TABLE chatbot_config DROP COLUMN greeting_enabled, DROP COLUMN greeting_message;

ALTER TABLE chatbot_config
    ADD COLUMN greeting_enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否启用打招呼',
    ADD COLUMN greeting_message TEXT NULL COMMENT '打招呼文案（仅 greeting_enabled=1 时生效）';

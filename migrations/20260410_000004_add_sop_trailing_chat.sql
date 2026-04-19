-- 20260410_000004_add_sop_trailing_chat.sql
-- SOP 末尾 AI 聊天步骤可选：trailing_chat_enabled 开关
-- 历史行默认 1（开启），保持向后兼容 — 老模板行为不变
-- Rollback:
--   ALTER TABLE sop_template DROP COLUMN trailing_chat_enabled;

ALTER TABLE sop_template
    ADD COLUMN trailing_chat_enabled TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否在流程末尾追加 AI 聊天步骤';

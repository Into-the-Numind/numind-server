-- 添加 chatbot_session.pinned_at 字段
--
-- 用途：支持会话置顶功能
--   NULL     = 未置顶（默认值，所有现有行 backfill 为此值）
--   非 NULL  = 置顶时间（用于置顶组内排序）
--
-- 排序语义：
--   ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC
--   置顶组在前按 pinned_at 倒序，非置顶组按 updated_at 倒序
--
-- Feature: chatbot-session-rename-pin (S4 Task 1)
-- Spec:    docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-design.md §1.1
--
-- 执行顺序：本 forward migration 在 GORM model 改动 commit 前 apply 到 dev DB
--          rollback (DROP COLUMN) 留作回滚保险

ALTER TABLE chatbot_session
    ADD COLUMN pinned_at TIMESTAMP NULL DEFAULT NULL
    COMMENT '置顶时间，NULL=未置顶；非 NULL 用于置顶组内倒序排序'
    AFTER message_count;

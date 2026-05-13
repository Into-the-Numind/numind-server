-- Rollback: 移除 chatbot_session.pinned_at 字段
--
-- 影响：所有用户的置顶元数据将永久丢失（会话本体不受影响）。
--      会话回到 ORDER BY updated_at DESC 单一排序模式。
--
-- Feature: chatbot-session-rename-pin (S4 Task 1)
-- Spec:    docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-design.md §1.1

ALTER TABLE chatbot_session DROP COLUMN pinned_at;

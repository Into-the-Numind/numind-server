-- +migrate Down
-- sop-chatbot-visibility-scope rollback: 撤销可见范围权限相关 schema 变更
-- 见 docs/superpowers/specs/2026-05-13-sop-chatbot-visibility-scope-design.md §7.2
--
-- 顺序: 先 DROP 子表 (避免外键约束) 再 DROP COLUMN.
-- 若 grant 表中有数据, DROP TABLE 会一并丢失 (rollback 语义).

DROP TABLE IF EXISTS chatbot_visibility_grant;
DROP TABLE IF EXISTS sop_visibility_grant;

ALTER TABLE chatbot_config DROP COLUMN visibility_restricted;
ALTER TABLE sop_template DROP COLUMN visibility_restricted;

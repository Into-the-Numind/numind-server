-- 20260411_120100_add_sop_chat_message_model_name_and_duration_ms.sql
-- feature: sop-runtime-visual-redesign (spec §4.2)
-- 为 sop_chat_message 表新增 model_name 与 duration_ms 两列，用于 trailing chat 消息尾部
-- MetaFooter 展示该条消息实际调用的 LLM 模型标识及生成耗时。
-- 背景：S1 backend audit §5 原称 sop_chat_message 已存在 model_name，S2/S3 gate review (P0-1)
-- 重新核查后确认该字段实际不存在，故在本次 migration 一并补齐 model_name 与 duration_ms。
-- model_name 长度 VARCHAR(100) 与 sop_node.model_name / sop_node_run.model_name 对齐；
-- 历史行保持默认空字符串 '' 与 0，前端对空值降级展示为 "—"，保证向后兼容。
-- Rollback:
--   ALTER TABLE sop_chat_message DROP COLUMN duration_ms;
--   ALTER TABLE sop_chat_message DROP COLUMN model_name;

ALTER TABLE sop_chat_message
    ADD COLUMN model_name VARCHAR(100) NOT NULL DEFAULT '' COMMENT 'trailing chat 消息实际使用的 LLM 模型标识（长度对齐 sop_node.model_name）' AFTER thinking,
    ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0 COMMENT 'trailing chat 消息生成耗时（毫秒）' AFTER model_name;

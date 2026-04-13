-- 20260411_120000_add_sop_node_run_model_name.sql
-- feature: sop-runtime-visual-redesign (spec §4.1)
-- 为 sop_node_run 表新增 model_name 列，记录该次节点运行实际调用的 LLM 模型标识。
-- 运行时 MetaFooter 需展示每个节点的"模型身份"，避免前端渲染时再去 join sop_node
-- （sop_node 上的 model 字段是模板配置，节点实际运行可能因 fallback/路由变更而不同）。
-- 历史行保持默认空字符串 ''，前端对空值降级展示为 "—"，保证向后兼容。
-- Rollback:
--   ALTER TABLE sop_node_run DROP COLUMN model_name;

ALTER TABLE sop_node_run
    ADD COLUMN model_name VARCHAR(100) NOT NULL DEFAULT '' COMMENT '本次节点运行实际使用的 LLM 模型标识（长度对齐 sop_node.model_name）' AFTER latency_ms;

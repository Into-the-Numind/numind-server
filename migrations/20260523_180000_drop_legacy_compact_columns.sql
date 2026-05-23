-- Migration: Drop legacy compact columns from agent_run
-- Feature: compact-dead-schema-cleanup
-- Date:    2026-05-23
--
-- 背景：
--   1. V1 compact 包已删（compact-v1-removal feature, commit 04cf5c07）
--   2. V2 compact 已重新接到 adapter 层（v2-compact-adapter-integration, commit 609297a0）
--   3. 中间残留 5 个 dead 列：
--        - compact_state           (V1, JSON)        — 0 写入 0 读取
--        - compact_summary         (V1, LONGTEXT)    — 0 写入 0 读取（前端 expose 字段永远是空串）
--        - compact_state_v2        (V2, JSON)        — outer-loop maybeCompactV2 写过，但 adapter 不读
--        - total_tokens_used_v2    (V2, BIGINT)      — 校准 hook 写过，但 maybeCompactV2 读端已死
--        - context_window_limit_v2 (V2, INT)         — 从未被写入（仅作 override，runner 用 32K 默认）
--
-- 删后影响：runner 不再读这 5 个字段（同步 model 删字段、store 删接口）。前端 SessionSnapshot
-- 的 `compact_summary` 字段同步从 RunDetail / SessionSnapshot struct 删除（之前永远空串，前端无感）。
--
-- 保留：
--   - use_compact_v2          (BOOL)      — runner gate 仍读，作为 V2 全局 kill switch
--   - agent_tool_artifact     (TABLE)     — L0 工具写盘表，活跃
--
-- 用 `DROP COLUMN IF EXISTS` 保 idempotent（MySQL 8.0.29+，与项目惯例
-- 20260523_120000_context_management_v2_schema.sql 一致）。

ALTER TABLE agent_run
  DROP COLUMN IF EXISTS compact_state,
  DROP COLUMN IF EXISTS compact_summary,
  DROP COLUMN IF EXISTS compact_state_v2,
  DROP COLUMN IF EXISTS total_tokens_used_v2,
  DROP COLUMN IF EXISTS context_window_limit_v2;

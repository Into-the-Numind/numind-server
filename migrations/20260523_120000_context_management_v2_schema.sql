-- Migration: Context Management V2 schema (Agent Mode V1.5 板块 2 Task 2.1)
-- Feature: agent-mode-v15-context-v2
-- Strategy: D3 平行重做 — V1 字段 `compact_state` / `compact_summary` 完全不动，
--           新增 4 个 *_v2 列 + 新建 agent_tool_artifact 表（V2 专用）。
-- Rollback: 20260523_120000_context_management_v2_schema_rollback.sql

-- 1) agent_run 表加 4 个 V2 专用列（compactv2 包独立读写，V1 包不读写）
-- 用 `ADD COLUMN IF NOT EXISTS` 保 idempotent（MySQL 8.0.29+，与项目惯例 20260522_153000 / 20260521_200000 一致）。
ALTER TABLE agent_run
  ADD COLUMN IF NOT EXISTS compact_state_v2        JSON         NULL                COMMENT 'V2 compactv2 包写入的 CompactStateV2 JSON',
  ADD COLUMN IF NOT EXISTS total_tokens_used_v2    BIGINT       NOT NULL DEFAULT 0  COMMENT 'V2 reconcile 累计 token（provider usage 校准后）',
  ADD COLUMN IF NOT EXISTS use_compact_v2          BOOLEAN      NOT NULL DEFAULT FALSE COMMENT 'V2 feature flag — run 创建时冻结，run 进行中不可切换',
  ADD COLUMN IF NOT EXISTS context_window_limit_v2 INT          NULL                COMMENT 'V2 run 启动冻结的 model context_window 上限';

-- 注意 BOOL 字段 `default:true` 坑（database.md §6）— 这里 default 是 FALSE，无 Create 路径风险。

-- 2) 新建 agent_tool_artifact 表（V2 专用，文件落 data dir，DB 仅存路径 + 1KB 预览）
CREATE TABLE IF NOT EXISTS agent_tool_artifact (
    id              BIGINT AUTO_INCREMENT PRIMARY KEY,
    uuid            VARCHAR(64)  NOT NULL UNIQUE,
    agent_run_id    BIGINT       NOT NULL,
    tool_call_id    VARCHAR(128) NOT NULL,
    tool_name       VARCHAR(64)  NOT NULL,
    mime_type       VARCHAR(64)  NULL,
    size_bytes      BIGINT       NOT NULL,
    file_path       TEXT         NOT NULL                       COMMENT '相对路径或 COS key',
    storage_backend VARCHAR(16)  NOT NULL DEFAULT 'local',
    preview         TEXT         NULL                           COMMENT '前 1KB',
    is_expired      TINYINT(1)   NOT NULL DEFAULT 0,
    expires_at      DATETIME(3)  NULL,
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_ata_run_tool_call (agent_run_id, tool_call_id),
    INDEX idx_ata_expires       (expires_at, is_expired)
);

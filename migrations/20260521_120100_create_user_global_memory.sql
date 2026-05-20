-- Migration: create user_global_memory table
-- Feature: agent-mode-memory-system (#7/14)
-- Rollback: 20260521_120100_create_user_global_memory_rollback.sql

CREATE TABLE IF NOT EXISTS user_global_memory (
  id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id                    INT UNSIGNED NOT NULL                   COMMENT 'FK 到 user.id',
  kind                       VARCHAR(20) NOT NULL                    COMMENT 'learning/decision/issue/fact/preference（无 summary）',
  key_name                   VARCHAR(100) NOT NULL                   COMMENT 'Notepad key，user-key 唯一',
  value                      TEXT NOT NULL                           COMMENT 'Notepad value，写入前 html.EscapeString 转义',
  confidence                 FLOAT NOT NULL DEFAULT 1.0              COMMENT 'agent 自评置信度，0.0-1.0',
  source_type                VARCHAR(20) NOT NULL DEFAULT 'agent_tool' COMMENT 'agent/user_explicit/agent_tool',
  source_agent_definition_id BIGINT UNSIGNED NULL                   COMMENT '写入者 agent；source=user_explicit 时 NULL',
  created_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_ugm_user_key (user_id, key_name),
  KEY idx_ugm_user_kind (user_id, kind)
);

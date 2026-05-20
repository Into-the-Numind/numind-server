-- Migration: create agent_definition_history table
-- Feature: agent-mode-skill-system (#5/14)
-- Rollback: 20260522_220100_create_agent_definition_history_rollback.sql

CREATE TABLE IF NOT EXISTS agent_definition_history (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  agent_id        BIGINT UNSIGNED NOT NULL                COMMENT 'agent_definition.id',
  version         INT UNSIGNED NOT NULL                   COMMENT '该版本号',
  snapshot        JSON NOT NULL                           COMMENT 'agent_definition 完整行快照 + Skill body',
  changes_summary VARCHAR(200) NULL                       COMMENT 'biz 计算的人类可读改动摘要',
  created_by      INT UNSIGNED NOT NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_adh_agent_version (agent_id, version),
  KEY idx_adh_agent_created (agent_id, created_at)
);

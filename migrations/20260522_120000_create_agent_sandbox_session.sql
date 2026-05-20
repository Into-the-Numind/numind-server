-- agent-mode-sandbox-integration #4 — agent_sandbox_session 沙箱会话生命周期审计表
-- Blueprint §4.6.4 + V5 ADR Q1-Q4 落地 + #4 spec §2.1

CREATE TABLE IF NOT EXISTS agent_sandbox_session (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         INT UNSIGNED NOT NULL                         COMMENT 'FK to user.id',
  agent_run_id    BIGINT UNSIGNED NULL                          COMMENT 'FK to agent_run.id, #4 可空（PreToolCall 写入），#11/#12 后必填',
  container_id    VARCHAR(128) NOT NULL                         COMMENT 'Docker container ID (12+ char hash)',
  image_tag       VARCHAR(128) NOT NULL DEFAULT 'python:3.11-slim',
  status          VARCHAR(20)  NOT NULL DEFAULT 'running'       COMMENT 'running | terminated | failed',
  mem_limit_mb    INT          NOT NULL DEFAULT 512,
  cpu_quota       DECIMAL(3,1) NOT NULL DEFAULT 1.0,
  exit_code       INT NULL                                      COMMENT 'NULL = still running',
  error_msg       TEXT NULL                                     COMMENT 'destroy 失败原因或 Exec err',
  started_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  ended_at        DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_ass_user_started (user_id, started_at),
  KEY idx_ass_status (status),
  KEY idx_ass_run (agent_run_id),
  CONSTRAINT chk_ass_status CHECK (status IN ('running', 'terminated', 'failed'))
);

-- Phase 0 agent-mode #2 runtime skeleton; agent_run 是 Runtime 唯一持久化表，messages 列 turn 级整体覆写

CREATE TABLE IF NOT EXISTS agent_run (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         INT UNSIGNED NOT NULL COMMENT 'FK to user.id',
  session_id      VARCHAR(64) NULL COMMENT '会话标识，多 run 串成 session',
  status          VARCHAR(20) NOT NULL DEFAULT 'running' COMMENT 'running | terminated',
  state_reason    VARCHAR(50) NULL COMMENT '终止/继续原因',
  messages        JSON NOT NULL COMMENT 'Eino messages 列表，turn 级整体覆写',
  reservation_id  BIGINT UNSIGNED NULL COMMENT 'FK to credit_reservation.id；#2 NULL, #12 填充',
  started_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  ended_at        DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_ar_user_started (user_id, started_at),
  KEY idx_ar_session (session_id),
  KEY idx_ar_status_started (status, started_at),
  CONSTRAINT chk_ar_status CHECK (status IN ('running', 'terminated')),
  CONSTRAINT chk_ar_state_reason CHECK (
    state_reason IS NULL OR state_reason IN (
      'completed','blocking_limit','image_error','model_error',
      'aborted_streaming','prompt_too_long','stop_hook_prevented',
      'aborted_tools','hook_stopped','max_turns','error_max_budget','error_max_retries',
      'next_turn','collapse_drain_retry','reactive_compact_retry',
      'max_output_escalate','max_output_recovery','stop_hook_blocking','token_budget_continue'
    )
  )
);

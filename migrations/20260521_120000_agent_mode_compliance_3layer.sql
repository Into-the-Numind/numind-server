-- Feature: agent-mode-compliance-3layer (#13/14) — Layer-0/1/2 + injection/fence/scope 合规框架 + 2 张新表

CREATE TABLE IF NOT EXISTS compliance_rule (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  parent_user_id  INT UNSIGNED NOT NULL,
  rule_type       VARCHAR(32) NOT NULL COMMENT 'forbid_topic / forbid_brand / forbid_phrase / custom',
  rule_text       TEXT NOT NULL,
  priority        INT NOT NULL DEFAULT 100 COMMENT '小在前；同 priority 按 created_at 倒序',
  is_active       TINYINT(1) NOT NULL DEFAULT 1 COMMENT 'GORM default:true bool 坑 — store 用 UpdateColumn fixup',
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_parent_active_priority (parent_user_id, is_active, priority)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Layer-1 父账户级合规规则（运营可配；#14 落地管理端 CRUD UI）';

CREATE TABLE IF NOT EXISTS compliance_audit_log (
  id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  agent_run_id         BIGINT UNSIGNED NULL,
  parent_user_id       INT UNSIGNED NOT NULL,
  agent_definition_id  BIGINT UNSIGNED NULL,
  rule_layer           VARCHAR(8) NOT NULL COMMENT 'L0 / L1 / L2 / injection / fence / scope',
  rule_id              BIGINT UNSIGNED NULL COMMENT 'L1 时引用 compliance_rule.id；intentional no-FK; audit row survives source rule deletion',
  decision             VARCHAR(16) NOT NULL COMMENT 'allow / deny / sanitize / passthrough',
  triggered_text       TEXT NULL COMMENT '≤500 字符截断',
  reason               VARCHAR(255) NULL,
  created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_parent_created (parent_user_id, created_at),
  INDEX idx_run (agent_run_id),
  INDEX idx_layer_decision (rule_layer, decision)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Layer-0/1/2 + injection/fence/scope 合规判定异步审计日志';

-- Verify: SHOW INDEX FROM compliance_rule; SHOW INDEX FROM compliance_audit_log;

-- Agent 模式 #6/14 — agent-mode-permission-pipeline
-- Creates 2 tables: agent_permission_config + agent_permission_decision_log

CREATE TABLE IF NOT EXISTS agent_permission_config (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_user_id INT UNSIGNED NOT NULL COMMENT '隶属父账户（B2B2C 顶级账户）',
    rule_type      VARCHAR(32)  NOT NULL COMMENT 'tool_blacklist / tool_input_regex_deny / topic_blacklist',
    rule_key       VARCHAR(255) NOT NULL COMMENT '规则键（工具名 / 主题词）',
    rule_value     TEXT                  COMMENT '规则值（正则字符串 / 关键词列表 JSON）',
    action         VARCHAR(16)  NOT NULL DEFAULT 'deny' COMMENT 'deny / ask',
    message        VARCHAR(500)          COMMENT '触发后展示给学员的友好理由',
    is_active      TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '启用开关',
    created_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_apc_parent_active (parent_user_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #6 — L2 租户管理员权限规则配置';

CREATE TABLE IF NOT EXISTS agent_permission_decision_log (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_run_id        BIGINT UNSIGNED NOT NULL COMMENT 'agent_run.id 对齐（uint64）',
    user_id             INT UNSIGNED NOT NULL COMMENT '学员（子账户）',
    parent_user_id      INT UNSIGNED NOT NULL COMMENT '父账户（决定 L2 规则范围）',
    agent_definition_id BIGINT UNSIGNED NOT NULL COMMENT 'agent_definition.id',
    tool_name           VARCHAR(64)  NOT NULL,
    tool_input_digest   CHAR(64)     NOT NULL COMMENT 'SHA-256 完整 64 hex（对账匹配）',
    behavior            VARCHAR(16)  NOT NULL COMMENT 'allow / ask / deny',
    decision_reason     VARCHAR(32)  NOT NULL COMMENT '11 种 canonical 之一',
    validator_id        VARCHAR(64)  NOT NULL COMMENT '触发决策的 validator',
    message             TEXT                  COMMENT '展示文案（ask/deny 有，allow 一般 NULL）',
    latency_ms          INT          NOT NULL DEFAULT 0 COMMENT '决策耗时（ms）',
    created_at          DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_apdl_run_tool (agent_run_id, tool_name),
    INDEX idx_apdl_parent_created (parent_user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #6 — 权限决策审计日志';

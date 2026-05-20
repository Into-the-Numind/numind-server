-- agent-mode-tool-registry #3 — tool_definition + tool_factory_registry
-- Blueprint §8.10 (tool_definition) + spec §2.2 (tool_factory_registry, #3 read-only).

CREATE TABLE IF NOT EXISTS tool_definition (
  id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tool_name                  VARCHAR(128)   NOT NULL,
  display_name               VARCHAR(128)   NOT NULL,
  description                TEXT           NOT NULL,
  tool_source                VARCHAR(16)    NOT NULL,
  risk_level                 VARCHAR(16)    NOT NULL DEFAULT 'safe',
  requires_sandbox           TINYINT(1)     NOT NULL DEFAULT 0,
  requires_tenant_whitelist  TINYINT(1)     NOT NULL DEFAULT 0,
  input_schema               JSON,
  output_schema              JSON,
  is_enabled                 TINYINT(1)     NOT NULL DEFAULT 1,
  is_beta                    TINYINT(1)     NOT NULL DEFAULT 0,
  category                   VARCHAR(64),
  config_json                JSON,
  created_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_tool_name (tool_name),
  KEY idx_source_enabled (tool_source, is_enabled),
  CONSTRAINT chk_td_source CHECK (tool_source IN ('platform','mcp','cli','webhook')),
  CONSTRAINT chk_td_risk CHECK (risk_level IN ('safe','moderate','dangerous'))
);

CREATE TABLE IF NOT EXISTS tool_factory_registry (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  factory_id        VARCHAR(64)    NOT NULL,
  source_type       VARCHAR(16)    NOT NULL,
  display_name      VARCHAR(128)   NOT NULL,
  config_json       JSON,
  is_enabled        TINYINT(1)     NOT NULL DEFAULT 1,
  loaded_tools_count INT           NOT NULL DEFAULT 0,
  last_loaded_at    DATETIME(3),
  created_at        DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at        DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_factory_id (factory_id),
  CONSTRAINT chk_tfr_source CHECK (source_type IN ('platform','mcp','cli','webhook'))
);

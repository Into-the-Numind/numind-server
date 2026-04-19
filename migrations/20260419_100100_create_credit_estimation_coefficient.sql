-- Credits System: R2 estimation coefficient table (append-only version)
-- Part of credits-system feature (Phase 0 契约冻结, Track A will execute)
-- See spec §2.3

CREATE TABLE IF NOT EXISTS credit_estimation_coefficient (
    id                       BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    provider                 VARCHAR(50)   NOT NULL COMMENT 'ali/volc/dmxapi/baidu',
    model                    VARCHAR(100)  NOT NULL,
    operation                VARCHAR(50)   NOT NULL COMMENT '见 spec §1.7 枚举：sop_run/sop_chat/salesrag_chat/profile_analysis/file_parse/style_analysis/ocr',
    char_to_token_ratio      DECIMAL(6,3)  NOT NULL COMMENT '1 汉字 ≈ N token',
    completion_prompt_ratio  DECIMAL(6,3)  NOT NULL COMMENT 'completion/prompt 历史均值',
    safety_buffer_pct        DECIMAL(5,3)  NOT NULL DEFAULT 0.200 COMMENT '安全余量，0.200=+20%',
    version                  INT UNSIGNED  NOT NULL COMMENT '(provider,model,operation) 维度递增',
    is_active                TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '1=启用；同一 key 至多一行为 1',
    change_reason            VARCHAR(255)  DEFAULT NULL,
    updated_by               VARCHAR(64)   DEFAULT NULL,
    created_at               DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at               DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE KEY uk_provider_model_op_version (provider, model, operation, version),
    KEY idx_active_lookup (provider, model, operation, is_active),
    KEY idx_version_lookup (provider, model, operation, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='R2 估算系数（append-only：修改=insert 新 version，老 version 保留对账历史 reservation）';

-- Note: CHECK constraint (operation IN (...)) can be added conditionally based on MySQL version
-- MySQL 8.0.16+ enforces CHECK; older versions parse but ignore. See spec §2.11.5.
-- Track A's agent team should check prod MySQL version and add CHECK if ≥ 8.0.16

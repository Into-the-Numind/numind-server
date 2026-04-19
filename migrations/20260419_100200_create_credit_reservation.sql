-- Credits System: credit_reservation (预扣记录，状态机 reserved→reconciled|refunded|expired)
-- Part of credits-system feature (Phase 0 契约冻结, Track A will execute)
-- See spec §2.4

CREATE TABLE IF NOT EXISTS credit_reservation (
    id                  BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id             INT UNSIGNED  NOT NULL,
    reference_type      VARCHAR(50)   NOT NULL COMMENT 'sop_run/sop_chat/salesrag_chat',
    reference_id        VARCHAR(100)  NOT NULL,
    operation           VARCHAR(50)   NOT NULL,
    reserved_credits    BIGINT        NOT NULL,
    coefficient_id      BIGINT UNSIGNED NOT NULL COMMENT '应用层保证 FK 到 credit_estimation_coefficient.id',
    status              ENUM('reserved','reconciled','refunded','expired') NOT NULL DEFAULT 'reserved',
    actual_cost_cents   BIGINT        DEFAULT NULL,
    delta               BIGINT        DEFAULT NULL COMMENT 'actual - reserved',
    finalize_reason     ENUM('normal','op_failed','user_cancelled','provider_timeout',
                             'no_actual_cost','expired_by_cron','manual_refund')
                        DEFAULT NULL,
    idempotency_key     VARCHAR(64)   DEFAULT NULL COMMENT '防重试重复创建；允许 NULL（InnoDB 允许多 NULL，退化为非幂等）',
    reconciled_at       DATETIME(3)   DEFAULT NULL,
    created_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    KEY idx_user_status (user_id, status, created_at),
    KEY idx_status_created (status, created_at) COMMENT 'cron 扫 24h 未 reconcile',
    UNIQUE KEY uk_idempotency_key (idempotency_key),
    KEY idx_coefficient (coefficient_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='积分预扣记录（Reserve 写入，Reconcile/Refund/Expired 切换终态）';

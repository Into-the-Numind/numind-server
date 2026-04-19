-- Credits System: credit_reservation_item (FIFO 扣减明细)
-- Part of credits-system feature (Phase 0 契约冻结, Track A will execute)
-- See spec §2.5

CREATE TABLE IF NOT EXISTS credit_reservation_item (
    id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    reservation_id     BIGINT UNSIGNED NOT NULL COMMENT 'FK credit_reservation.id（应用层保证）',
    package_id         BIGINT UNSIGNED NOT NULL COMMENT 'FK credit_package.id（应用层保证）',
    credits            BIGINT          NOT NULL COMMENT '从此 package 扣的积分',
    package_type       VARCHAR(20)     NOT NULL COMMENT 'trial/subscription/booster 扣减时快照',
    package_expires_at DATETIME(3)     NOT NULL COMMENT '扣减时 package.expires_at 快照',
    seq                INT             NOT NULL COMMENT 'FIFO 扣减顺序号（1,2,...），非 INSERT 顺序',
    created_at         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    KEY idx_reservation (reservation_id),
    KEY idx_package (package_id, created_at) COMMENT '供查"某 package 被多少 reservation 冻结"',
    UNIQUE KEY uk_reservation_seq (reservation_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='预扣明细（一个 Reservation 按 FIFO 可能扣多个 Package）';

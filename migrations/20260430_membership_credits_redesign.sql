-- 20260430 membership credits redesign — 新建 5 张表
-- spec: docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md §2

-- §2.1 subscription
CREATE TABLE IF NOT EXISTS `subscription` (
  `id`                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`                  BIGINT UNSIGNED NOT NULL,
  `first_started_at`         DATETIME(0)     NOT NULL,
  `current_started_at`       DATETIME(0)     NOT NULL,
  `expires_at`               DATETIME(0)     NOT NULL,
  `total_months_purchased`   INT             NOT NULL,
  `source`                   ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`          BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`               DATETIME(0)     NOT NULL,
  `updated_at`               DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_sub_user_id` (`user_id`),
  KEY `idx_sub_expires_at` (`expires_at`),
  KEY `idx_sub_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='用户订阅主表，每个用户单行，原地更新';

-- §2.2 trial_grant
CREATE TABLE IF NOT EXISTS `trial_grant` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `granted_at`          DATETIME(0)     NOT NULL,
  `expires_at`          DATETIME(0)     NOT NULL,
  `credits_remaining`   INT             NOT NULL DEFAULT 200,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL DEFAULT 'b2b_grant',
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `created_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_trial_user_id` (`user_id`),
  KEY `idx_trial_expires_at` (`expires_at`),
  KEY `idx_trial_granter_expires` (`granter_user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='试用包记录，每个用户 lifetime 单行';

-- §2.3 credit_cycle
CREATE TABLE IF NOT EXISTS `credit_cycle` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `subscription_id`     BIGINT UNSIGNED NOT NULL,
  `cycle_start`         DATETIME(0)     NOT NULL,
  `cycle_end`           DATETIME(0)     NOT NULL,
  `credits_granted`     INT             NOT NULL DEFAULT 0,
  `credits_remaining`   INT             NOT NULL DEFAULT 0,
  `created_at`          DATETIME(0)     NOT NULL,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_cycle_user_start` (`user_id`, `cycle_start`),
  KEY `idx_cycle_user_end` (`user_id`, `cycle_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='月度积分周期，懒创建';

-- §2.4 user_booster_balance
CREATE TABLE IF NOT EXISTS `user_booster_balance` (
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `credits_remaining`   BIGINT          NOT NULL DEFAULT 0,
  `updated_at`          DATETIME(0)     NOT NULL,
  PRIMARY KEY (`user_id`),
  KEY `idx_booster_updated_at` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='加量包余额，永不过期、单用户单行';

-- §2.5 membership_event
CREATE TABLE IF NOT EXISTS `membership_event` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`             BIGINT UNSIGNED NOT NULL,
  `event_type`          ENUM('trial_granted','sub_granted','sub_renewed','booster_granted') NOT NULL,
  `product_type`        ENUM('trial','monthly','booster') NOT NULL,
  `months`              TINYINT UNSIGNED         DEFAULT NULL,
  `quantity`            SMALLINT UNSIGNED        DEFAULT NULL,
  `amount_cents`        BIGINT          NOT NULL DEFAULT 0,
  `source`              ENUM('self_purchase','b2b_grant') NOT NULL,
  `granter_user_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `idempotency_key`     VARCHAR(64)              DEFAULT NULL,
  `subscription_id`     BIGINT UNSIGNED          DEFAULT NULL,
  `occurred_at`         DATETIME(0)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_event_idempotency_key` (`idempotency_key`),
  KEY `idx_event_user_occurred` (`user_id`, `occurred_at`),
  KEY `idx_event_granter_occurred` (`granter_user_id`, `occurred_at`),
  KEY `idx_event_type_occurred` (`event_type`, `occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='会员事件流水，append-only，B2B 账单唯一数据源';

-- ROLLBACK（仅供 ops 应急执行；本文件不主动 DROP）
-- DROP TABLE IF EXISTS `membership_event`;
-- DROP TABLE IF EXISTS `user_booster_balance`;
-- DROP TABLE IF EXISTS `credit_cycle`;
-- DROP TABLE IF EXISTS `trial_grant`;
-- DROP TABLE IF EXISTS `subscription`;

-- Add user_type_multiplier to credit_reservation (snapshot at reserve time)
ALTER TABLE credit_reservation
    ADD COLUMN user_type_multiplier DECIMAL(5,2) NOT NULL DEFAULT 1.00
    COMMENT 'User-type credit burn-rate multiplier snapshotted at Reserve time. 1.00 = no discount.';

-- New config table for per-user-type credit multipliers (admin-configurable)
CREATE TABLE IF NOT EXISTS credit_user_type_config (
    user_type   VARCHAR(30)    NOT NULL PRIMARY KEY COMMENT 'trial | subscription | ...',
    credit_multiplier DECIMAL(5,2) NOT NULL DEFAULT 1.00 COMMENT 'Credits burn-rate multiplier. <1 = slower burn.',
    description VARCHAR(200)   NOT NULL DEFAULT '' COMMENT 'Human-readable note for admins',
    is_active   TINYINT(1)     NOT NULL DEFAULT 1,
    created_at  DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Per-user-type credit burn-rate multipliers';

-- Seed: trial users burn credits at 0.5× rate
INSERT INTO credit_user_type_config (user_type, credit_multiplier, description, is_active)
VALUES ('trial', 0.50, 'Trial users burn credits at half rate to encourage product exploration before converting to subscription', 1)
ON DUPLICATE KEY UPDATE credit_multiplier = VALUES(credit_multiplier), description = VALUES(description), is_active = VALUES(is_active);

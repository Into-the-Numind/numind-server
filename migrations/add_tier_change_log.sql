CREATE TABLE IF NOT EXISTS tier_change_log (
  id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  parent_user_id   INT UNSIGNED NOT NULL,
  sub_user_id      INT UNSIGNED NOT NULL,
  old_tier         VARCHAR(20) NOT NULL,
  new_tier         VARCHAR(20) NOT NULL,
  months           INT NOT NULL DEFAULT 1,
  old_tier_expires DATETIME NULL,
  new_tier_expires DATETIME NOT NULL,
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_parent (parent_user_id),
  INDEX idx_sub    (sub_user_id),
  INDEX idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

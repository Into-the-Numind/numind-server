-- 功能权限表（通用，可扩展到其他功能）
-- 白名单模式：子用户有记录则按白名单，无记录则默认允许
CREATE TABLE IF NOT EXISTS user_feature_permission (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    parent_user_id BIGINT UNSIGNED NOT NULL,
    sub_user_id BIGINT UNSIGNED NOT NULL,
    feature_key VARCHAR(64) NOT NULL,
    INDEX idx_parent_sub (parent_user_id, sub_user_id),
    INDEX idx_sub_feature (sub_user_id, feature_key),
    INDEX idx_deleted_at (deleted_at),
    UNIQUE INDEX idx_sub_feature_unique (sub_user_id, feature_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

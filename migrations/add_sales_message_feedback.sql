-- 创建消息反馈表（点赞/点踩）
CREATE TABLE IF NOT EXISTS `sales_message_feedback` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `created_at` DATETIME(3) DEFAULT NULL,
    `updated_at` DATETIME(3) DEFAULT NULL,
    `deleted_at` DATETIME(3) DEFAULT NULL,
    `message_id` BIGINT UNSIGNED NOT NULL,
    `user_id` BIGINT UNSIGNED NOT NULL,
    `rating` INT NOT NULL DEFAULT 0,
    `comment` TEXT,
    `trace_id` VARCHAR(255) DEFAULT '',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_feedback_message_id` (`message_id`),
    KEY `idx_feedback_user_id` (`user_id`),
    KEY `idx_feedback_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

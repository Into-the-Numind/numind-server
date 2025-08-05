-- 创建对话会话表
CREATE TABLE IF NOT EXISTS `chat_session` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  `deleted_at` datetime(3) DEFAULT NULL,
  `user_id` bigint unsigned NOT NULL,
  `title` varchar(255) DEFAULT NULL,
  `status` varchar(20) DEFAULT 'active',
  `message_count` int DEFAULT 0,
  PRIMARY KEY (`id`),
  KEY `idx_chat_session_deleted_at` (`deleted_at`),
  KEY `idx_chat_session_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 创建对话消息表
CREATE TABLE IF NOT EXISTS `chat_message` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  `deleted_at` datetime(3) DEFAULT NULL,
  `session_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `role` varchar(20) NOT NULL,
  `content` text NOT NULL,
  `status` varchar(20) DEFAULT 'sent',
  PRIMARY KEY (`id`),
  KEY `idx_chat_message_deleted_at` (`deleted_at`),
  KEY `idx_chat_message_session_id` (`session_id`),
  KEY `idx_chat_message_user_id` (`user_id`),
  CONSTRAINT `fk_chat_message_session` FOREIGN KEY (`session_id`) REFERENCES `chat_session` (`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_chat_message_user` FOREIGN KEY (`user_id`) REFERENCES `user` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 为chat_session表添加外键约束
ALTER TABLE `chat_session` 
ADD CONSTRAINT `fk_chat_session_user` 
FOREIGN KEY (`user_id`) REFERENCES `user` (`id`) ON DELETE CASCADE; 
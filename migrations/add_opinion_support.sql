-- 新增观点赛道表
CREATE TABLE IF NOT EXISTS `opinion_track` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  `deleted_at` datetime(3) DEFAULT NULL,
  `slug` varchar(50) NOT NULL,
  `name` varchar(100) NOT NULL,
  `description` varchar(512) DEFAULT '',
  `is_enabled` tinyint(1) DEFAULT 1,
  `sort_order` int DEFAULT 0,
  `doc_id` bigint unsigned DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_opinion_track_slug` (`slug`),
  KEY `idx_opinion_track_deleted_at` (`deleted_at`),
  KEY `idx_opinion_track_doc_id` (`doc_id`),
  KEY `idx_opinion_track_enabled_order` (`is_enabled`, `sort_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 扩展 sales_session 表：新增观点库字段
ALTER TABLE `sales_session`
  ADD COLUMN IF NOT EXISTS `opinion_doc_ids` text COMMENT '用户上传观点文档 JSON array',
  ADD COLUMN IF NOT EXISTS `opinion_track_ids` text COMMENT '系统赛道 ID JSON array';

-- 扩展 knowledge_document 表：新增系统文档标记
ALTER TABLE `knowledge_document`
  ADD COLUMN IF NOT EXISTS `is_system` tinyint(1) DEFAULT 0 COMMENT '是否系统文档';

-- 注意：is_system 索引已由 GORM AutoMigrate 通过 model tag 自动创建

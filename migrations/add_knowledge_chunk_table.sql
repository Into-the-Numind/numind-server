-- 创建 knowledge_chunk 表用于存储知识切片
-- 避免每次查询向量数据库产生费用，提升查询性能和系统稳定性

CREATE TABLE IF NOT EXISTS `knowledge_chunk` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `document_id` int(10) unsigned NOT NULL COMMENT 'Foreign key to knowledge_document',
  `user_id` int(10) unsigned NOT NULL COMMENT '用户ID，用于数据隔离',
  `sequence` int NOT NULL COMMENT '切片在文档中的顺序',
  `content` text COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '切片内容',
  `summary` text COLLATE utf8mb4_unicode_ci COMMENT 'AI生成的摘要',
  `source_ref` varchar(255) COLLATE utf8mb4_unicode_ci COMMENT '来源引用（如页码）',
  `tags` varchar(512) COLLATE utf8mb4_unicode_ci COMMENT '标签（逗号分隔）',
  `vector_id` varchar(255) COLLATE utf8mb4_unicode_ci COMMENT '向量数据库中的ID',
  `embedding_status` varchar(20) COLLATE utf8mb4_unicode_ci DEFAULT 'PENDING' COMMENT 'PENDING/COMPLETED/FAILED',
  `metadata` text COLLATE utf8mb4_unicode_ci COMMENT 'JSON格式的额外元数据',
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_knowledge_chunk_document_id` (`document_id`),
  KEY `idx_knowledge_chunk_user_id` (`user_id`),
  KEY `idx_user_doc` (`user_id`, `document_id`),
  KEY `idx_vector_id` (`vector_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='知识切片表，存储文档切片的详细内容';

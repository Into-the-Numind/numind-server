-- 为 knowledge_document 表添加新字段
-- 执行时间：2026-01-23

-- 添加字段（如果不存在）
ALTER TABLE knowledge_document
ADD COLUMN IF NOT EXISTS description VARCHAR(1024) DEFAULT '' COMMENT '文档描述',
ADD COLUMN IF NOT EXISTS file_type VARCHAR(20) DEFAULT '' COMMENT '文件类型',
ADD COLUMN IF NOT EXISTS file_size BIGINT DEFAULT 0 COMMENT '文件大小（字节）',
ADD COLUMN IF NOT EXISTS chunk_count INT DEFAULT 0 COMMENT '切片数量',
ADD COLUMN IF NOT EXISTS type VARCHAR(20) DEFAULT 'FACT' COMMENT '文档类型: FACT, STRATEGY, CASE',
ADD COLUMN IF NOT EXISTS is_enabled TINYINT(1) DEFAULT 1 COMMENT '是否启用';

-- 为 is_enabled 添加索引（用于IsEnabled过滤优化）
CREATE INDEX IF NOT EXISTS idx_knowledge_document_is_enabled ON knowledge_document(is_enabled);

-- 验证表结构
DESCRIBE knowledge_document;

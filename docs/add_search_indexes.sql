-- 添加搜索相关索引的数据库迁移脚本
-- 执行此脚本以优化关键词搜索性能

-- 1. 为Title和Tags字段添加复合索引
-- 这将显著提升基于标题和标签的搜索性能
CREATE INDEX IF NOT EXISTS idx_title_tags ON book(title, tags);

-- 2. 为Status字段添加索引
-- 这将优化状态过滤查询
CREATE INDEX IF NOT EXISTS idx_status ON book(status);

-- 3. 为UserID字段添加索引（如果不存在）
-- 这将优化按用户查询的性能
CREATE INDEX IF NOT EXISTS idx_user_id ON book(user_id);

-- 4. 为CategoryID字段添加索引（如果不存在）
-- 这将优化按分类查询的性能
CREATE INDEX IF NOT EXISTS idx_category_id ON book(category_id);

-- 5. 为创建时间添加索引（如果不存在）
-- 这将优化按时间排序的查询
CREATE INDEX IF NOT EXISTS idx_created_at ON book(created_at);

-- 验证索引创建
SELECT 
    index_name,
    column_name,
    index_type
FROM information_schema.statistics 
WHERE table_schema = DATABASE() 
    AND table_name = 'book' 
    AND index_name LIKE 'idx_%'
ORDER BY index_name, seq_in_index;

-- 查看表的索引信息
SHOW INDEX FROM book;

-- 修复JSON索引问题的SQL脚本
-- 解决 "JSON column 'keywords' supports indexing only via generated columns" 错误

-- 1. 检查当前表结构
DESCRIBE book;

-- 2. 删除有问题的索引（如果存在）
DROP INDEX IF EXISTS idx_keywords ON book;

-- 3. 添加KeywordsText字段（如果不存在）
-- 这个字段用于存储关键词的文本表示，支持索引
ALTER TABLE book ADD COLUMN IF NOT EXISTS keywords_text VARCHAR(500) AFTER keywords;

-- 4. 为KeywordsText字段创建索引（使用前缀索引避免长度问题）
CREATE INDEX IF NOT EXISTS idx_keywords_text ON book(keywords_text(200));

-- 5. 更新现有数据，将JSON格式的关键词转换为文本格式
-- 如果keywords字段有数据，将其转换为keywords_text
UPDATE book 
SET keywords_text = JSON_UNQUOTE(JSON_EXTRACT(keywords, '$'))
WHERE keywords IS NOT NULL AND keywords != '[]' AND keywords != 'null';

-- 6. 验证修复结果
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';

-- 7. 检查字段数据
SELECT 
    id,
    title,
    keywords,
    keywords_text,
    LENGTH(keywords_text) as text_length
FROM book 
WHERE keywords IS NOT NULL 
LIMIT 10;

-- 8. 创建触发器来自动同步keywords和keywords_text字段
-- 注意：这个触发器需要在应用层实现，因为MySQL的JSON触发器支持有限

-- 9. 验证表结构
SHOW CREATE TABLE book;

-- 说明：
-- 1. Keywords字段保持JSON类型，用于存储结构化数据
-- 2. KeywordsText字段为VARCHAR类型，用于索引和搜索
-- 3. 应用层负责同步这两个字段
-- 4. 搜索时可以使用KeywordsText字段进行LIKE查询
-- 5. 匹配时仍然使用Keywords字段的JSON数据

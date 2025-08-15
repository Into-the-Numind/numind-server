-- 全面的索引解决方案
-- 解决JSON索引和键长度问题

-- 1. 检查当前数据库字符集
SELECT 
    SCHEMA_NAME,
    DEFAULT_CHARACTER_SET_NAME,
    DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = DATABASE();

-- 2. 检查book表结构
DESCRIBE book;

-- 3. 检查现有索引
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';

-- 4. 删除有问题的索引（如果存在）
DROP INDEX IF EXISTS idx_keywords ON book;
DROP INDEX IF EXISTS idx_keywords_text ON book;

-- 5. 添加KeywordsText字段（如果不存在）
-- 使用合理的长度，避免索引键长度问题
ALTER TABLE book ADD COLUMN IF NOT EXISTS keywords_text VARCHAR(500) AFTER keywords;

-- 6. 创建前缀索引（避免键长度超限）
-- 使用前200个字符创建索引，在UTF8MB4下约为800字节，远低于3072字节限制
CREATE INDEX IF NOT EXISTS idx_keywords_text ON book(keywords_text(200));

-- 7. 创建复合索引（如果需要）
-- 结合title和keywords_text进行搜索优化
CREATE INDEX IF NOT EXISTS idx_title_keywords ON book(title(100), keywords_text(100));

-- 8. 更新现有数据
-- 将JSON格式的关键词转换为文本格式
UPDATE book 
SET keywords_text = JSON_UNQUOTE(JSON_EXTRACT(keywords, '$'))
WHERE keywords IS NOT NULL AND keywords != '[]' AND keywords != 'null';

-- 9. 验证索引创建结果
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';

-- 10. 检查索引键长度
SELECT 
    TABLE_NAME,
    INDEX_NAME,
    COLUMN_NAME,
    SUB_PART as PREFIX_LENGTH,
    CARDINALITY
FROM information_schema.STATISTICS 
WHERE TABLE_SCHEMA = DATABASE() 
    AND TABLE_NAME = 'book' 
    AND INDEX_NAME LIKE '%keywords%';

-- 11. 测试索引效果
-- 检查索引是否正常工作
EXPLAIN SELECT * FROM book WHERE keywords_text LIKE '%美食%';

-- 说明：
-- 1. 使用VARCHAR(500)避免字段过长
-- 2. 使用前缀索引(200)避免键长度超限
-- 3. 在UTF8MB4字符集下，200字符约为800字节，远低于3072字节限制
-- 4. 前缀索引仍然能提供良好的搜索性能
-- 5. 复合索引可以进一步优化搜索查询

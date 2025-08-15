-- 数据库字符集修复脚本
-- 解决 utf8mb3_general_ci 到 utf8mb4_0900_ai_ci 的转换问题
-- 执行此脚本前请先备份数据库

-- 1. 检查当前数据库字符集
SELECT 
    SCHEMA_NAME,
    DEFAULT_CHARACTER_SET_NAME,
    DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = 'numind';

-- 2. 检查当前表字符集
SELECT 
    TABLE_NAME,
    TABLE_COLLATION,
    TABLE_TYPE
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind'
ORDER BY TABLE_NAME;

-- 3. 检查chat_message表的字符集
SELECT 
    COLUMN_NAME,
    CHARACTER_SET_NAME,
    COLLATION_NAME,
    DATA_TYPE
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = 'numind' 
    AND TABLE_NAME = 'chat_message'
ORDER BY ORDINAL_POSITION;

-- 4. 修复数据库字符集
ALTER DATABASE numind CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 5. 修复chat_message表的字符集
ALTER TABLE chat_message CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 6. 修复chat_session表的字符集
ALTER TABLE chat_session CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 7. 修复其他相关表的字符集
ALTER TABLE book CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE card CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE category CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE user CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE image CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE template CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE feedback CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE order_m CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE payment CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE article CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE admin CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE account_record CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 8. 特别修复content字段的字符集
ALTER TABLE chat_message MODIFY COLUMN content TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 9. 验证修复结果
SELECT 
    SCHEMA_NAME,
    DEFAULT_CHARACTER_SET_NAME,
    DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = 'numind';

SELECT 
    TABLE_NAME,
    TABLE_COLLATION
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind'
ORDER BY TABLE_NAME;

SELECT 
    COLUMN_NAME,
    CHARACTER_SET_NAME,
    COLLATION_NAME
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = 'numind' 
    AND TABLE_NAME = 'chat_message'
    AND COLUMN_NAME = 'content';

-- 10. 创建索引优化（如果需要）
-- 为chat_message表添加索引以提升查询性能
CREATE INDEX IF NOT EXISTS idx_chat_message_session_user ON chat_message(session_id, user_id);
CREATE INDEX IF NOT EXISTS idx_chat_message_created_at ON chat_message(created_at);

-- 11. 检查是否有其他需要修复的表
SELECT DISTINCT
    TABLE_NAME,
    TABLE_COLLATION
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind'
    AND TABLE_COLLATION NOT LIKE 'utf8mb4%'
ORDER BY TABLE_NAME;

-- 12. 如果还有其他表需要修复，可以批量执行
-- 注意：以下命令会修复所有非utf8mb4的表，请谨慎使用
/*
SELECT CONCAT('ALTER TABLE ', TABLE_NAME, ' CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;') as fix_command
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind'
    AND TABLE_COLLATION NOT LIKE 'utf8mb4%';
*/

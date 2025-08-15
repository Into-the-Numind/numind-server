-- 快速修复数据库字符集
-- 解决 utf8mb3_general_ci 到 utf8mb4_0900_ai_ci 的转换问题
-- 执行前请确保已备份数据库

-- 1. 修复数据库字符集
ALTER DATABASE numind CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 2. 修复chat_message表（这是出错的主要表）
ALTER TABLE chat_message CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 3. 特别修复content字段
ALTER TABLE chat_message MODIFY COLUMN content TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 4. 修复chat_session表
ALTER TABLE chat_session CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 5. 修复其他相关表
ALTER TABLE book CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE user CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE card CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE category CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 6. 验证修复结果
SELECT 'Database charset:' as info, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME 
FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'numind';

SELECT 'Table charset:' as info, TABLE_NAME, TABLE_COLLATION 
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind' AND TABLE_NAME IN ('chat_message', 'chat_session', 'book', 'user');

SELECT 'Content field charset:' as info, CHARACTER_SET_NAME, COLLATION_NAME 
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = 'numind' AND TABLE_NAME = 'chat_message' AND COLUMN_NAME = 'content';

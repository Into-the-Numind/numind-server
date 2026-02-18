-- 修复知识库文档数据脚本
-- 用途: 删除或修复 name 字段为空或错误的文档记录

USE numind_dev;

-- 1. 查看问题文档
SELECT
    id,
    user_id,
    name,
    CHAR_LENGTH(name) as name_length,
    file_path,
    status,
    created_at
FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2  -- name 长度 <= 2 很可能是错误数据（如 "1", "66"）
ORDER BY id DESC;

-- 2. 删除这些错误记录（执行前请确认上面的查询结果）
-- 取消下面的注释以执行删除
-- DELETE FROM knowledge_document
-- WHERE name = ''
--    OR name IS NULL
--    OR CHAR_LENGTH(name) <= 2;

-- 3. 同时清理关联的 chunks 数据
-- DELETE FROM knowledge_chunk
-- WHERE document_id IN (
--     SELECT id FROM knowledge_document
--     WHERE name = '' OR name IS NULL OR CHAR_LENGTH(name) <= 2
-- );

-- 4. 查看修复后的数据
SELECT COUNT(*) as total_docs FROM knowledge_document;

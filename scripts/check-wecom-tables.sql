-- =====================================================
-- wecom 数据表检查脚本
-- 用于确认表是否存在及数据量
-- =====================================================

-- 检查表是否存在
SELECT 
    TABLE_NAME as table_name,
    TABLE_ROWS as estimated_rows,
    ROUND(DATA_LENGTH / 1024 / 1024, 2) as data_size_mb
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = DATABASE() 
    AND TABLE_NAME IN ('wecom_users', 'wecom_messages', 'wecom_cursors', 'wecom_bind_codes')
ORDER BY TABLE_NAME;

-- 显示各表具体数据量
SELECT 'wecom_users' as table_name, COUNT(*) as exact_count FROM wecom_users
UNION ALL
SELECT 'wecom_messages', COUNT(*) FROM wecom_messages
UNION ALL
SELECT 'wecom_cursors', COUNT(*) FROM wecom_cursors
UNION ALL
SELECT 'wecom_bind_codes', COUNT(*) FROM wecom_bind_codes;

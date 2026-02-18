-- =====================================================
-- wecom 数据表删除脚本
-- ⚠️ 警告: 此操作不可逆，请确保已执行备份
-- 执行时间: 2026-02-18
-- =====================================================

-- 关闭外键检查（防止外键约束导致删除失败）
SET FOREIGN_KEY_CHECKS = 0;

-- 删除原表
DROP TABLE IF EXISTS wecom_bind_codes;
DROP TABLE IF EXISTS wecom_cursors;
DROP TABLE IF EXISTS wecom_messages;
DROP TABLE IF EXISTS wecom_users;

-- 恢复外键检查
SET FOREIGN_KEY_CHECKS = 1;

-- 验证删除结果
SELECT 
    'wecom_users' as table_name, 
    CASE 
        WHEN COUNT(*) = 0 THEN '已删除'
        ELSE '仍然存在'
    END as status
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wecom_users'
UNION ALL
SELECT 
    'wecom_messages',
    CASE 
        WHEN COUNT(*) = 0 THEN '已删除'
        ELSE '仍然存在'
    END
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wecom_messages'
UNION ALL
SELECT 
    'wecom_cursors',
    CASE 
        WHEN COUNT(*) = 0 THEN '已删除'
        ELSE '仍然存在'
    END
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wecom_cursors'
UNION ALL
SELECT 
    'wecom_bind_codes',
    CASE 
        WHEN COUNT(*) = 0 THEN '已删除'
        ELSE '仍然存在'
    END
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wecom_bind_codes';

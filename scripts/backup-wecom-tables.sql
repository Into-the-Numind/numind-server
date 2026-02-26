-- =====================================================
-- wecom 数据表备份脚本
-- 执行时间: 2026-02-18
-- 备份表: wecom_users, wecom_messages, wecom_cursors, wecom_bind_codes
-- =====================================================

-- 创建备份表（如果不存在则创建，存在则先删除）
DROP TABLE IF EXISTS wecom_users_backup;
DROP TABLE IF EXISTS wecom_messages_backup;
DROP TABLE IF EXISTS wecom_cursors_backup;
DROP TABLE IF EXISTS wecom_bind_codes_backup;

-- 复制表结构和数据
CREATE TABLE wecom_users_backup AS SELECT * FROM wecom_users;
CREATE TABLE wecom_messages_backup AS SELECT * FROM wecom_messages;
CREATE TABLE wecom_cursors_backup AS SELECT * FROM wecom_cursors;
CREATE TABLE wecom_bind_codes_backup AS SELECT * FROM wecom_bind_codes;

-- 添加备份时间戳
ALTER TABLE wecom_users_backup ADD COLUMN backup_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE wecom_messages_backup ADD COLUMN backup_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE wecom_cursors_backup ADD COLUMN backup_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE wecom_bind_codes_backup ADD COLUMN backup_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP;

-- 显示备份结果
SELECT 
    'wecom_users_backup' as table_name, 
    COUNT(*) as record_count 
FROM wecom_users_backup
UNION ALL
SELECT 
    'wecom_messages_backup', 
    COUNT(*) 
FROM wecom_messages_backup
UNION ALL
SELECT 
    'wecom_cursors_backup', 
    COUNT(*) 
FROM wecom_cursors_backup
UNION ALL
SELECT 
    'wecom_bind_codes_backup', 
    COUNT(*) 
FROM wecom_bind_codes_backup;

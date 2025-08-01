-- 修复用户表中空username的SQL脚本
-- 执行前请备份数据库

-- 1. 查看当前有问题的记录
SELECT id, open_id, username, created_at 
FROM user 
WHERE username = '' OR username IS NULL;

-- 2. 更新空username的记录，使用open_id生成唯一username
UPDATE user 
SET username = CONCAT('user_', open_id)
WHERE username = '' OR username IS NULL;

-- 3. 验证修复结果
SELECT id, open_id, username, created_at 
FROM user 
WHERE username LIKE 'user_%'
ORDER BY created_at DESC
LIMIT 10;

-- 4. 检查是否还有重复的username
SELECT username, COUNT(*) as count
FROM user 
GROUP BY username 
HAVING count > 1;

-- 5. 如果需要，为重复的username添加后缀
-- 注意：这个查询会显示重复的username，需要手动处理
SELECT username, GROUP_CONCAT(id) as user_ids
FROM user 
GROUP BY username 
HAVING COUNT(*) > 1; 
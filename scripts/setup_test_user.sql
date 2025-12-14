-- 设置Web登录测试用户
-- 密码为明文: admin123456

-- 方式1: 更新现有用户（根据ID更新）
-- 请将 <USER_ID> 替换为实际的用户ID
-- UPDATE user SET password = 'admin123456', username = 'admin' WHERE id = <USER_ID>;

-- 方式2: 更新现有用户（根据用户名更新）
UPDATE user SET password = 'admin123456' WHERE username = 'admin';

-- 方式3: 如果用户不存在，创建新的测试用户
INSERT INTO user (
    username, 
    password, 
    nickname, 
    is_admin,
    membership_type,
    created_at, 
    updated_at
) 
SELECT 
    'admin',
    'admin123456',
    '管理员',
    1,
    'subscription',
    NOW(),
    NOW()
FROM DUAL
WHERE NOT EXISTS (
    SELECT 1 FROM user WHERE username = 'admin'
);

-- 验证用户是否设置成功
SELECT id, username, nickname, is_admin, membership_type, created_at 
FROM user 
WHERE username = 'admin';

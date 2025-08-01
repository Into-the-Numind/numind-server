#!/bin/bash

# 数据库修复脚本
# 用于修复用户表中的username重复问题

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}数据库修复脚本${NC}"
echo "================"

# 数据库配置
DB_HOST="49.233.219.254"
DB_PORT="13306"
DB_USER="root"
DB_PASS="Numind2025"
DB_NAME="numind"

echo -e "\n${YELLOW}检查数据库连接...${NC}"

# 测试数据库连接
if ! mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -e "SELECT 1;" > /dev/null 2>&1; then
    echo -e "${RED}错误: 无法连接到数据库${NC}"
    exit 1
fi

echo -e "${GREEN}✓ 数据库连接成功${NC}"

echo -e "\n${YELLOW}检查有问题的用户记录...${NC}"

# 查看空username的记录
EMPTY_USERS=$(mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -s -N -e "
SELECT COUNT(*) FROM user WHERE username = '' OR username IS NULL;
")

if [ "$EMPTY_USERS" -gt 0 ]; then
    echo -e "${YELLOW}发现 $EMPTY_USERS 条空username的记录${NC}"
    
    echo -e "\n${YELLOW}修复空username记录...${NC}"
    
    # 修复空username的记录
    mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -e "
    UPDATE user 
    SET username = CONCAT('user_', open_id)
    WHERE username = '' OR username IS NULL;
    "
    
    echo -e "${GREEN}✓ 修复完成${NC}"
else
    echo -e "${GREEN}✓ 没有发现空username的记录${NC}"
fi

echo -e "\n${YELLOW}检查重复username...${NC}"

# 检查重复username
DUPLICATE_USERS=$(mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -s -N -e "
SELECT COUNT(*) FROM (
    SELECT username, COUNT(*) as count
    FROM user 
    GROUP BY username 
    HAVING count > 1
) as duplicates;
")

if [ "$DUPLICATE_USERS" -gt 0 ]; then
    echo -e "${YELLOW}发现 $DUPLICATE_USERS 组重复username${NC}"
    
    echo -e "\n${YELLOW}重复的username列表:${NC}"
    mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -e "
    SELECT username, COUNT(*) as count
    FROM user 
    GROUP BY username 
    HAVING count > 1
    ORDER BY count DESC;
    "
    
    echo -e "\n${YELLOW}建议手动处理重复的username${NC}"
else
    echo -e "${GREEN}✓ 没有发现重复username${NC}"
fi

echo -e "\n${YELLOW}验证修复结果...${NC}"

# 显示最近的用户记录
echo -e "\n${GREEN}最近的用户记录:${NC}"
mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -e "
SELECT id, open_id, username, created_at 
FROM user 
ORDER BY created_at DESC 
LIMIT 5;
"

echo -e "\n${GREEN}✓ 数据库修复完成${NC}" 
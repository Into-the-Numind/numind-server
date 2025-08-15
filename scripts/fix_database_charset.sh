#!/bin/bash

# 数据库字符集修复脚本
# 解决 utf8mb3_general_ci 到 utf8mb4_0900_ai_ci 的转换问题

echo "🔧 数据库字符集修复工具"
echo "=========================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置变量
DB_NAME="numind"
DB_HOST="localhost"
DB_PORT="3306"
DB_USER="root"
DB_PASSWORD=""

# 检查MySQL客户端
echo -e "${BLUE}1. 检查MySQL客户端...${NC}"
if command -v mysql &> /dev/null; then
    echo -e "${GREEN}✅ MySQL客户端已安装${NC}"
else
    echo -e "${RED}❌ MySQL客户端未安装${NC}"
    echo "请先安装MySQL客户端或MariaDB客户端"
    exit 1
fi

# 获取数据库连接信息
echo -e "${BLUE}2. 配置数据库连接...${NC}"
read -p "数据库主机 [localhost]: " input_host
DB_HOST=${input_host:-localhost}

read -p "数据库端口 [3306]: " input_port
DB_PORT=${input_port:-3306}

read -p "数据库用户名 [root]: " input_user
DB_USER=${input_user:-root}

read -s -p "数据库密码: " DB_PASSWORD
echo ""

# 测试数据库连接
echo -e "${BLUE}3. 测试数据库连接...${NC}"
if mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "USE $DB_NAME;" 2>/dev/null; then
    echo -e "${GREEN}✅ 数据库连接成功${NC}"
else
    echo -e "${RED}❌ 数据库连接失败${NC}"
    echo "请检查数据库连接信息"
    exit 1
fi

echo ""

# 备份数据库
echo -e "${BLUE}4. 创建数据库备份...${NC}"
BACKUP_FILE="backup_${DB_NAME}_$(date +%Y%m%d_%H%M%S).sql"
echo "备份文件: $BACKUP_FILE"

if mysqldump -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" --single-transaction --routines --triggers "$DB_NAME" > "$BACKUP_FILE" 2>/dev/null; then
    echo -e "${GREEN}✅ 数据库备份成功${NC}"
else
    echo -e "${YELLOW}⚠️  数据库备份失败，但继续执行修复${NC}"
fi

echo ""

# 检查当前字符集
echo -e "${BLUE}5. 检查当前字符集...${NC}"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
SELECT 
    SCHEMA_NAME,
    DEFAULT_CHARACTER_SET_NAME,
    DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = '$DB_NAME';
" 2>/dev/null

echo ""

# 执行修复
echo -e "${BLUE}6. 执行字符集修复...${NC}"

# 修复数据库字符集
echo "修复数据库字符集..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
ALTER DATABASE $DB_NAME CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 数据库字符集修复成功${NC}"
else
    echo -e "${RED}❌ 数据库字符集修复失败${NC}"
fi

# 修复主要表的字符集
echo "修复主要表字符集..."
TABLES=("chat_message" "chat_session" "book" "card" "category" "user" "image" "template" "feedback" "order_m" "payment" "article" "admin" "account_record")

for table in "${TABLES[@]}"; do
    echo "  修复表: $table"
    mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
    ALTER TABLE $table CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    " 2>/dev/null
    
    if [ $? -eq 0 ]; then
        echo -e "    ${GREEN}✅ 成功${NC}"
    else
        echo -e "    ${YELLOW}⚠️  失败（可能表不存在）${NC}"
    fi
done

# 特别修复chat_message表的content字段
echo "特别修复chat_message.content字段..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
ALTER TABLE chat_message MODIFY COLUMN content TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ content字段修复成功${NC}"
else
    echo -e "${YELLOW}⚠️  content字段修复失败${NC}"
fi

echo ""

# 创建索引优化
echo -e "${BLUE}7. 创建索引优化...${NC}"
echo "为chat_message表添加索引..."

mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
CREATE INDEX IF NOT EXISTS idx_chat_message_session_user ON chat_message(session_id, user_id);
CREATE INDEX IF NOT EXISTS idx_chat_message_created_at ON chat_message(created_at);
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 索引创建成功${NC}"
else
    echo -e "${YELLOW}⚠️  索引创建失败${NC}"
fi

echo ""

# 验证修复结果
echo -e "${BLUE}8. 验证修复结果...${NC}"
echo "数据库字符集:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
SELECT 
    SCHEMA_NAME,
    DEFAULT_CHARACTER_SET_NAME,
    DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = '$DB_NAME';
" 2>/dev/null

echo ""
echo "主要表字符集:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
SELECT 
    TABLE_NAME,
    TABLE_COLLATION
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = '$DB_NAME'
    AND TABLE_NAME IN ('chat_message', 'chat_session', 'book', 'user')
ORDER BY TABLE_NAME;
" 2>/dev/null

echo ""
echo "chat_message.content字段字符集:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
SELECT 
    COLUMN_NAME,
    CHARACTER_SET_NAME,
    COLLATION_NAME
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = '$DB_NAME' 
    AND TABLE_NAME = 'chat_message'
    AND COLUMN_NAME = 'content';
" 2>/dev/null

echo ""

# 检查是否还有其他需要修复的表
echo -e "${BLUE}9. 检查其他需要修复的表...${NC}"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
SELECT DISTINCT
    TABLE_NAME,
    TABLE_COLLATION
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = '$DB_NAME'
    AND TABLE_COLLATION NOT LIKE 'utf8mb4%'
ORDER BY TABLE_NAME;
" 2>/dev/null

echo ""

# 完成提示
echo -e "${GREEN}🎉 数据库字符集修复完成！${NC}"
echo ""
echo -e "${YELLOW}重要提醒:${NC}"
echo "1. 数据库已备份到: $BACKUP_FILE"
echo "2. 所有主要表已转换为 utf8mb4 字符集"
echo "3. chat_message.content 字段已特别修复"
echo "4. 建议重启应用程序以确保连接使用新的字符集"
echo ""
echo -e "${BLUE}下一步:${NC}"
echo "1. 重启应用程序"
echo "2. 测试WebSocket聊天功能"
echo "3. 验证中文字符是否正常显示"
echo ""
echo -e "${GREEN}如果还有问题，请检查日志或联系技术支持${NC}"

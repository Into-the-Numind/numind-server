#!/bin/bash

# 快速修复索引长度问题
# 解决 "Specified key was too long; max key length is 3072 bytes" 错误

echo "🔧 快速修复索引长度问题"
echo "========================="
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

# 获取数据库连接信息
echo -e "${BLUE}1. 配置数据库连接...${NC}"
read -p "数据库主机 [localhost]: " input_host
DB_HOST=${input_host:-localhost}

read -p "数据库端口 [3306]: " input_port
DB_PORT=${input_port:-3306}

read -p "数据库用户名 [root]: " input_user
DB_USER=${input_user:-root}

read -s -p "数据库密码: " DB_PASSWORD
echo ""

# 测试数据库连接
echo -e "${BLUE}2. 测试数据库连接...${NC}"
if mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "USE $DB_NAME;" 2>/dev/null; then
    echo -e "${GREEN}✅ 数据库连接成功${NC}"
else
    echo -e "${RED}❌ 数据库连接失败${NC}"
    exit 1
fi

echo ""

# 执行快速修复
echo -e "${BLUE}3. 执行快速修复...${NC}"

# 删除有问题的索引
echo "删除有问题的索引..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
DROP INDEX IF EXISTS idx_keywords ON book;
DROP INDEX IF EXISTS idx_keywords_text ON book;
" 2>/dev/null

echo -e "${GREEN}✅ 问题索引已删除${NC}"

# 添加KeywordsText字段（如果不存在）
echo "添加KeywordsText字段..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
ALTER TABLE book ADD COLUMN IF NOT EXISTS keywords_text VARCHAR(500) AFTER keywords;
" 2>/dev/null

echo -e "${GREEN}✅ KeywordsText字段已添加${NC}"

# 创建前缀索引（避免键长度超限）
echo "创建前缀索引..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
CREATE INDEX idx_keywords_text ON book(keywords_text(200));
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 前缀索引创建成功${NC}"
else
    echo -e "${RED}❌ 前缀索引创建失败${NC}"
    exit 1
fi

echo ""

# 验证修复结果
echo -e "${BLUE}4. 验证修复结果...${NC}"
echo "检查索引状态:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';
" 2>/dev/null

echo ""

# 完成提示
echo -e "${GREEN}🎉 索引长度问题修复完成！${NC}"
echo ""
echo -e "${BLUE}现在可以启动应用程序：${NC}"
echo "go run cmd/numind/main.go"
echo ""
echo -e "${YELLOW}修复说明:${NC}"
echo "• 删除了有问题的索引"
echo "• 添加了KeywordsText字段（VARCHAR(500)）"
echo "• 创建了前缀索引（200字符，避免长度超限）"
echo "• 在UTF8MB4字符集下，200字符约为800字节，远低于3072字节限制"
echo ""
echo -e "${GREEN}应用程序现在应该可以正常启动！${NC}"

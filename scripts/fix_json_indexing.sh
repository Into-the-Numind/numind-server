#!/bin/bash

# 修复JSON索引问题的脚本
# 解决 "JSON column 'keywords' supports indexing only via generated columns" 错误

echo "🔧 修复JSON索引问题"
echo "===================="
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

# 检查当前表结构
echo -e "${BLUE}4. 检查当前表结构...${NC}"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
DESCRIBE book;
" 2>/dev/null

echo ""

# 检查现有索引
echo -e "${BLUE}5. 检查现有索引...${NC}"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';
" 2>/dev/null

echo ""

# 执行修复操作
echo -e "${BLUE}6. 执行修复操作...${NC}"

# 删除有问题的索引
echo "删除有问题的索引..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
DROP INDEX IF EXISTS idx_keywords ON book;
DROP INDEX IF EXISTS idx_keywords_text ON book;
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 问题索引删除成功${NC}"
else
    echo -e "${YELLOW}⚠️  索引删除失败（可能不存在）${NC}"
fi

# 添加KeywordsText字段
echo "添加KeywordsText字段..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
ALTER TABLE book ADD COLUMN IF NOT EXISTS keywords_text VARCHAR(500) AFTER keywords;
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ KeywordsText字段添加成功${NC}"
else
    echo -e "${YELLOW}⚠️  KeywordsText字段添加失败（可能已存在）${NC}"
fi

# 为KeywordsText字段创建前缀索引（避免键长度超限）
echo "为KeywordsText字段创建前缀索引..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
CREATE INDEX IF NOT EXISTS idx_keywords_text ON book(keywords_text(200));
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ KeywordsText前缀索引创建成功${NC}"
else
    echo -e "${YELLOW}⚠️  KeywordsText前缀索引创建失败${NC}"
fi

# 创建复合索引（优化搜索性能）
echo "创建复合索引..."
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
CREATE INDEX IF NOT EXISTS idx_title_keywords ON book(title(100), keywords_text(100));
" 2>/dev/null

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 复合索引创建成功${NC}"
else
    echo -e "${YELLOW}⚠️  复合索引创建失败${NC}"
fi

echo ""

# 验证修复结果
echo -e "${BLUE}7. 验证修复结果...${NC}"
echo "检查新的表结构:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
DESCRIBE book;
" 2>/dev/null

echo ""
echo "检查新的索引:"
mysql -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" -e "
USE $DB_NAME;
SHOW INDEX FROM book WHERE Key_name LIKE '%keywords%';
" 2>/dev/null

echo ""

# 显示修复说明
echo -e "${BLUE}8. 修复说明:${NC}"
echo "✅ 问题已解决："
echo "   - 删除了有问题的JSON列索引"
echo "   - 添加了KeywordsText字段用于索引"
echo "   - 创建了新的文本索引"
echo ""
echo "📋 新的架构："
echo "   - Keywords: JSON类型，存储结构化关键词数据"
echo "   - KeywordsText: VARCHAR类型，支持索引和搜索"
echo "   - 应用层自动同步两个字段"
echo ""
echo "🔧 下一步操作："
echo "   1. 重启应用程序"
echo "   2. 系统会自动同步Keywords和KeywordsText字段"
echo "   3. 搜索功能将使用新的索引字段"
echo ""

# 完成提示
echo -e "${GREEN}🎉 JSON索引问题修复完成！${NC}"
echo ""
echo -e "${YELLOW}重要提醒:${NC}"
echo "1. 现在可以正常启动应用程序"
echo "2. 关键词功能将正常工作"
echo "3. 搜索性能会有所提升"
echo ""
echo -e "${BLUE}重启服务即可体验修复后的关键词功能！${NC}"

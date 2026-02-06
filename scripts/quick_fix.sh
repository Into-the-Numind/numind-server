#!/bin/bash
#
# DOCX 解析问题快速修复脚本
#
# 使用方法: ./scripts/quick_fix.sh
#

set -e

echo "=========================================="
echo "  DOCX 解析问题快速修复"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 数据库配置（从环境变量或默认值）
DB_HOST="${DB_HOST:-localhost}"
DB_USER="${DB_USER:-root}"
DB_NAME="${DB_NAME:-numind-dev}"

echo "📋 检查数据库中的问题数据..."
echo ""

# 检查是否有 mysql 命令
if ! command -v mysql &> /dev/null; then
    echo -e "${RED}❌ mysql 命令不可用${NC}"
    echo "请先安装 MySQL 客户端"
    exit 1
fi

# 检查问题数据
echo "正在查询问题记录..."
PROBLEM_COUNT=$(mysql -h "$DB_HOST" -u "$DB_USER" -p"$MYSQL_PWD" "$DB_NAME" -sN << 'EOF'
SELECT COUNT(*)
FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2;
EOF
)

if [[ "$PROBLEM_COUNT" -eq 0 ]]; then
    echo -e "${GREEN}✅ 未发现问题数据${NC}"
    echo ""
else
    echo -e "${YELLOW}⚠️  发现 $PROBLEM_COUNT 条问题记录${NC}"
    echo ""

    # 显示问题数据
    echo "问题记录详情:"
    mysql -h "$DB_HOST" -u "$DB_USER" -p"$MYSQL_PWD" "$DB_NAME" -t << 'EOF'
SELECT
    id,
    user_id,
    name,
    CHAR_LENGTH(name) as name_len,
    SUBSTRING(file_path, 1, 50) as file_path_preview,
    status,
    created_at
FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2
ORDER BY id DESC
LIMIT 10;
EOF

    echo ""
    echo -e "${YELLOW}是否删除这些问题记录？${NC}"
    echo "注意：删除后无法恢复，建议先备份数据库"
    echo ""
    read -p "输入 'yes' 继续，或按 Ctrl+C 取消: " confirm

    if [[ "$confirm" == "yes" ]]; then
        echo ""
        echo "正在删除问题记录..."

        # 先备份到临时表
        mysql -h "$DB_HOST" -u "$DB_USER" -p"$MYSQL_PWD" "$DB_NAME" << 'EOF'
CREATE TABLE IF NOT EXISTS knowledge_document_backup_$(date +%Y%m%d) AS
SELECT * FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2;
EOF

        # 删除问题记录
        mysql -h "$DB_HOST" -u "$DB_USER" -p"$MYSQL_PWD" "$DB_NAME" << 'EOF'
DELETE FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2;

DELETE FROM knowledge_chunk
WHERE document_id NOT IN (
    SELECT id FROM knowledge_document
);
EOF

        echo -e "${GREEN}✅ 问题记录已删除${NC}"
        echo ""
    else
        echo "跳过删除操作"
        echo ""
    fi
fi

# 检查代码是否已更新
echo "📦 检查代码更新..."
if grep -q "cleanFilename" internal/numind/biz/salesrag/adapter/enhanced_parser.go 2>/dev/null; then
    echo -e "${GREEN}✅ 代码已更新${NC}"
else
    echo -e "${YELLOW}⚠️  代码可能未更新，请运行 git pull${NC}"
fi
echo ""

# 检查二进制文件
echo "🔨 检查编译状态..."
if [[ -f "bin/numind" ]]; then
    BIN_TIME=$(stat -f %m bin/numind 2>/dev/null || stat -c %Y bin/numind)
    CODE_TIME=$(find internal -name "*.go" -type f -exec stat -f %m {} \; 2>/dev/null | sort -n | tail -1)

    if [[ "$BIN_TIME" -lt "$CODE_TIME" ]]; then
        echo -e "${YELLOW}⚠️  二进制文件过期，需要重新编译${NC}"
        echo ""
        read -p "是否立即编译？(yes/no): " compile
        if [[ "$compile" == "yes" ]]; then
            echo "正在编译..."
            go build -o bin/numind cmd/numind/main.go
            echo -e "${GREEN}✅ 编译完成${NC}"
        fi
    else
        echo -e "${GREEN}✅ 二进制文件是最新的${NC}"
    fi
else
    echo -e "${YELLOW}⚠️  未找到二进制文件${NC}"
    echo ""
    read -p "是否立即编译？(yes/no): " compile
    if [[ "$compile" == "yes" ]]; then
        echo "正在编译..."
        go build -o bin/numind cmd/numind/main.go
        echo -e "${GREEN}✅ 编译完成${NC}"
    fi
fi
echo ""

echo "=========================================="
echo "✅ 修复完成！"
echo ""
echo "后续步骤:"
echo "1. 重启服务:"
echo "   docker-compose restart numind-server"
echo "   # 或"
echo "   pkill numind && ./bin/numind -c config_dev.yaml"
echo ""
echo "2. 重新上传 DOCX 文件测试"
echo ""
echo "3. 查看日志确认:"
echo "   tail -f numind.log | grep -i 'parsing document'"
echo "=========================================="

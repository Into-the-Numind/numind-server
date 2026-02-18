#!/bin/bash
# =====================================================
# WeCom 数据库表自动清理脚本
# 用途: 在部署过程中自动删除 wecom 相关数据表
# 警告: 此脚本不备份数据，直接删除！
# =====================================================

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 从环境变量读取配置，或使用默认值
DB_HOST="${DB_HOST:-49.233.219.254}"
DB_PORT="${DB_PORT:-13306}"
DB_USER="${DB_USER:-root}"
DB_PASS="${DB_PASS:-Numind2025}"
DB_NAME="${DB_NAME:-numind-dev}"

# 如果通过容器执行，使用容器名
if [ -n "$MYSQL_CONTAINER" ]; then
    MYSQL_CMD="docker exec -i $MYSQL_CONTAINER mysql -u$DB_USER -p$DB_PASS"
else
    MYSQL_CMD="mysql -h$DB_HOST -P$DB_PORT -u$DB_USER -p$DB_PASS"
fi

log_info "开始清理 wecom 数据库表..."
log_info "目标数据库: $DB_NAME"

# 检查 MySQL 连接
log_info "检查 MySQL 连接..."
if ! $MYSQL_CMD -e "SELECT 1;" > /dev/null 2>&1; then
    log_error "无法连接到 MySQL 数据库"
    exit 1
fi
log_info "MySQL 连接成功"

# 检查表是否存在
check_table_exists() {
    local table=$1
    local count
    count=$($MYSQL_CMD $DB_NAME -N -e "
        SELECT COUNT(*) 
        FROM information_schema.TABLES 
        WHERE TABLE_SCHEMA = DATABASE() 
        AND TABLE_NAME = '$table'
    " 2>/dev/null || echo "0")
    
    if [ "$count" -gt 0 ]; then
        return 0
    else
        return 1
    fi
}

# 删除表
drop_table() {
    local table=$1
    if check_table_exists "$table"; then
        log_info "删除表: $table"
        $MYSQL_CMD $DB_NAME -e "DROP TABLE IF EXISTS $table;" 2>/dev/null || {
            log_warn "删除 $table 失败，可能表不存在"
            return 0
        }
        log_info "✓ $table 已删除"
    else
        log_warn "表 $table 不存在，跳过"
    fi
}

# 要删除的表列表
TABLES=(
    "wecom_bind_codes"
    "wecom_cursors"
    "wecom_messages"
    "wecom_users"
)

# 关闭外键检查
log_info "关闭外键检查..."
$MYSQL_CMD $DB_NAME -e "SET FOREIGN_KEY_CHECKS = 0;" 2>/dev/null || true

# 遍历删除表
DELETED_COUNT=0
for table in "${TABLES[@]}"; do
    if check_table_exists "$table"; then
        drop_table "$table"
        ((DELETED_COUNT++)) || true
    fi
done

# 恢复外键检查
log_info "恢复外键检查..."
$MYSQL_CMD $DB_NAME -e "SET FOREIGN_KEY_CHECKS = 1;" 2>/dev/null || true

# 验证删除结果
log_info "验证删除结果..."
REMAINING=$($MYSQL_CMD $DB_NAME -N -e "
    SELECT COUNT(*) 
    FROM information_schema.TABLES 
    WHERE TABLE_SCHEMA = DATABASE() 
    AND TABLE_NAME IN ('wecom_bind_codes', 'wecom_cursors', 'wecom_messages', 'wecom_users')
" 2>/dev/null || echo "0")

if [ "$REMAINING" -eq 0 ]; then
    log_info "================================"
    log_info "✅ WeCom 数据库表清理完成"
    log_info "删除表数量: $DELETED_COUNT"
    log_info "================================"
    exit 0
else
    log_warn "仍有 $REMAINING 个表存在，可能删除失败"
    exit 1
fi

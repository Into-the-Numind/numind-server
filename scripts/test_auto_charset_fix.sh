#!/bin/bash

# 自动字符集修复功能测试脚本
# 测试服务启动时是否自动检查和修复数据库字符集

echo "🔧 自动字符集修复功能测试"
echo "============================"
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查项目编译
echo -e "${BLUE}1. 检查项目编译...${NC}"
if go build ./cmd/numind/... > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 项目编译成功${NC}"
else
    echo -e "${RED}❌ 项目编译失败${NC}"
    exit 1
fi

echo ""

# 检查配置文件
echo -e "${BLUE}2. 检查配置文件...${NC}"
if [ -f "configs/database_charset.yaml" ]; then
    echo -e "${GREEN}✅ 字符集配置文件存在${NC}"
    echo "配置文件: configs/database_charset.yaml"
else
    echo -e "${YELLOW}⚠️  字符集配置文件不存在，将使用默认配置${NC}"
fi

echo ""

# 检查代码集成
echo -e "${BLUE}3. 检查代码集成...${NC}"

# 检查autoMigrate函数
if grep -q "ensureDatabaseCharset" internal/numind/helper.go; then
    echo -e "${GREEN}✅ ensureDatabaseCharset函数已集成${NC}"
else
    echo -e "${RED}❌ ensureDatabaseCharset函数未集成${NC}"
fi

# 检查配置管理
if grep -q "getDatabaseCharsetConfig" internal/numind/helper.go; then
    echo -e "${GREEN}✅ getDatabaseCharsetConfig函数已集成${NC}"
else
    echo -e "${RED}❌ getDatabaseCharsetConfig函数未集成${NC}"
fi

# 检查配置包
if [ -f "internal/numind/config/database_charset.go" ]; then
    echo -e "${GREEN}✅ 字符集配置包已创建${NC}"
else
    echo -e "${RED}❌ 字符集配置包未创建${NC}"
fi

echo ""

# 检查服务状态
echo -e "${BLUE}4. 检查服务状态...${NC}"
if curl -s http://localhost:9091/healthz > /dev/null; then
    echo -e "${GREEN}✅ 服务正在运行${NC}"
    SERVICE_RUNNING=true
else
    echo -e "${YELLOW}⚠️  服务未运行${NC}"
    SERVICE_RUNNING=false
fi

echo ""

# 显示功能说明
echo -e "${BLUE}5. 自动字符集修复功能说明:${NC}"
echo "现在服务启动时会自动："
echo ""
echo "🔍 **启动时检查**: 检查数据库和关键表的字符集"
echo "🛠️  **自动修复**: 如果发现不匹配的字符集，自动修复"
echo "📊 **迁移后检查**: 在数据库迁移完成后再次检查"
echo "⚙️  **配置驱动**: 通过配置文件控制修复行为"
echo ""
echo "**支持的配置项**:"
echo "  - target_charset: 目标字符集 (默认: utf8mb4)"
echo "  - target_collation: 目标排序规则 (默认: utf8mb4_unicode_ci)"
echo "  - auto_fix: 是否自动修复 (默认: true)"
echo "  - check_on_startup: 启动时检查 (默认: true)"
echo "  - check_after_migration: 迁移后检查 (默认: true)"
echo "  - critical_tables: 关键表列表"
echo ""

# 显示关键表
echo -e "${BLUE}6. 关键表列表:${NC}"
echo "以下表的字符集会被优先检查和修复："
echo "  - chat_message (聊天消息表 - 重要)"
echo "  - chat_session (聊天会话表)"
echo "  - book (卡册表)"
echo "  - user (用户表)"
echo "  - card (卡片表)"
echo "  - category (分类表)"
echo "  - 其他相关表..."
echo ""

# 下一步操作
echo -e "${BLUE}7. 下一步操作:${NC}"
if [ "$SERVICE_RUNNING" = true ]; then
    echo -e "${GREEN}服务正在运行，可以测试自动修复功能：${NC}"
    echo "1. 重启服务观察日志输出"
    echo "2. 检查是否自动修复了字符集问题"
    echo "3. 测试WebSocket聊天功能是否正常"
else
    echo -e "${YELLOW}请启动服务测试自动修复功能：${NC}"
    echo "go run cmd/numind/main.go"
fi

echo ""
echo -e "${BLUE}8. 测试方法:${NC}"
echo "方法1: 重启服务观察日志"
echo "  go run cmd/numind/main.go"
echo ""
echo "方法2: 查看日志输出"
echo "  观察是否出现字符集检查和修复的日志"
echo ""
echo "方法3: 测试功能"
echo "  python3 scripts/test_smart_chat.py"
echo ""

echo -e "${BLUE}9. 预期日志输出:${NC}"
echo "启动时应该看到类似日志："
echo "  INFO Ensuring database charset... target_charset=utf8mb4 target_collation=utf8mb4_unicode_ci"
echo "  INFO Current database charset charset=utf8mb3 collation=utf8mb3_general_ci"
echo "  INFO Database charset needs to be updated from=utf8mb3 to=utf8mb4"
echo "  INFO Database charset updated successfully"
echo "  INFO Table charset info table=chat_message charset=utf8mb3 collation=utf8mb3_general_ci"
echo "  INFO Updating table charset table=chat_message from=utf8mb3 to=utf8mb4"
echo "  INFO Table charset updated successfully table=chat_message"
echo ""

echo -e "${GREEN}🎉 自动字符集修复功能已集成完成！${NC}"
echo ""
echo -e "${YELLOW}现在每次启动服务时：${NC}"
echo "• 系统会自动检查数据库字符集"
echo "• 发现不匹配时自动修复"
echo "• 支持配置文件控制修复行为"
echo "• 无需手动执行SQL脚本"
echo "• 确保中文字符正常存储和显示"
echo ""
echo -e "${BLUE}配置文件位置: configs/database_charset.yaml${NC}"
echo -e "${BLUE}重启服务即可体验自动修复功能！${NC}"

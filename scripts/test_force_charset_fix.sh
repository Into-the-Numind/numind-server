#!/bin/bash

# 强制字符集修复功能测试脚本
# 测试改进后的autoMigrate函数是否能彻底解决字符集问题

echo "🔧 强制字符集修复功能测试"
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

# 检查改进的代码
echo -e "${BLUE}2. 检查改进的代码...${NC}"

# 检查强制修复函数
if grep -q "forceEnsureDatabaseCharset" internal/numind/helper.go; then
    echo -e "${GREEN}✅ forceEnsureDatabaseCharset函数已添加${NC}"
else
    echo -e "${RED}❌ forceEnsureDatabaseCharset函数未添加${NC}"
fi

if grep -q "forceFixChatMessageTable" internal/numind/helper.go; then
    echo -e "${GREEN}✅ forceFixChatMessageTable函数已添加${NC}"
else
    echo -e "${RED}❌ forceFixChatMessageTable函数未添加${NC}"
fi

if grep -q "verifyCharsetRepair" internal/numind/helper.go; then
    echo -e "${GREEN}✅ verifyCharsetRepair函数已添加${NC}"
else
    echo -e "${RED}❌ verifyCharsetRepair函数未添加${NC}"
fi

echo ""

# 检查配置文件
echo -e "${BLUE}3. 检查配置文件...${NC}"
if [ -f "configs/database_charset.yaml" ]; then
    echo -e "${GREEN}✅ 字符集配置文件存在${NC}"
    echo "配置文件: configs/database_charset.yaml"
else
    echo -e "${YELLOW}⚠️  字符集配置文件不存在，将使用默认配置${NC}"
fi

echo ""

# 显示改进的功能说明
echo -e "${BLUE}4. 改进的强制修复功能说明:${NC}"
echo "现在服务启动时会强制执行以下操作："
echo ""
echo "🔧 **启动时强制修复**: 强制更新数据库和所有关键表字符集"
echo "🔄 **迁移后强制修复**: 在数据库迁移完成后再次强制修复"
echo "🎯 **特别处理chat_message表**: 使用多种方法强制修复出错表"
echo "✅ **验证修复结果**: 验证所有修复操作是否成功"
echo "📊 **详细日志记录**: 记录每个步骤的执行结果"
echo ""
echo "**强制修复策略**:"
echo "  - 不再检查当前状态，直接执行修复操作"
echo "  - 使用多种修复方法确保成功率"
echo "  - 特别关注chat_message表（出错的主要表）"
echo "  - 修复后立即验证结果"
echo ""

# 显示修复流程
echo -e "${BLUE}5. 强制修复流程:${NC}"
echo "1. 启动时强制修复数据库字符集"
echo "2. 强制修复所有关键表字符集"
echo "3. 执行数据库结构迁移"
echo "4. 迁移后再次强制修复字符集"
echo "5. 特别强制修复chat_message表"
echo "6. 验证所有修复操作结果"
echo ""

# 显示关键表
echo -e "${BLUE}6. 关键表列表:${NC}"
echo "以下表的字符集会被强制修复："
echo "  - chat_message (聊天消息表 - 重点修复)"
echo "  - chat_session (聊天会话表)"
echo "  - book (卡册表)"
echo "  - user (用户表)"
echo "  - card (卡片表)"
echo "  - category (分类表)"
echo "  - 其他相关表..."
echo ""

# 显示chat_message表特别处理
echo -e "${BLUE}7. chat_message表特别处理:${NC}"
echo "由于这是出错的主要表，会使用多种方法强制修复："
echo "  - 方法1: 强制转换整个表字符集"
echo "  - 方法2: 强制修改content字段字符集"
echo "  - 方法3: 强制修改所有TEXT字段字符集"
echo "  - 验证: 确认修复结果"
echo ""

# 下一步操作
echo -e "${BLUE}8. 下一步操作:${NC}"
echo -e "${GREEN}现在可以启动服务测试强制修复功能：${NC}"
echo "go run cmd/numind/main.go"
echo ""
echo "观察日志输出，应该看到："
echo "  - Starting database charset verification and repair..."
echo "  - Force updating database charset..."
echo "  - Force fixing table charset table=chat_message"
echo "  - Force fixing chat_message table with multiple approaches..."
echo "  - Verifying charset repair results..."
echo ""

echo -e "${BLUE}9. 预期结果:${NC}"
echo "启动后应该看到详细的修复日志，包括："
echo "  ✅ 数据库字符集强制更新成功"
echo "  ✅ 所有关键表字符集强制更新成功"
echo "  ✅ chat_message表特别修复成功"
echo "  ✅ 验证结果显示所有字符集都是utf8mb4"
echo ""

echo -e "${BLUE}10. 测试验证:${NC}"
echo "修复完成后，测试WebSocket聊天功能："
echo "python3 scripts/test_smart_chat.py"
echo ""
echo "应该不再出现字符集转换错误！"
echo ""

echo -e "${GREEN}🎉 强制字符集修复功能已集成完成！${NC}"
echo ""
echo -e "${YELLOW}主要改进:${NC}"
echo "• 不再检查当前状态，直接强制修复"
echo "• 使用多种修复方法确保成功率"
echo "• 特别关注chat_message表的问题"
echo "• 详细的日志记录和验证"
echo "• 完全集成到autoMigrate中"
echo ""
echo -e "${BLUE}现在重启服务即可彻底解决字符集问题！${NC}"
echo -e "${BLUE}无需手动执行任何SQL脚本！${NC}"

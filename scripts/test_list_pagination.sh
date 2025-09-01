#!/bin/bash

# 测试列表分页修复效果的脚本

set -e

echo "🧪 测试列表分页修复效果..."
echo "=========================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查服务是否运行
echo -e "${BLUE}🔍 检查服务状态...${NC}"
if pgrep -f "numind" > /dev/null; then
    echo -e "${GREEN}✅ 服务正在运行${NC}"
else
    echo -e "${RED}❌ 服务未运行，请先启动服务${NC}"
    echo "启动命令: ./numind"
    exit 1
fi

echo ""

# 监控关键修复日志
echo -e "${BLUE}🔧 监控列表分页修复效果日志...${NC}"
echo "正在监控以下关键指标："
echo "1. 列表分割逻辑触发"
echo "2. 分割元素数量"
echo "3. 卡片创建数量"
echo "4. 第7条内容处理"

echo ""
echo -e "${YELLOW}⏳ 请在另一个窗口创建包含7条列表内容的book，然后观察以下日志...${NC}"
echo ""

# 实时监控关键日志
tail -f logs/app.log | grep -E "(🔧 开始分割列表元素|🎯 分割完成|🔍 调试：元素分割成功|🔍 调试.*分割元素创建新卡片|分页完成.*总卡片数|🔍 调试.*项 6)" --line-buffered | while read line; do
    case "$line" in
        *"🔧 开始分割列表元素"*)
            echo -e "${BLUE}$line${NC}"
            ;;
        *"🎯 分割完成"*|*"元素分割成功"*)
            echo -e "${GREEN}$line${NC}"
            ;;
        *"分割元素创建新卡片"*|*"分页完成"*)
            echo -e "${GREEN}$line${NC}"
            ;;
        *"项 6"*)
            echo -e "${YELLOW}$line${NC}"
            ;;
        *)
            echo "$line"
            ;;
    esac
done

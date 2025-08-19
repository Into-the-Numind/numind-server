#!/bin/bash

# 验证第7条内容修复效果的脚本

set -e

echo "🔧 验证第7条内容修复效果..."
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
echo -e "${BLUE}🔧 监控修复效果日志...${NC}"
echo "正在监控以下关键指标："
echo "1. Chrome窗口尺寸设置"
echo "2. 渲染超时调整"
echo "3. 第7条内容处理"
echo "4. 最终卡片包含的内容"

echo ""
echo -e "${YELLOW}⏳ 请在另一个窗口创建包含7条内容的book，然后观察以下日志...${NC}"
echo ""

# 实时监控关键日志
tail -f logs/app.log | grep -E "(🔧 设置Chrome窗口尺寸|⏱️ 根据内容高度|🔍 调试.*项 6|🔍 调试.*第7条|Chrome渲染超时|Chrome渲染成功|🔍 调试.*最后页项 6)" --line-buffered | while read line; do
    case "$line" in
        *"🔧 设置Chrome窗口尺寸"*)
            echo -e "${BLUE}$line${NC}"
            ;;
        *"⏱️ 根据内容高度"*)
            echo -e "${BLUE}$line${NC}"
            ;;
        *"项 6"*|*"第7条"*)
            echo -e "${GREEN}$line${NC}"
            ;;
        *"Chrome渲染超时"*)
            echo -e "${RED}$line${NC}"
            ;;
        *"Chrome渲染成功"*|*"渲染完成"*)
            echo -e "${GREEN}$line${NC}"
            ;;
        *)
            echo "$line"
            ;;
    esac
done

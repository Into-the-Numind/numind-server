#!/bin/bash

# 验证字体渲染一致性
# 用于检查本地和Docker环境的字体配置

set -e

echo "🔍 字体渲染验证工具"
echo "===================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 1. 检查本地字体安装
echo "1️⃣  检查本地字体安装情况："
echo "----------------------------"

if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    FONT_DIR="$HOME/Library/Fonts"
    echo "📍 系统: macOS"
    echo "📁 用户字体目录: $FONT_DIR"
    echo ""

    # 检查思源宋体
    if ls "$FONT_DIR"/SourceHanSerifSC-*.otf &> /dev/null; then
        echo -e "${GREEN}✅ 找到思源宋体字体文件:${NC}"
        ls -lh "$FONT_DIR"/SourceHanSerifSC-*.otf
    else
        echo -e "${RED}❌ 未找到思源宋体字体文件${NC}"
        echo "   请运行: ./scripts/install-sourcehan-fonts.sh"
    fi

    # 检查系统字体列表
    echo ""
    echo "系统字体注册情况："
    if system_profiler SPFontsDataType 2>/dev/null | grep -i "source.*han.*serif" > /dev/null; then
        echo -e "${GREEN}✅ 系统已注册思源宋体${NC}"
    else
        echo -e "${YELLOW}⚠️  系统未注册思源宋体（可能需要重启应用）${NC}"
    fi

elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    echo "📍 系统: Linux"

    if fc-list | grep -i "sourcehanserif" > /dev/null; then
        echo -e "${GREEN}✅ 找到思源宋体:${NC}"
        fc-list | grep -i "sourcehanserif"
    else
        echo -e "${RED}❌ 未找到思源宋体${NC}"
        echo "   请运行: ./scripts/install-sourcehan-fonts.sh"
    fi
fi

echo ""
echo "2️⃣  检查Docker容器字体安装："
echo "----------------------------"

# 检查是否有运行中的容器
CONTAINER=$(docker ps --filter "ancestor=numind-server" --format "{{.Names}}" | head -1)

if [ -z "$CONTAINER" ]; then
    echo -e "${YELLOW}⚠️  没有运行中的numind-server容器${NC}"
    echo "   提示: 使用 'task docker-run' 启动容器"
else
    echo "📦 容器: $CONTAINER"
    echo ""
    echo "容器内字体安装情况："

    # 检查字体文件
    if docker exec "$CONTAINER" ls /usr/share/fonts/truetype/SourceHanSerifSC-*.otf &> /dev/null; then
        echo -e "${GREEN}✅ 容器内找到思源宋体文件${NC}"
        docker exec "$CONTAINER" ls -lh /usr/share/fonts/truetype/SourceHanSerifSC-*.otf
    else
        echo -e "${RED}❌ 容器内未找到思源宋体文件${NC}"
    fi

    echo ""
    echo "容器字体缓存："
    docker exec "$CONTAINER" fc-list | grep -i "sourcehanserif" || echo "未在字体缓存中找到"
fi

echo ""
echo "3️⃣  检查配置文件字体设置："
echo "----------------------------"

# 检查配置文件
for config in config_local.yaml config_dev.yaml config_prod.yaml config_qa.yaml; do
    if [ -f "$config" ]; then
        echo ""
        echo "📄 $config:"
        grep -A1 "fonts:" "$config" 2>/dev/null | grep "family:" | sed 's/^/   /' || echo "   未找到字体配置"
    fi
done

echo ""
echo "4️⃣  检查代码中的字体配置："
echo "----------------------------"

# 检查封面生成代码
echo "📝 封面HTML生成 (async_processor.go):"
grep "font-family.*SourceHanSerifSC" internal/numind/biz/book/async_processor.go | head -1 | sed 's/^/   /' || echo "   未找到"

echo ""
echo "📝 Markdown转换 (html_converter.go):"
grep "FontFamily.*SourceHanSerifSC" internal/numind/biz/markdown/html_converter.go | head -1 | sed 's/^/   /' || echo "   未找到"

echo ""
echo "5️⃣  字体回退链分析："
echo "----------------------------"
echo "当前配置的字体优先级："
echo "1. SourceHanSerifSC (思源宋体)"
echo "2. STFangsong (华文仿宋)"
echo "3. Noto Sans CJK SC"
echo "4. PingFang SC (苹方)"
echo "5. Microsoft YaHei (微软雅黑)"
echo ""

if [[ "$OSTYPE" == "darwin"* ]]; then
    echo "macOS 系统可用字体："
    for font in "STFangsong" "PingFang SC"; do
        if system_profiler SPFontsDataType 2>/dev/null | grep -q "$font"; then
            echo -e "  ${GREEN}✅ $font${NC}"
        else
            echo -e "  ${RED}❌ $font${NC}"
        fi
    done
fi

echo ""
echo "===================="
echo "📊 验证总结："
echo ""

# 总结
LOCAL_OK=false
DOCKER_OK=false

if [[ "$OSTYPE" == "darwin"* ]]; then
    if ls "$HOME/Library/Fonts"/SourceHanSerifSC-*.otf &> /dev/null; then
        LOCAL_OK=true
    fi
fi

if [ ! -z "$CONTAINER" ]; then
    if docker exec "$CONTAINER" ls /usr/share/fonts/truetype/SourceHanSerifSC-*.otf &> /dev/null; then
        DOCKER_OK=true
    fi
fi

if $LOCAL_OK && $DOCKER_OK; then
    echo -e "${GREEN}✅ 本地和Docker环境字体配置一致${NC}"
elif $LOCAL_OK && ! $DOCKER_OK; then
    echo -e "${YELLOW}⚠️  本地已安装字体，但Docker容器未运行或字体未安装${NC}"
elif ! $LOCAL_OK && $DOCKER_OK; then
    echo -e "${YELLOW}⚠️  Docker容器字体正常，但本地未安装${NC}"
    echo "   运行: ./scripts/install-sourcehan-fonts.sh"
else
    echo -e "${RED}❌ 本地和Docker都未正确安装字体${NC}"
    echo "   1. 运行: ./scripts/install-sourcehan-fonts.sh (安装本地字体)"
    echo "   2. 重建Docker镜像: task docker-build"
fi

echo ""
echo "💡 提示："
echo "   - 安装字体后需要重启Chrome浏览器"
echo "   - 重启应用服务以应用字体变化"
echo "   - 使用Chrome DevTools验证实际渲染字体"
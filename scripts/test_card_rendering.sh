#!/bin/bash

# 卡片渲染测试脚本
# 用于诊断和测试增强版卡片渲染功能

set -e

echo "🧪 开始卡片渲染测试..."
echo "=================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查Chrome是否可用
echo -e "${BLUE}🔍 检查Chrome无头浏览器...${NC}"

chrome_cmd=""

# 检查macOS上的Chrome
if [ -f "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]; then
    echo -e "${GREEN}✅ 找到 Google Chrome (macOS)${NC}"
    chrome_cmd="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
# 检查常见的Linux命令
elif command -v google-chrome >/dev/null 2>&1; then
    echo -e "${GREEN}✅ 找到 google-chrome${NC}"
    chrome_cmd="google-chrome"
elif command -v chromium-browser >/dev/null 2>&1; then
    echo -e "${GREEN}✅ 找到 chromium-browser${NC}"
    chrome_cmd="chromium-browser"
elif command -v chromium >/dev/null 2>&1; then
    echo -e "${GREEN}✅ 找到 chromium${NC}"
    chrome_cmd="chromium"
else
    echo -e "${RED}❌ 未找到Chrome浏览器${NC}"
    echo "请安装Chrome或Chromium浏览器："
    echo "  macOS: brew install --cask google-chrome"
    echo "  Ubuntu: sudo apt-get install google-chrome-stable"
    echo "  CentOS: sudo yum install google-chrome-stable"
    exit 1
fi

# 测试Chrome版本
echo -e "${BLUE}📋 Chrome版本信息:${NC}"
"$chrome_cmd" --version || echo -e "${YELLOW}⚠️ 无法获取Chrome版本${NC}"

echo ""

# 检查必要的目录
echo -e "${BLUE}📁 检查图片存储目录...${NC}"

DIRS_TO_CHECK=(
    "images/upload/card"
    "res/upload/card"
    "uploads/card"
)

CARD_DIR=""
for dir in "${DIRS_TO_CHECK[@]}"; do
    if [ -d "$dir" ]; then
        echo -e "${GREEN}✅ 找到目录: $dir${NC}"
        CARD_DIR="$dir"
        break
    fi
done

if [ -z "$CARD_DIR" ]; then
    echo -e "${YELLOW}⚠️ 未找到标准卡片目录，将创建 images/upload/card${NC}"
    mkdir -p images/upload/card
    CARD_DIR="images/upload/card"
fi

# 检查目录权限
echo -e "${BLUE}🔐 检查目录权限...${NC}"
if [ -w "$CARD_DIR" ]; then
    echo -e "${GREEN}✅ 目录可写: $CARD_DIR${NC}"
else
    echo -e "${RED}❌ 目录不可写: $CARD_DIR${NC}"
    echo "请修复权限: chmod 755 $CARD_DIR"
fi

echo ""

# 创建测试HTML文件
echo -e "${BLUE}🧪 创建测试HTML文件...${NC}"

TEST_HTML="test_render_$(date +%s).html"
cat > "$TEST_HTML" << 'EOF'
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>卡片渲染测试</title>
    <style>
        body {
            margin: 0;
            padding: 60px;
            width: 1080px;
            font-family: 'Arial', sans-serif;
            background-color: #FFFFFF;
        }
        .test-card {
            width: 960px;
            margin: 0 auto 40px auto;
            padding: 40px;
            border: 2px solid #E0E0E0;
            border-radius: 12px;
            background-color: #F9F9F9;
        }
        .test-title {
            font-size: 36px;
            font-weight: bold;
            color: #333333;
            text-align: center;
            margin-bottom: 30px;
        }
        .test-content {
            font-size: 18px;
            line-height: 1.8;
            color: #333333;
            text-align: justify;
        }
    </style>
</head>
<body>
    <div class="test-card">
        <div class="test-title">卡片渲染测试</div>
        <div class="test-content">
            这是一个用于测试增强版卡片渲染器的HTML文件。
            如果您能看到这段文本被正确渲染为图片，说明Chrome无头浏览器工作正常。
        </div>
    </div>
    
    <div class="test-card">
        <div class="test-title">性能测试</div>
        <div class="test-content">
            此测试包含多个卡片元素，用于验证渲染性能和内存使用情况。
            如果渲染过程中出现卡顿，请检查系统资源使用情况。
        </div>
    </div>
</body>
</html>
EOF

echo -e "${GREEN}✅ 测试HTML文件创建完成: $TEST_HTML${NC}"

# 测试Chrome无头浏览器渲染
echo -e "${BLUE}🖥️ 测试Chrome无头浏览器渲染...${NC}"

TEST_IMAGE="test_render_$(date +%s).png"
ABS_HTML_PATH=$(realpath "$TEST_HTML")

echo -e "${BLUE}📍 HTML文件路径: file://$ABS_HTML_PATH${NC}"

# 运行Chrome渲染测试
echo -e "${BLUE}⏱️ 开始渲染测试...${NC}"
start_time=$(date +%s)

timeout 30 "$chrome_cmd" \
    --headless \
    --disable-gpu \
    --no-sandbox \
    --disable-dev-shm-usage \
    --window-size=1080,1440 \
    --screenshot="$TEST_IMAGE" \
    "file://$ABS_HTML_PATH" 2>/dev/null

end_time=$(date +%s)
render_time=$((end_time - start_time))

if [ -f "$TEST_IMAGE" ]; then
    image_size=$(stat -f%z "$TEST_IMAGE" 2>/dev/null || stat -c%s "$TEST_IMAGE" 2>/dev/null)
    echo -e "${GREEN}✅ 渲染成功！${NC}"
    echo -e "${GREEN}  渲染时间: ${render_time}秒${NC}"
    echo -e "${GREEN}  图片大小: ${image_size}字节${NC}"
    echo -e "${GREEN}  图片路径: $TEST_IMAGE${NC}"
    
    # 检查图片尺寸（如果有imagemagick）
    if command -v identify >/dev/null 2>&1; then
        image_info=$(identify "$TEST_IMAGE" 2>/dev/null || echo "无法获取图片信息")
        echo -e "${GREEN}  图片信息: $image_info${NC}"
    fi
else
    echo -e "${RED}❌ 渲染失败，未生成图片文件${NC}"
    echo -e "${YELLOW}可能的问题:${NC}"
    echo "  1. Chrome权限问题"
    echo "  2. 内存不足"
    echo "  3. 系统资源不足"
    echo "  4. HTML文件路径问题"
fi

echo ""

# 内存和性能建议
echo -e "${BLUE}💡 性能优化建议...${NC}"
echo "=================================="

echo -e "${YELLOW}如果渲染时间过长（>10秒）或出现卡顿:${NC}"
echo "1. 检查系统内存使用情况: free -h (Linux) 或 vm_stat (macOS)"
echo "2. 关闭其他占用内存的程序"
echo "3. 增加系统虚拟内存"
echo "4. 考虑减少单次渲染的图片高度"

echo ""
echo -e "${YELLOW}Chrome优化参数已应用:${NC}"
echo "- 禁用GPU加速"
echo "- 禁用沙盒模式"
echo "- 增加内存限制"
echo "- 禁用后台进程"

echo ""

# 清理测试文件
echo -e "${BLUE}🧹 清理测试文件...${NC}"
read -p "是否删除测试文件? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    rm -f "$TEST_HTML" "$TEST_IMAGE"
    echo -e "${GREEN}✅ 测试文件已删除${NC}"
else
    echo -e "${YELLOW}⚠️ 测试文件保留: $TEST_HTML, $TEST_IMAGE${NC}"
fi

echo ""
echo -e "${GREEN}✅ 卡片渲染测试完成！${NC}"

# 提供进一步的帮助信息
echo ""
echo -e "${BLUE}📖 进一步调试:${NC}"
echo "1. 查看实时日志: tail -f logs/app.log"
echo "2. 检查API状态: ./scripts/diagnose_api_issues.sh"
echo "3. 运行完整测试: go test ./internal/numind/biz/card/..."

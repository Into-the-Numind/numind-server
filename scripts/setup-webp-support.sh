#!/bin/bash

# 设置webp支持的安装脚本
# 确保系统能够进行webp格式转换

echo "🔧 开始安装webp支持..."

# 检测操作系统
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    echo "📱 检测到macOS系统"
    
    if command -v brew &> /dev/null; then
        echo "🍺 使用Homebrew安装webp工具..."
        brew install webp
    else
        echo "❌ 未检测到Homebrew，请先安装Homebrew"
        echo "   安装命令: /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi
    
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    echo "🐧 检测到Linux系统"
    
    if command -v apt-get &> /dev/null; then
        echo "📦 使用apt-get安装webp工具..."
        sudo apt-get update
        sudo apt-get install -y webp
    elif command -v yum &> /dev/null; then
        echo "📦 使用yum安装webp工具..."
        sudo yum install -y libwebp-tools
    elif command -v dnf &> /dev/null; then
        echo "📦 使用dnf安装webp工具..."
        sudo dnf install -y libwebp-tools
    else
        echo "❌ 未检测到支持的包管理器，请手动安装webp工具"
        exit 1
    fi
    
else
    echo "❌ 不支持的操作系统: $OSTYPE"
    exit 1
fi

# 验证安装
if command -v cwebp &> /dev/null; then
    echo "✅ webp工具安装成功"
    echo "📊 版本信息:"
    cwebp -version
else
    echo "❌ webp工具安装失败"
    exit 1
fi

# 测试转换功能
echo "🧪 测试webp转换功能..."
TEMP_DIR="/tmp/webp_test_$$"
mkdir -p "$TEMP_DIR"

# 创建一个简单的测试图片
cat > "$TEMP_DIR/test.svg" << 'EOF'
<svg width="100" height="100" xmlns="http://www.w3.org/2000/svg">
  <rect width="100" height="100" fill="red"/>
  <text x="50" y="50" text-anchor="middle" fill="white" font-size="16">Test</text>
</svg>
EOF

# 使用rsvg-convert转换为PNG（如果可用）
if command -v rsvg-convert &> /dev/null; then
    rsvg-convert -w 100 -h 100 "$TEMP_DIR/test.svg" -o "$TEMP_DIR/test.png"
elif command -v convert &> /dev/null; then
    convert "$TEMP_DIR/test.svg" "$TEMP_DIR/test.png"
else
    # 创建一个简单的测试PNG
    echo "⚠️  未检测到SVG转换工具，创建简单测试图片..."
    # 这里可以添加创建简单PNG的代码
    echo "请手动创建一个测试PNG文件进行测试"
fi

# 测试webp转换
if [ -f "$TEMP_DIR/test.png" ]; then
    if cwebp -q 95 "$TEMP_DIR/test.png" -o "$TEMP_DIR/test.webp"; then
        echo "✅ webp转换测试成功"
        echo "📁 测试文件位置: $TEMP_DIR"
        echo "📊 文件大小对比:"
        ls -lh "$TEMP_DIR/test.png" "$TEMP_DIR/test.webp"
    else
        echo "❌ webp转换测试失败"
    fi
else
    echo "⚠️  跳过webp转换测试（缺少测试PNG文件）"
fi

echo "🎉 webp支持安装完成！"
echo ""
echo "📋 使用说明:"
echo "   - 所有卡片渲染器现在都会输出webp格式"
echo "   - 模板背景支持已启用"
echo "   - 图片质量设置为95%，确保高质量输出"
echo ""
echo "🔍 验证命令:"
echo "   cwebp -version"
echo "   which cwebp"

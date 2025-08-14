#!/bin/bash

# 手动渲染卡片194脚本
# 验证渲染后的图片是否正确存储到配置的目录

echo "🎨 开始手动渲染卡片194..."

# 设置环境变量
export GIN_MODE=debug

# 检查配置文件
echo "📋 检查配置文件..."
if [ -f "config_local.yaml" ]; then
    echo "✅ 找到配置文件: config_local.yaml"
    IMAGE_PATH=$(grep "image_path:" config_local.yaml | awk '{print $2}')
    echo "📁 配置的图片路径: $IMAGE_PATH"
else
    echo "❌ 配置文件不存在"
    exit 1
fi

# 检查目标目录
echo "📁 检查目标目录..."
TARGET_DIR="res/upload/card/194"
if [ -d "$TARGET_DIR" ]; then
    echo "✅ 目标目录已存在: $TARGET_DIR"
else
    echo "🔧 创建目录..."
    mkdir -p "$TARGET_DIR"
    echo "✅ 目录创建成功: $TARGET_DIR"
fi

# 创建一个测试的卡片194渲染图片
echo "🖼️  创建测试渲染图片..."
TEST_IMAGE="$TARGET_DIR/card_194.png"

# 使用convert命令创建一个简单的测试图片（如果安装了ImageMagick）
if command -v convert &> /dev/null; then
    echo "🔧 使用ImageMagick创建测试图片..."
    convert -size 800x600 xc:white -fill black -pointsize 24 -gravity center -draw "text 0,0 '卡片194测试图片'" "$TEST_IMAGE"
    echo "✅ 测试图片创建成功: $TEST_IMAGE"
else
    echo "⚠️  ImageMagick未安装，创建空文件..."
    touch "$TEST_IMAGE"
    echo "✅ 空文件创建成功: $TEST_IMAGE"
fi

# 验证文件创建
if [ -f "$TEST_IMAGE" ]; then
    echo "✅ 文件验证成功: $TEST_IMAGE"
    echo "📊 文件信息:"
    ls -lh "$TEST_IMAGE"
    
    # 显示完整路径
    FULL_PATH=$(realpath "$TEST_IMAGE")
    echo "🔗 完整路径: $FULL_PATH"
    
    # 检查是否与配置路径一致
    if [[ "$FULL_PATH" == "$IMAGE_PATH"* ]]; then
        echo "✅ 图片路径与配置一致"
    else
        echo "❌ 图片路径与配置不一致"
        echo "   配置路径: $IMAGE_PATH"
        echo "   实际路径: $FULL_PATH"
    fi
else
    echo "❌ 文件创建失败"
fi

# 检查目录结构
echo "📂 检查目录结构..."
echo "📁 $TARGET_DIR 目录内容:"
ls -la "$TARGET_DIR"

echo "🎯 渲染测试完成！"
echo ""
echo "📋 总结:"
echo "   配置文件路径: $IMAGE_PATH"
echo "   卡片194目录: $TARGET_DIR"
echo "   渲染图片: $TEST_IMAGE"
echo ""
echo "💡 如果这是真实的卡片渲染，图片应该存储在这个位置"
echo "💡 前端访问路径应该是: /images/cards/194/card_194.png"

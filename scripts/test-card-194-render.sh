#!/bin/bash

# 测试卡片194渲染脚本
# 验证渲染后的图片是否正确存储到配置的目录

echo "🔍 开始测试卡片194渲染..."

# 设置环境变量
export GIN_MODE=debug

# 检查配置文件
echo "📋 检查配置文件..."
if [ -f "config_local.yaml" ]; then
    echo "✅ 找到配置文件: config_local.yaml"
    echo "📁 配置的图片路径:"
    grep "image_path:" config_local.yaml
else
    echo "❌ 配置文件不存在"
    exit 1
fi

# 检查目标目录
echo "📁 检查目标目录..."
TARGET_DIR="res/upload/card/194"
if [ -d "$TARGET_DIR" ]; then
    echo "✅ 目标目录已存在: $TARGET_DIR"
    echo "📄 目录内容:"
    ls -la "$TARGET_DIR"
else
    echo "⚠️  目标目录不存在: $TARGET_DIR"
    echo "🔧 创建目录..."
    mkdir -p "$TARGET_DIR"
fi

# 检查是否有卡片194的渲染图片
echo "🖼️  检查卡片194的渲染图片..."
RENDERED_IMAGE="$TARGET_DIR/card_194.png"
if [ -f "$RENDERED_IMAGE" ]; then
    echo "✅ 找到渲染图片: $RENDERED_IMAGE"
    echo "📊 图片信息:"
    file "$RENDERED_IMAGE"
    ls -lh "$RENDERED_IMAGE"
else
    echo "❌ 渲染图片不存在: $RENDERED_IMAGE"
    echo "💡 需要先渲染卡片194"
fi

# 检查整个card目录结构
echo "📂 检查整个card目录结构..."
echo "📁 res/upload/card 目录内容:"
ls -la res/upload/card/ | head -20

echo "🔍 测试完成！"

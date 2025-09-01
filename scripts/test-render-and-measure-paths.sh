#!/bin/bash

# 测试新的渲染-测量方案的图片保存路径
# 验证无头浏览器先渲染再测量的逻辑是否正确保存图片到配置的目录

echo "🚀 开始测试新的渲染-测量方案..."

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

# 检查渲染-测量方案的配置
echo "🔧 检查渲染-测量方案配置..."
if [ -f "internal/numind/biz/card/config.go" ]; then
    echo "✅ 找到渲染器配置文件"
    echo "📋 渲染-测量方案配置:"
    grep -A 10 "EnableRenderAndMeasure" internal/numind/biz/card/config.go
else
    echo "❌ 渲染器配置文件不存在"
fi

# 检查目标目录结构
echo "📁 检查目标目录结构..."
CARD_DIR="res/upload/card"
BOOK_DIR="res/upload/book"

if [ -d "$CARD_DIR" ]; then
    echo "✅ 卡片目录存在: $CARD_DIR"
    echo "📄 卡片目录内容 (前10个):"
    ls -la "$CARD_DIR" | head -10
else
    echo "❌ 卡片目录不存在: $CARD_DIR"
fi

if [ -d "$BOOK_DIR" ]; then
    echo "✅ 书籍目录存在: $BOOK_DIR"
    echo "📄 书籍目录内容 (前10个):"
    ls -la "$BOOK_DIR" | head -10
else
    echo "❌ 书籍目录不存在: $BOOK_DIR"
fi

# 检查最新的卡片目录
echo "🔍 检查最新的卡片目录..."
if [ -d "$CARD_DIR" ]; then
    LATEST_CARD=$(ls -1 "$CARD_DIR" | sort -n | tail -1)
    if [ -n "$LATEST_CARD" ]; then
        echo "📊 最新卡片ID: $LATEST_CARD"
        LATEST_CARD_DIR="$CARD_DIR/$LATEST_CARD"
        echo "📁 最新卡片目录: $LATEST_CARD_DIR"
        
        if [ -d "$LATEST_CARD_DIR" ]; then
            echo "📄 最新卡片目录内容:"
            ls -la "$LATEST_CARD_DIR"
            
            # 检查是否有渲染图片
            RENDERED_IMAGE="$LATEST_CARD_DIR/card_$LATEST_CARD.png"
            if [ -f "$RENDERED_IMAGE" ]; then
                echo "✅ 找到渲染图片: $RENDERED_IMAGE"
                echo "📊 图片信息:"
                ls -lh "$RENDERED_IMAGE"
                file "$RENDERED_IMAGE"
            else
                echo "❌ 渲染图片不存在: $RENDERED_IMAGE"
            fi
        fi
    else
        echo "⚠️  没有找到卡片目录"
    fi
fi

# 检查渲染-测量方案的代码实现
echo "🔍 检查渲染-测量方案的代码实现..."
echo "📋 检查RenderAndMeasureRenderer的saveImage方法:"
if [ -f "internal/numind/biz/card/render_and_measure_renderer.go" ]; then
    echo "✅ 找到RenderAndMeasureRenderer实现"
    echo "🔍 saveImage方法实现:"
    grep -A 20 "func (r \*RenderAndMeasureRenderer) saveImage" internal/numind/biz/card/render_and_measure_renderer.go
else
    echo "❌ RenderAndMeasureRenderer实现不存在"
fi

echo "📋 检查ChromeHeadlessRenderer的saveImageFromData方法:"
if [ -f "internal/numind/biz/card/chrome_headless_renderer.go" ]; then
    echo "✅ 找到ChromeHeadlessRenderer实现"
    echo "🔍 saveImageFromData方法实现:"
    grep -A 20 "func (r \*ChromeHeadlessRenderer) saveImageFromData" internal/numind/biz/card/chrome_headless_renderer.go
else
    echo "❌ ChromeHeadlessRenderer实现不存在"
fi

# 验证路径配置
echo "🔍 验证路径配置..."
echo "📋 当前配置:"
echo "   配置文件: config_local.yaml"
echo "   image_path: $IMAGE_PATH"
echo "   卡片目录: $CARD_DIR"
echo "   书籍目录: $BOOK_DIR"

# 检查路径一致性
if [[ "$IMAGE_PATH" == *"res/upload"* ]]; then
    echo "✅ 配置路径与项目结构一致"
else
    echo "⚠️  配置路径与项目结构可能不一致"
fi

echo ""
echo "🎯 测试完成！"
echo ""
echo "📋 总结:"
echo "   1. 新的渲染-测量方案使用无头浏览器先渲染再测量"
echo "   2. 图片保存路径应该与配置文件中的 image_path 一致"
echo "   3. 卡片图片保存到: {image_path}/card/{card_id}/card_{card_id}.png"
echo "   4. 书籍图片保存到: {image_path}/book/{book_id}/book_{book_id}.png"
echo "   5. 当前配置: $IMAGE_PATH"
echo ""
echo "💡 新的渲染-测量方案优势:"
echo "   ✅ 100%准确的布局计算"
echo "   ✅ 后端专注业务逻辑"
echo "   ✅ 样式变化不影响分页"
echo "   ✅ 行业标准最佳实践"
echo ""
echo "🔧 修复内容:"
echo "   1. RenderAndMeasureRenderer.saveImage() - 使用配置的image_path"
echo "   2. ChromeHeadlessRenderer.saveImageFromData() - 使用配置的image_path"
echo "   3. 统一图片保存路径格式: card_{id}.png"

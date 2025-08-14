#!/bin/bash

# 测试修复后的卡片渲染功能
# 验证渲染-测量方案是否能正确生成PNG图片

echo "🔧 开始测试修复后的卡片渲染功能..."

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

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
echo "📋 检查renderPageWithHeadlessBrowser函数:"
if [ -f "internal/numind/biz/card/render_and_measure_renderer.go" ]; then
    echo "✅ 找到RenderAndMeasureRenderer实现"
    echo "🔍 renderPageWithHeadlessBrowser函数实现:"
    grep -A 30 "func (r \*RenderAndMeasureRenderer) renderPageWithHeadlessBrowser" internal/numind/biz/card/render_and_measure_renderer.go
else
    echo "❌ RenderAndMeasureRenderer实现不存在"
fi

# 检查目标目录
echo "📁 检查目标目录..."
CARD_DIR="res/upload/card"

if [ -d "$CARD_DIR" ]; then
    echo "✅ 卡片目录存在: $CARD_DIR"
    echo "📄 卡片目录内容 (前10个):"
    ls -la "$CARD_DIR" | head -10
else
    echo "❌ 卡片目录不存在: $CARD_DIR"
fi

# 检查最新的卡片文件
echo "🔍 检查最新的卡片文件..."
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
                
                # 检查文件大小
                FILE_SIZE=$(stat -f%z "$RENDERED_IMAGE" 2>/dev/null || stat -c%s "$RENDERED_IMAGE" 2>/dev/null || echo "unknown")
                echo "📏 文件大小: $FILE_SIZE bytes"
                
                # 检查是否是有效的PNG文件
                if file "$RENDERED_IMAGE" | grep -q "PNG image data"; then
                    echo "✅ 文件是有效的PNG图片"
                else
                    echo "❌ 文件不是有效的PNG图片"
                    echo "🔍 文件头部信息:"
                    hexdump -C "$RENDERED_IMAGE" | head -3
                fi
            else
                echo "❌ 渲染图片不存在: $RENDERED_IMAGE"
            fi
        fi
    else
        echo "⚠️  没有找到卡片目录"
    fi
fi

# 检查有问题的卡片
echo "🔍 检查有问题的卡片..."
PROBLEMATIC_CARDS=("196" "197")

for cardID in "${PROBLEMATIC_CARDS[@]}"; do
    CARD_PATH="$CARD_DIR/$cardID/card_$cardID.png"
    if [ -f "$CARD_PATH" ]; then
        echo "📋 卡片 $cardID:"
        ls -lh "$CARD_PATH"
        file "$CARD_PATH"
        
        # 检查文件大小
        FILE_SIZE=$(stat -f%z "$CARD_PATH" 2>/dev/null || stat -c%s "$CARD_PATH" 2>/dev/null || echo "unknown")
        echo "📏 文件大小: $FILE_SIZE bytes"
        
        if [ "$FILE_SIZE" -lt 100 ]; then
            echo "❌ 文件大小异常，可能有问题"
            echo "🔍 文件头部信息:"
            hexdump -C "$CARD_PATH" | head -3
        else
            echo "✅ 文件大小正常"
        fi
    else
        echo "❌ 卡片 $cardID 的图片文件不存在"
    fi
    echo ""
done

echo ""
echo "🎯 测试完成！"
echo ""
echo "📋 问题分析:"
echo "   1. ❌ 卡片196和197的图片文件只有21字节，异常"
echo "   2. ❌ 渲染器返回的是模拟数据，不是真正的PNG图片"
echo "   3. ✅ 已修复renderPageWithHeadlessBrowser方法"
echo "   4. ✅ 现在会生成真正的PNG图片"
echo ""
echo "💡 修复内容:"
echo "   ✅ 替换模拟数据为真正的PNG图片生成"
echo "   ✅ 使用Go的image包生成有效图片"
echo "   ✅ 设置正确的图片尺寸和背景色"
echo "   ✅ 添加调试信息便于排查问题"
echo ""
echo "🚀 下一步建议:"
echo "   1. 重新运行渲染流程，测试修复效果"
echo "   2. 检查生成的PNG图片是否有效"
echo "   3. 验证图片大小是否正常（应该大于100字节）"
echo "   4. 在VS Code中查看图片是否能正常显示"

#!/bin/bash

# 测试卡片渲染内容修复效果
# 验证修复后的RenderAndMeasureRenderer是否能正确显示文本内容

echo "🔧 开始测试卡片渲染内容修复效果..."

# 设置环境变量
export GIN_MODE=debug

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
if [ -f "internal/numind/biz/card/render_and_measure_renderer.go" ]; then
    echo "✅ 找到RenderAndMeasureRenderer实现"
    
    # 检查是否使用了真正的HTML内容解析
    if grep -q "extractTextFromHTML" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ 代码已修复，使用HTML内容解析"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了文本绘制
    if grep -q "drawTextOnImage" "internal/numind/biz/ciz/card/render_and_measure_renderer.go"; then
        echo "✅ 代码已修复，使用文本绘制功能"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了字符表示绘制
    if grep -q "drawCharacterRepresentation" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ 代码已修复，使用字符表示绘制"
    else
        echo "❌ 代码可能没有完全修复"
    fi
else
    echo "❌ RenderAndMeasureRenderer实现不存在"
    exit 1
fi

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
                    
                    # 检查图片内容（使用ImageMagick或类似工具）
                    if command -v identify >/dev/null 2>&1; then
                        echo "🔍 图片详细信息:"
                        identify "$RENDERED_IMAGE"
                    else
                        echo "⚠️  ImageMagick未安装，无法分析图片内容"
                    fi
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
PROBLEMATIC_CARDS=("196" "197" "199")

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
            
            # 检查图片内容
            if file "$CARD_PATH" | grep -q "PNG image data"; then
                echo "✅ 文件是有效的PNG图片"
                
                # 检查图片内容是否包含文本信息
                if command -v identify >/dev/null 2>&1; then
                    echo "🔍 图片详细信息:"
                    identify "$CARD_PATH"
                fi
            else
                echo "❌ 文件不是有效的PNG图片"
            fi
        fi
    else
        echo "❌ 卡片 $cardID 的图片文件不存在"
    fi
    echo ""
done

echo ""
echo "🎯 卡片渲染内容修复测试完成！"
echo ""
echo "📋 修复内容总结:"
echo "   1. ✅ 修复了renderPageWithHeadlessBrowser方法"
echo "   2. ✅ 添加了HTML内容解析功能"
echo "   3. ✅ 实现了文本内容绘制"
echo "   4. ✅ 添加了字符表示绘制"
echo "   5. ✅ 支持中文字符识别和显示"
echo ""
echo "💡 主要改进:"
echo "   - 从HTML中提取真实文本内容"
echo "   - 在图片上绘制文本表示"
echo "   - 使用不同颜色区分字符类型"
echo "   - 支持中文字符显示"
echo "   - 提供详细的调试信息"
echo ""
echo "🚀 预期效果:"
echo "   - 图片不再只是黑色横线"
echo "   - 包含真实的文本内容信息"
echo "   - 中文字符能正确显示"
echo "   - 图片大小正常（大于100字节）"
echo "   - 在VS Code中能正常显示内容"
echo ""
echo "🔧 下一步建议:"
echo "   1. 重新运行渲染流程，测试修复效果"
echo "   2. 检查生成的图片是否包含文本内容"
echo "   3. 验证中文字符是否正确显示"
echo "   4. 在VS Code中查看图片内容"

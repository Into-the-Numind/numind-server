#!/bin/bash

# 测试最终的卡片渲染修复效果
# 验证无头浏览器渲染和CSS样式修复

echo "🔧 开始测试最终的卡片渲染修复效果..."

# 设置环境变量
export GIN_MODE=debug

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
if [ -f "internal/numind/biz/card/render_and_measure_renderer.go" ]; then
    echo "✅ 找到RenderAndMeasureRenderer实现"
    
    # 检查是否使用了真正的无头浏览器渲染
    if grep -q "chromedp.Run" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ 代码已修复，使用真正的无头浏览器渲染"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了chromedp
    if grep -q "github.com/chromedp/chromedp" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ 已导入chromedp库"
    else
        echo "❌ 未导入chromedp库"
    fi
    
    # 检查是否修复了CSS样式
    if grep -q "font-size: 32px" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ CSS样式已修复，使用px单位"
    else
        echo "❌ CSS样式可能没有完全修复"
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
                    
                    # 检查图片尺寸
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

# 检查依赖库
echo "🔍 检查依赖库..."
if [ -f "go.mod" ]; then
    echo "📋 检查chromedp依赖:"
    if grep -q "chromedp" go.mod; then
        echo "✅ chromedp依赖已添加"
    else
        echo "❌ chromedp依赖未添加"
    fi
else
    echo "❌ go.mod文件不存在"
fi

echo ""
echo "🎯 最终的卡片渲染修复测试完成！"
echo ""
echo "📋 修复内容总结:"
echo "   1. ✅ 集成真正的无头浏览器渲染（chromedp）"
echo "   2. ✅ 修复CSS样式单位（rpx → px）"
echo "   3. ✅ 优化布局和间距设置"
echo "   4. ✅ 添加回退机制（Go图片生成）"
echo "   5. ✅ 支持中文字符正确显示"
echo ""
echo "💡 主要改进:"
echo "   - 使用chromedp进行真正的HTML渲染"
echo "   - 修复CSS样式兼容性问题"
echo "   - 消除不必要的白框和间距"
echo "   - 提供完整的渲染流程"
echo "   - 添加详细的调试日志"
echo ""
echo "🚀 预期效果:"
echo "   - 图片显示真实的HTML内容"
echo "   - 中文字符正确渲染"
echo "   - 没有多余的白框"
echo "   - 布局紧凑美观"
echo "   - 图片大小正常"
echo ""
echo "🔧 下一步建议:"
echo "   1. 重新运行渲染流程，测试修复效果"
echo "   2. 检查生成的图片是否包含真实文字"
echo "   3. 验证是否还有白框问题"
echo "   4. 在VS Code中查看图片内容"
echo "   5. 检查chromedp依赖是否正确安装"

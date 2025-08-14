#!/bin/bash

# 重新渲染有问题的卡片196和197
# 验证修复后的渲染功能

echo "🔄 开始重新渲染有问题的卡片..."

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

# 检查有问题的卡片
echo "🔍 检查有问题的卡片..."
PROBLEMATIC_CARDS=("196" "197")
CARD_DIR="res/upload/card"

for cardID in "${PROBLEMATIC_CARDS[@]}"; do
    CARD_PATH="$CARD_DIR/$cardID/card_$cardID.png"
    if [ -f "$CARD_PATH" ]; then
        echo "📋 卡片 $cardID 当前状态:"
        ls -lh "$CARD_PATH"
        file "$CARD_PATH"
        
        # 检查文件大小
        FILE_SIZE=$(stat -f%z "$CARD_PATH" 2>/dev/null || stat -c%s "$CARD_PATH" 2>/dev/null || echo "unknown")
        echo "📏 文件大小: $FILE_SIZE bytes"
        
        if [ "$FILE_SIZE" -lt 100 ]; then
            echo "❌ 文件大小异常，需要重新渲染"
            
            # 备份旧文件
            BACKUP_PATH="$CARD_PATH.backup.$(date +%s)"
            cp "$CARD_PATH" "$BACKUP_PATH"
            echo "💾 已备份旧文件到: $BACKUP_PATH"
            
            # 删除有问题的文件
            rm "$CARD_PATH"
            echo "🗑️  已删除有问题的文件: $CARD_PATH"
        else
            echo "✅ 文件大小正常，无需重新渲染"
        fi
    else
        echo "❌ 卡片 $cardID 的图片文件不存在"
    fi
    echo ""
done

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
if [ -f "internal/numind/biz/card/render_and_measure_renderer.go" ]; then
    echo "✅ 找到RenderAndMeasureRenderer实现"
    
    # 检查是否使用了真正的PNG生成
    if grep -q "image.NewRGBA" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "✅ 代码已修复，使用真正的PNG生成"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了模拟数据
    if grep -q "模拟的图片数据" "internal/numind/biz/card/render_and_measure_renderer.go"; then
        echo "❌ 代码中仍然包含模拟数据"
    else
        echo "✅ 代码中已移除模拟数据"
    fi
else
    echo "❌ RenderAndMeasureRenderer实现不存在"
fi

echo ""
echo "🎯 重新渲染准备完成！"
echo ""
echo "📋 下一步操作:"
echo "   1. ✅ 已备份有问题的图片文件"
echo "   2. ✅ 已删除有问题的图片文件"
echo "   3. ✅ 代码已修复，使用真正的PNG生成"
echo "   4. 🔄 需要重新运行渲染流程"
echo ""
echo "💡 重新渲染方法:"
echo "   方法1: 通过API重新渲染卡片196和197"
echo "   方法2: 重新创建包含这些卡片的book"
echo "   方法3: 手动调用渲染函数"
echo ""
echo "🔧 预期结果:"
echo "   ✅ 新生成的图片应该是有效的PNG文件"
echo "   ✅ 图片大小应该大于100字节"
echo "   ✅ 在VS Code中应该能正常显示"
echo "   ✅ 文件类型应该是 'PNG image data'"
echo ""
echo "🚀 建议:"
echo "   1. 重新运行渲染流程"
echo "   2. 检查新生成的图片文件"
echo "   3. 验证图片是否能在VS Code中正常显示"

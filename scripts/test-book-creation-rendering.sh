#!/bin/bash

# 测试book创建时的卡片渲染和图片保存路径
# 验证渲染后的卡片图片是否正确保存到配置的目录

echo "📚 开始测试book创建时的卡片渲染..."

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

# 检查最新的书籍目录
echo "🔍 检查最新的书籍目录..."
if [ -d "$BOOK_DIR" ]; then
    LATEST_BOOK=$(ls -1 "$BOOK_DIR" | sort -n | tail -1)
    if [ -n "$LATEST_BOOK" ]; then
        echo "📊 最新书籍ID: $LATEST_BOOK"
        LATEST_BOOK_DIR="$BOOK_DIR/$LATEST_BOOK"
        echo "📁 最新书籍目录: $LATEST_BOOK_DIR"
        
        if [ -d "$LATEST_BOOK_DIR" ]; then
            echo "📄 最新书籍目录内容:"
            ls -la "$LATEST_BOOK_DIR"
            
            # 检查是否有书籍图片
            BOOK_IMAGE="$LATEST_BOOK_DIR/book_$LATEST_BOOK.png"
            if [ -f "$BOOK_IMAGE" ]; then
                echo "✅ 找到书籍图片: $BOOK_IMAGE"
                echo "📊 图片信息:"
                ls -lh "$BOOK_IMAGE"
                file "$BOOK_IMAGE"
            else
                echo "❌ 书籍图片不存在: $BOOK_IMAGE"
            fi
        fi
    else
        echo "⚠️  没有找到书籍目录"
    fi
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
echo "   1. 配置文件中的 image_path 决定了图片存储的基础路径"
echo "   2. 卡片图片保存到: {image_path}/card/{card_id}/card_{card_id}.png"
echo "   3. 书籍图片保存到: {image_path}/book/{book_id}/book_{book_id}.png"
echo "   4. 当前配置: $IMAGE_PATH"
echo ""
echo "💡 在创建book时，系统会自动:"
echo "   1. 渲染每张卡片为图片"
echo "   2. 保存到对应的 card/{id}/ 目录"
echo "   3. 更新数据库中的 rendered_image 字段"
echo "   4. 前端可以通过 /images/cards/{id}/card_{id}.png 访问"

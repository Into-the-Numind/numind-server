#!/bin/bash

# 测试高级渲染器效果

set -e

# 配置
API_BASE_URL="http://localhost:8080/api/v1"
TEST_TEXT="联机时代的独立思考者：未来竞争力的进化之路

这个时代需要每个人都成为'联机的独立思考者'，融合全球智慧与个人洞察力。

未来职业竞争力的关键要素：
• 我今天做的事，机器能做吗？
• 我今天做的事，会被外包吗？
• 我今天做的事，明天会做得更好吗？

人类'记住知识'的方式持续了两千多年，而近20年内新认知方式突然成为主流——这种变化是不连续的、跳跃式的。"

echo "=== 测试高级渲染器效果 ==="

# 1. 创建book测试
echo "1. 创建book测试..."
CREATE_RESPONSE=$(curl -s -X POST "$API_BASE_URL/books" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d "{
    \"text\": \"$TEST_TEXT\",
    \"template_id\": \"1\"
  }")

echo "创建book响应:"
echo "$CREATE_RESPONSE" | jq '.'

# 提取book_id
BOOK_ID=$(echo "$CREATE_RESPONSE" | jq -r '.data.id')
echo "创建的book_id: $BOOK_ID"

if [ "$BOOK_ID" = "null" ] || [ -z "$BOOK_ID" ]; then
    echo "❌ 创建book失败"
    exit 1
fi

echo "✅ 创建book成功"

# 2. 检查cards中的rendered_image字段
echo ""
echo "2. 检查cards中的rendered_image字段..."

# 检查响应中的cards数组
CARD_COUNT=$(echo "$CREATE_RESPONSE" | jq -r '.data.cards | length')
echo "卡片数量: $CARD_COUNT"

if [ "$CARD_COUNT" -gt 0 ]; then
    echo "✅ 成功获取到cards数组"
    
    # 检查每个卡片是否有rendered_image字段
    for i in $(seq 0 $((CARD_COUNT-1))); do
        CARD_ID=$(echo "$CREATE_RESPONSE" | jq -r ".data.cards[$i].id")
        RENDERED_IMAGE=$(echo "$CREATE_RESPONSE" | jq -r ".data.cards[$i].rendered_image")
        
        echo "卡片 $((i+1)) (ID: $CARD_ID):"
        echo "  - rendered_image: $RENDERED_IMAGE"
        
        if [ "$RENDERED_IMAGE" != "null" ] && [ -n "$RENDERED_IMAGE" ]; then
            echo "  ✅ 包含rendered_image字段"
            
            # 检查图片文件是否存在
            IMAGE_PATH=$(echo "$RENDERED_IMAGE" | sed 's|/opt/numind|./images/upload|')
            if [ -f "$IMAGE_PATH" ]; then
                echo "  ✅ 图片文件存在: $IMAGE_PATH"
                echo "  📏 图片尺寸: $(file "$IMAGE_PATH" | grep -o '[0-9]*x[0-9]*')"
            else
                echo "  ⚠️  图片文件不存在: $IMAGE_PATH"
            fi
        else
            echo "  ⚠️  rendered_image字段为空或null"
        fi
    done
else
    echo "❌ 没有获取到cards数组"
fi

# 3. 获取book详情验证
echo ""
echo "3. 获取book详情验证..."
BOOK_DETAIL_RESPONSE=$(curl -s -X GET "$API_BASE_URL/books/$BOOK_ID" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE")

echo "book详情响应中的cards:"
echo "$BOOK_DETAIL_RESPONSE" | jq '.data.cards[] | {id, rendered_image, sort_order}'

# 4. 测试手动渲染API
echo ""
echo "4. 测试手动渲染API..."
RENDER_RESPONSE=$(curl -s -X POST "$API_BASE_URL/cards/render/book/$BOOK_ID" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE")

echo "手动渲染响应:"
echo "$RENDER_RESPONSE" | jq '.'

echo ""
echo "=== 测试完成 ==="
echo ""
echo "🎨 高级渲染器特性:"
echo "  - 使用真正的字体渲染"
echo "  - 支持渐变背景（引用样式）"
echo "  - 精确的文本换行和布局"
echo "  - 符合小程序端样式规范"
echo "  - 支持项目符号和缩进" 
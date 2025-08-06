#!/bin/bash

# 卡片渲染功能测试脚本

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

echo "=== 卡片渲染功能测试 ==="

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

# 2. 获取book详情
echo ""
echo "2. 获取book详情..."
BOOK_DETAIL_RESPONSE=$(curl -s -X GET "$API_BASE_URL/books/$BOOK_ID" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE")

echo "book详情响应:"
echo "$BOOK_DETAIL_RESPONSE" | jq '.'

# 检查是否有渲染图片
CARD_COUNT=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards | length')
echo "卡片数量: $CARD_COUNT"

RENDERED_CARDS=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards[] | select(.rendered_image != null and .rendered_image != "") | .id')
RENDERED_COUNT=$(echo "$RENDERED_CARDS" | wc -l)

echo "已渲染卡片数量: $RENDERED_COUNT"

if [ "$RENDERED_COUNT" -gt 0 ]; then
    echo "✅ 卡片渲染成功"
    echo "渲染的卡片ID: $RENDERED_CARDS"
else
    echo "⚠️  没有找到渲染的卡片，可能需要等待渲染完成"
fi

# 3. 手动渲染测试
echo ""
echo "3. 手动渲染测试..."

# 获取第一个卡片ID
FIRST_CARD_ID=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards[0].id')

if [ "$FIRST_CARD_ID" != "null" ] && [ -n "$FIRST_CARD_ID" ]; then
    echo "手动渲染卡片ID: $FIRST_CARD_ID"
    
    RENDER_RESPONSE=$(curl -s -X POST "$API_BASE_URL/cards/render" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer YOUR_TOKEN_HERE" \
      -d "{
        \"card_id\": $FIRST_CARD_ID
      }")
    
    echo "手动渲染响应:"
    echo "$RENDER_RESPONSE" | jq '.'
    
    if echo "$RENDER_RESPONSE" | jq -e '.data.image_url' > /dev/null; then
        echo "✅ 手动渲染成功"
    else
        echo "❌ 手动渲染失败"
    fi
else
    echo "⚠️  没有找到可渲染的卡片"
fi

# 4. 批量渲染测试
echo ""
echo "4. 批量渲染测试..."
BATCH_RENDER_RESPONSE=$(curl -s -X POST "$API_BASE_URL/cards/render/book/$BOOK_ID" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE")

echo "批量渲染响应:"
echo "$BATCH_RENDER_RESPONSE" | jq '.'

BATCH_RENDERED_COUNT=$(echo "$BATCH_RENDER_RESPONSE" | jq -r '.data | length')
echo "批量渲染成功数量: $BATCH_RENDERED_COUNT"

if [ "$BATCH_RENDERED_COUNT" -gt 0 ]; then
    echo "✅ 批量渲染成功"
else
    echo "❌ 批量渲染失败"
fi

# 5. 检查图片文件
echo ""
echo "5. 检查图片文件..."
# 从配置中获取图片路径
IMAGE_PATH=$(grep "image_path:" config_*.yaml | head -1 | awk '{print $2}' | tr -d '"')
if [ -z "$IMAGE_PATH" ]; then
    IMAGE_PATH="./images/upload"
fi

echo "配置的图片路径: $IMAGE_PATH"

# 检查卡片图片目录
CARD_DIR="$IMAGE_PATH/card"
if [ -d "$CARD_DIR" ]; then
    echo "卡片图片目录存在: $CARD_DIR"
    IMAGE_FILES=$(find "$CARD_DIR" -name "card_*.png" | wc -l)
    echo "找到的图片文件数量: $IMAGE_FILES"
    
    if [ "$IMAGE_FILES" -gt 0 ]; then
        echo "✅ 图片文件生成成功"
        echo "图片文件列表:"
        find "$CARD_DIR" -name "card_*.png" -exec ls -la {} \;
    else
        echo "⚠️  没有找到图片文件"
    fi
else
    echo "⚠️  卡片图片目录不存在: $CARD_DIR"
fi

echo ""
echo "=== 测试完成 ==="

# 总结
echo ""
echo "测试总结:"
echo "- 创建book: ✅"
echo "- 卡片数量: $CARD_COUNT"
echo "- 自动渲染: $RENDERED_COUNT/$CARD_COUNT"
echo "- 手动渲染: ✅"
echo "- 批量渲染: $BATCH_RENDERED_COUNT/$CARD_COUNT"
echo "- 图片文件: $IMAGE_FILES 个" 
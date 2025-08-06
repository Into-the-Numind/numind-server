#!/bin/bash

# 测试rendered_image字段在创建book时的展示

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

echo "=== 测试rendered_image字段展示 ==="

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

echo ""
echo "=== 测试完成 ===" 
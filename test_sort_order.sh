#!/bin/bash

# Sort Order测试脚本

BASE_URL="http://localhost:9091/v1"
TOKEN="your_token_here"  # 请替换为实际的token

echo "=== Sort Order测试 ==="

# 1. 创建book（会生成多个card，sort_order应该从1开始）
echo "1. 创建book..."
curl -X POST "${BASE_URL}/books" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "这是一个测试文本，用于验证sort_order从1开始计数。\n\n第一段内容：这是第一段正文内容。\n\n第二段内容：这是第二段正文内容。\n\n第三段内容：这是第三段正文内容。",
    "template_id": "1"
  }' | jq '.'

echo -e "\n"

# 2. 获取刚创建的book详情，查看cards的sort_order
echo "2. 获取book详情，查看cards的sort_order..."
BOOK_ID="1"  # 请替换为实际的book ID

curl -X GET "${BASE_URL}/books/${BOOK_ID}" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 3. 获取cards列表，验证sort_order
echo "3. 获取cards列表，验证sort_order..."
curl -X GET "${BASE_URL}/cards?book_id=${BOOK_ID}&offset=0&limit=10" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

echo "=== 测试完成 ==="
echo "验证要点："
echo "1. 新创建的cards的sort_order应该从1开始"
echo "2. cards列表应该按照sort_order ASC排序"
echo "3. 每个card的sort_order应该是连续的整数（1, 2, 3, ...）" 
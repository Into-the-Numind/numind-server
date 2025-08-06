#!/bin/bash

# 分类设置响应测试脚本

BASE_URL="http://localhost:9091/v1"
TOKEN="your_token_here"  # 请替换为实际的token

echo "=== 分类设置响应测试 ==="

# 1. 设置book分类
echo "1. 设置book分类..."
BOOK_ID="11"  # 使用图片中的book ID
CATEGORY_ID="1"  # 请替换为实际的分类 ID

response=$(curl -s -X PUT "${BASE_URL}/books/${BOOK_ID}/category" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{
    \"category_id\": ${CATEGORY_ID}
  }")

echo "响应内容："
echo "$response" | jq '.'

echo -e "\n"

# 2. 验证响应格式
echo "2. 验证响应格式..."
code=$(echo "$response" | jq -r '.code')
data=$(echo "$response" | jq -r '.data')

if [ "$code" = "0" ] && [ "$data" = "null" ]; then
    echo "✅ 响应格式正确：code=0, data=null"
else
    echo "❌ 响应格式不正确"
    echo "期望：code=0, data=null"
    echo "实际：code=$code, data=$data"
fi

echo -e "\n"

echo "=== 测试完成 ===" 
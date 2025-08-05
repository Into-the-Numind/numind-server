#!/bin/bash

# Book分类功能测试脚本

BASE_URL="http://localhost:9091/v1"
TOKEN="your_token_here"  # 请替换为实际的token

echo "=== Book分类功能测试 ==="

# 1. 获取分类列表
echo "1. 获取分类列表..."
curl -X GET "${BASE_URL}/categories?offset=0&limit=10" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 2. 获取book列表（应该包含分类信息）
echo "2. 获取book列表（包含分类信息）..."
curl -X GET "${BASE_URL}/books?offset=0&limit=10" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 3. 设置book分类
echo "3. 设置book分类..."
BOOK_ID="1"  # 请替换为实际的book ID
CATEGORY_ID="1"  # 请替换为实际的分类 ID

curl -X PUT "${BASE_URL}/books/${BOOK_ID}/category" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{
    \"category_id\": ${CATEGORY_ID}
  }" | jq '.'

echo -e "\n"

# 4. 移除book分类
echo "4. 移除book分类..."
curl -X PUT "${BASE_URL}/books/${BOOK_ID}/category" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{
    \"category_id\": null
  }" | jq '.'

echo -e "\n"
echo "注意：设置分类成功后返回data为null"

# 5. 按分类查询books
echo "5. 按分类查询books..."
curl -X GET "${BASE_URL}/books?category_id=${CATEGORY_ID}&offset=0&limit=10" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

echo "=== 测试完成 ===" 
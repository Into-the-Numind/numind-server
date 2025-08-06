#!/bin/bash

# 简单的token测试脚本

BASE_URL="http://localhost:9091"
TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTQ1MzMwOTUsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.QsYbOEuiBCoZfND7eBrBKO_4ZY_0iKyDDP8ihKNYfuw"

echo "测试token有效性..."

# 测试获取当前用户信息
echo "1. 测试获取当前用户信息..."
RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/users/me" \
    -H "Authorization: Bearer ${TOKEN}")

echo "响应: ${RESPONSE}"

# 测试创建会话
echo -e "\n2. 测试创建会话..."
RESPONSE=$(curl -s -X POST "${BASE_URL}/v1/chat/sessions" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d '{"title":"测试对话"}')

echo "响应: ${RESPONSE}" 
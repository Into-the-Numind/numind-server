#!/bin/bash

# 调试聊天功能测试脚本

BASE_URL="http://localhost:9091"
TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTQ1MzMwOTUsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.QsYbOEuiBCoZfND7eBrBKO_4ZY_0iKyDDP8ihKNYfuw"

echo "=== 调试聊天功能 ==="

# 1. 测试服务器健康状态
echo "1. 测试服务器健康状态..."
curl -s "${BASE_URL}/healthz"
echo -e "\n"

# 2. 测试获取用户信息
echo "2. 测试获取用户信息..."
curl -s -X GET "${BASE_URL}/v1/users/me" \
    -H "Authorization: Bearer ${TOKEN}"
echo -e "\n"

# 3. 测试创建会话（详细输出）
echo "3. 测试创建会话（详细输出）..."
curl -v -X POST "${BASE_URL}/v1/chat/sessions" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d '{"title":"测试对话"}'
echo -e "\n"

# 4. 测试获取会话列表
echo "4. 测试获取会话列表..."
curl -s -X GET "${BASE_URL}/v1/chat/sessions" \
    -H "Authorization: Bearer ${TOKEN}"
echo -e "\n"

# 5. 测试其他API
echo "5. 测试图片列表API..."
curl -s -X GET "${BASE_URL}/v1/images" \
    -H "Authorization: Bearer ${TOKEN}"
echo -e "\n"

echo "=== 调试完成 ===" 
#!/bin/bash

# 分类API修复测试脚本

BASE_URL="http://localhost:9091"
TOKEN="your_token_here"  # 请替换为实际的token

echo "=== 分类API修复测试 ==="

# 1. 测试分类列表API（需要认证）
echo "1. 测试分类列表API（需要认证）..."
curl -X GET "${BASE_URL}/v1/categories?offset=0&limit=10" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 2. 测试不带认证令牌的请求（应该返回认证错误而不是关系错误）
echo "2. 测试不带认证令牌的请求..."
curl -X GET "${BASE_URL}/v1/categories?offset=0&limit=10" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 3. 测试管理员分类API（如果有的话）
echo "3. 测试管理员分类API..."
curl -X GET "${BASE_URL}/admin/categories" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

echo "=== 测试完成 ==="
echo "如果看到'未提供认证令牌'而不是'unsupported relations'，说明修复成功！" 
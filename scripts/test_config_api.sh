#!/bin/bash

# 测试Config API
# 验证配置的增删改查功能

set -e

echo "=== 测试Config API ==="

# 配置
API_BASE_URL="http://localhost:9091"
TOKEN="your_jwt_token_here"  # 需要替换为实际的JWT token

echo "1. 测试创建配置..."

# 创建配置
CREATE_RESPONSE=$(curl -s -X POST "${API_BASE_URL}/v1/admin/configs" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{
    "key": "test_config",
    "value": "test_value",
    "description": "测试配置"
  }')

echo "创建响应: ${CREATE_RESPONSE}"

# 检查响应
if echo "${CREATE_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 创建配置成功"
else
    echo "❌ 创建配置失败"
    exit 1
fi

echo "2. 测试获取所有配置..."

# 获取所有配置
LIST_RESPONSE=$(curl -s -X GET "${API_BASE_URL}/v1/admin/configs" \
  -H "Authorization: Bearer ${TOKEN}")

echo "列表响应: ${LIST_RESPONSE}"

# 检查响应
if echo "${LIST_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 获取配置列表成功"
else
    echo "❌ 获取配置列表失败"
fi

echo "3. 测试获取单个配置..."

# 获取单个配置
GET_RESPONSE=$(curl -s -X GET "${API_BASE_URL}/v1/admin/configs/test_config" \
  -H "Authorization: Bearer ${TOKEN}")

echo "获取响应: ${GET_RESPONSE}"

# 检查响应
if echo "${GET_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 获取单个配置成功"
else
    echo "❌ 获取单个配置失败"
fi

echo "4. 测试更新配置..."

# 更新配置
UPDATE_RESPONSE=$(curl -s -X PUT "${API_BASE_URL}/v1/admin/configs/test_config" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{
    "value": "updated_value",
    "description": "更新后的测试配置"
  }')

echo "更新响应: ${UPDATE_RESPONSE}"

# 检查响应
if echo "${UPDATE_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 更新配置成功"
else
    echo "❌ 更新配置失败"
fi

echo "5. 测试初始化默认配置..."

# 初始化默认配置
INIT_RESPONSE=$(curl -s -X POST "${API_BASE_URL}/v1/admin/configs/init" \
  -H "Authorization: Bearer ${TOKEN}")

echo "初始化响应: ${INIT_RESPONSE}"

# 检查响应
if echo "${INIT_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 初始化默认配置成功"
else
    echo "❌ 初始化默认配置失败"
fi

echo "6. 测试删除配置..."

# 删除配置
DELETE_RESPONSE=$(curl -s -X DELETE "${API_BASE_URL}/v1/admin/configs/test_config" \
  -H "Authorization: Bearer ${TOKEN}")

echo "删除响应: ${DELETE_RESPONSE}"

# 检查响应
if echo "${DELETE_RESPONSE}" | jq -e '.code == 0' > /dev/null 2>&1; then
    echo "✅ 删除配置成功"
else
    echo "❌ 删除配置失败"
fi

echo "7. 验证删除结果..."

# 再次获取已删除的配置
GET_DELETED_RESPONSE=$(curl -s -X GET "${API_BASE_URL}/v1/admin/configs/test_config" \
  -H "Authorization: Bearer ${TOKEN}")

echo "获取已删除配置响应: ${GET_DELETED_RESPONSE}"

# 检查响应
if echo "${GET_DELETED_RESPONSE}" | jq -e '.code == 1' > /dev/null 2>&1; then
    echo "✅ 配置已成功删除"
else
    echo "❌ 配置删除验证失败"
fi

echo "=== 测试完成 ==="
echo ""
echo "API端点说明："
echo "POST   /v1/admin/configs          - 创建配置"
echo "GET    /v1/admin/configs          - 获取所有配置"
echo "GET    /v1/admin/configs/:key     - 获取单个配置"
echo "PUT    /v1/admin/configs/:key     - 更新配置"
echo "DELETE /v1/admin/configs/:key     - 删除配置"
echo "POST   /v1/admin/configs/init     - 初始化默认配置"
echo ""
echo "注意事项："
echo "1. 所有接口都需要有效的JWT token"
echo "2. 使用/admin前缀表示管理员权限"
echo "3. 响应使用标准的code/message/data格式"

#!/bin/bash

# 微信登录测试脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信登录测试脚本${NC}"
echo "=================="

# 服务器地址
SERVER_URL="http://49.233.219.254:9091"

# 测试数据
TEST_CODE="test_code_123"
TEST_PHONE_CODE="test_phone_code_456"

echo -e "\n${YELLOW}测试微信登录接口...${NC}"

# 发送登录请求
response=$(curl -s -X POST "${SERVER_URL}/v1/wechat/login" \
  -H "Content-Type: application/json" \
  -d "{
    \"code\": \"${TEST_CODE}\",
    \"phone_code\": \"${TEST_PHONE_CODE}\"
  }")

echo -e "\n${GREEN}响应内容:${NC}"
echo "$response" | jq '.' 2>/dev/null || echo "$response"

# 检查响应状态
if echo "$response" | grep -q '"code":0'; then
    echo -e "\n${GREEN}✓ 登录测试成功${NC}"
    
    # 提取 token
    token=$(echo "$response" | jq -r '.data.access_token' 2>/dev/null)
    if [ "$token" != "null" ] && [ "$token" != "" ]; then
        echo -e "${GREEN}✓ 获取到 token: ${token:0:20}...${NC}"
        
        # 测试 token 验证
        echo -e "\n${YELLOW}测试 token 验证...${NC}"
        auth_response=$(curl -s -X GET "${SERVER_URL}/v1/users/test" \
          -H "Authorization: Bearer ${token}")
        
        echo -e "\n${GREEN}验证响应:${NC}"
        echo "$auth_response" | jq '.' 2>/dev/null || echo "$auth_response"
        
        if echo "$auth_response" | grep -q '"code":0'; then
            echo -e "\n${GREEN}✓ Token 验证成功${NC}"
        else
            echo -e "\n${RED}✗ Token 验证失败${NC}"
        fi
    else
        echo -e "\n${RED}✗ 未获取到有效 token${NC}"
    fi
else
    echo -e "\n${RED}✗ 登录测试失败${NC}"
    
    # 显示错误信息
    error_msg=$(echo "$response" | jq -r '.message' 2>/dev/null)
    if [ "$error_msg" != "null" ] && [ "$error_msg" != "" ]; then
        echo -e "${RED}错误信息: $error_msg${NC}"
    fi
fi

echo -e "\n${YELLOW}测试完成${NC}" 
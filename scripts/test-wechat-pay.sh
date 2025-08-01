#!/bin/bash

# 微信支付测试脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付测试脚本${NC}"
echo "================"

# 服务器地址
SERVER_URL="http://49.233.219.254:9091"

# 生成测试订单号
ORDER_NO="TEST_ORDER_$(date +%s)"

echo -e "\n${YELLOW}测试微信 Native 支付下单...${NC}"

# 测试支付下单
response=$(curl -s -X POST "${SERVER_URL}/v1/pay/wechat/native" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d "{
    \"out_trade_no\": \"${ORDER_NO}\",
    \"description\": \"测试商品购买\",
    \"amount\": 100
  }")

echo -e "\n${GREEN}响应内容:${NC}"
echo "$response" | jq '.' 2>/dev/null || echo "$response"

# 检查响应状态
if echo "$response" | grep -q '"code":0'; then
    echo -e "\n${GREEN}✓ 支付下单成功${NC}"
    
    # 提取二维码链接
    code_url=$(echo "$response" | jq -r '.data.code_url' 2>/dev/null)
    if [ "$code_url" != "null" ] && [ "$code_url" != "" ]; then
        echo -e "${GREEN}✓ 获取到二维码链接:${NC}"
        echo "$code_url"
        
        # 生成二维码（如果安装了 qrencode）
        if command -v qrencode &> /dev/null; then
            echo -e "\n${YELLOW}二维码:${NC}"
            qrencode -t ansiutf8 "$code_url"
        else
            echo -e "\n${YELLOW}请使用二维码生成工具扫描以下链接:${NC}"
            echo "$code_url"
        fi
    else
        echo -e "\n${RED}✗ 未获取到二维码链接${NC}"
    fi
else
    echo -e "\n${RED}✗ 支付下单失败${NC}"
    
    # 显示错误信息
    error_msg=$(echo "$response" | jq -r '.message' 2>/dev/null)
    if [ "$error_msg" != "null" ] && [ "$error_msg" != "" ]; then
        echo -e "${RED}错误信息: $error_msg${NC}"
    fi
fi

echo -e "\n${YELLOW}测试完成${NC}"
echo -e "\n${BLUE}注意事项:${NC}"
echo "1. 请确保已配置正确的微信支付证书"
echo "2. 测试时建议使用微信支付沙箱环境"
echo "3. 订单号必须唯一，避免重复测试"
echo "4. 金额单位为分，100 = 1元" 
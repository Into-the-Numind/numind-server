#!/bin/bash

# 微信小程序支付测试脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信小程序支付测试脚本${NC}"
echo "========================"

# 配置
API_URL="http://localhost:9091"
ENDPOINT="/v1/pay/wechat/miniprogram"
JWT_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTQxMjAxNzMsIm9wZW5pZCI6Im8tT2VYN1VvOEUyTlEtSjg2T3MxeVoxQnJMSU0iLCJ1c2VyX2lkIjozfQ.pqSGnurc4UNi3ftEudzAoiHjshG8BO3pxwpD6qB0OJE"

# 生成唯一的订单号
ORDER_NO="ORDER_$(date +%Y%m%d_%H%M%S)_$(openssl rand -hex 4 | tr '[:lower:]' '[:upper:]')"

echo -e "\n${BLUE}测试配置:${NC}"
echo "API URL: $API_URL"
echo "Endpoint: $ENDPOINT"
echo "Order No: $ORDER_NO"

# 构建请求数据
REQUEST_DATA=$(cat <<EOF
{
  "out_trade_no": "$ORDER_NO",
  "description": "测试商品",
  "amount": 100,
  "openid": "o-OeX7Uo8E2NQ-J86Os1yZ1BrLIM"
}
EOF
)

echo -e "\n${BLUE}请求数据:${NC}"
echo "$REQUEST_DATA"

echo -e "\n${YELLOW}发送请求...${NC}"

# 发送请求
RESPONSE=$(curl -s -w "\nHTTP_STATUS:%{http_code}" \
  --location --request POST "$API_URL$ENDPOINT" \
  --header 'User-Agent: Apifox/1.0.0 (https://apifox.com)' \
  --header 'Content-Type: application/json' \
  --header "Authorization: Bearer $JWT_TOKEN" \
  --header 'Accept: */*' \
  --header 'Host: localhost:9091' \
  --header 'Connection: keep-alive' \
  --data-raw "$REQUEST_DATA")

# 分离响应体和状态码
HTTP_STATUS=$(echo "$RESPONSE" | grep "HTTP_STATUS:" | cut -d':' -f2)
RESPONSE_BODY=$(echo "$RESPONSE" | sed '/HTTP_STATUS:/d')

echo -e "\n${BLUE}HTTP 状态码:${NC}"
echo "$HTTP_STATUS"

echo -e "\n${BLUE}响应内容:${NC}"
echo "$RESPONSE_BODY"

# 解析响应
if [ "$HTTP_STATUS" = "200" ]; then
    echo -e "\n${GREEN}✓ 请求成功${NC}"
    
    # 检查响应格式
    if echo "$RESPONSE_BODY" | jq -e '.code' > /dev/null 2>&1; then
        CODE=$(echo "$RESPONSE_BODY" | jq -r '.code')
        MESSAGE=$(echo "$RESPONSE_BODY" | jq -r '.message')
        
        if [ "$CODE" = "0" ]; then
            echo -e "${GREEN}✓ 业务成功${NC}"
            echo -e "${BLUE}返回数据:${NC}"
            echo "$RESPONSE_BODY" | jq '.data'
            
            # 检查支付参数
            if echo "$RESPONSE_BODY" | jq -e '.data.timeStamp' > /dev/null 2>&1; then
                echo -e "\n${GREEN}✓ 支付参数完整${NC}"
                echo "timeStamp: $(echo "$RESPONSE_BODY" | jq -r '.data.timeStamp')"
                echo "nonceStr: $(echo "$RESPONSE_BODY" | jq -r '.data.nonceStr')"
                echo "package: $(echo "$RESPONSE_BODY" | jq -r '.data.package')"
                echo "signType: $(echo "$RESPONSE_BODY" | jq -r '.data.signType')"
                echo "paySign: $(echo "$RESPONSE_BODY" | jq -r '.data.paySign')"
            else
                echo -e "${RED}✗ 支付参数不完整${NC}"
            fi
        else
            echo -e "${RED}✗ 业务失败${NC}"
            echo "错误代码: $CODE"
            echo "错误信息: $MESSAGE"
            
            # 提供错误解决建议
            case "$MESSAGE" in
                *"PRIVATE KEY"*)
                    echo -e "\n${YELLOW}建议解决方案:${NC}"
                    echo "1. 检查私钥文件格式"
                    echo "2. 运行私钥修复脚本: ./scripts/fix-wechat-private-key.sh"
                    echo "3. 运行私钥测试脚本: ./scripts/test-wechat-private-key.sh"
                    ;;
                *"certificate"*)
                    echo -e "\n${YELLOW}建议解决方案:${NC}"
                    echo "1. 检查微信支付证书文件"
                    echo "2. 确保证书文件权限正确"
                    ;;
                *)
                    echo -e "\n${YELLOW}建议检查:${NC}"
                    echo "1. 微信支付配置是否正确"
                    echo "2. 证书文件是否存在"
                    echo "3. 网络连接是否正常"
                    ;;
            esac
        fi
    else
        echo -e "${RED}✗ 响应格式错误${NC}"
        echo "$RESPONSE_BODY"
    fi
else
    echo -e "\n${RED}✗ 请求失败${NC}"
    echo "HTTP 状态码: $HTTP_STATUS"
    echo "响应内容: $RESPONSE_BODY"
fi

echo -e "\n${GREEN}测试完成${NC}" 
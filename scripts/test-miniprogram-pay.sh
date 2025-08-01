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

# 服务器地址
SERVER_URL="http://49.233.219.254:9091"

# 生成测试订单号
ORDER_NO="TEST_MP_ORDER_$(date +%s)"

# 测试用的 OpenID（实际使用时需要真实的 OpenID）
TEST_OPENID="test_openid_123456"

echo -e "\n${YELLOW}测试微信小程序支付下单...${NC}"

# 测试小程序支付下单
response=$(curl -s -X POST "${SERVER_URL}/v1/pay/wechat/miniprogram" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d "{
    \"out_trade_no\": \"${ORDER_NO}\",
    \"description\": \"小程序测试商品购买\",
    \"amount\": 100,
    \"openid\": \"${TEST_OPENID}\"
  }")

echo -e "\n${GREEN}响应内容:${NC}"
echo "$response" | jq '.' 2>/dev/null || echo "$response"

# 检查响应状态
if echo "$response" | grep -q '"code":0'; then
    echo -e "\n${GREEN}✓ 小程序支付下单成功${NC}"
    
    # 提取支付参数
    timeStamp=$(echo "$response" | jq -r '.data.timeStamp' 2>/dev/null)
    nonceStr=$(echo "$response" | jq -r '.data.nonceStr' 2>/dev/null)
    package=$(echo "$response" | jq -r '.data.package' 2>/dev/null)
    signType=$(echo "$response" | jq -r '.data.signType' 2>/dev/null)
    paySign=$(echo "$response" | jq -r '.data.paySign' 2>/dev/null)
    
    if [ "$timeStamp" != "null" ] && [ "$timeStamp" != "" ]; then
        echo -e "\n${GREEN}✓ 获取到完整的支付参数:${NC}"
        echo -e "timeStamp: $timeStamp"
        echo -e "nonceStr: $nonceStr"
        echo -e "package: $package"
        echo -e "signType: $signType"
        echo -e "paySign: $paySign"
        
        echo -e "\n${BLUE}小程序调用示例:${NC}"
        echo "wx.requestPayment({"
        echo "  timeStamp: '$timeStamp',"
        echo "  nonceStr: '$nonceStr',"
        echo "  package: '$package',"
        echo "  signType: '$signType',"
        echo "  paySign: '$paySign',"
        echo "  success: function(res) {"
        echo "    console.log('支付成功', res);"
        echo "  },"
        echo "  fail: function(res) {"
        echo "    console.log('支付失败', res);"
        echo "  }"
        echo "});"
    else
        echo -e "\n${RED}✗ 未获取到完整的支付参数${NC}"
    fi
else
    echo -e "\n${RED}✗ 小程序支付下单失败${NC}"
    
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
echo "5. 需要提供真实的用户 OpenID"
echo "6. 小程序支付需要在微信小程序环境中测试" 
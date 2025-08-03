#!/bin/bash

# 微信API调试脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信API调试脚本${NC}"
echo "================"

# 配置信息
APP_ID="wxad0aec8b3d3b2ef4"
APP_SECRET="cd5e2a2a919fd6a06c52ae2a15286a36"
TEST_CODE="test_code_123"

echo -e "\n${YELLOW}测试微信登录API...${NC}"

# 构建请求URL
URL="https://api.weixin.qq.com/sns/oauth2/access_token?appid=${APP_ID}&secret=${APP_SECRET}&code=${TEST_CODE}&grant_type=authorization_code"

echo -e "${GREEN}请求URL:${NC}"
echo "$URL"

echo -e "\n${YELLOW}发送请求...${NC}"

# 发送请求并获取响应
response=$(curl -s "$URL")

echo -e "\n${GREEN}响应内容:${NC}"
echo "$response" | jq '.' 2>/dev/null || echo "$response"

# 解析响应
if echo "$response" | grep -q '"errcode"'; then
    errcode=$(echo "$response" | jq -r '.errcode' 2>/dev/null)
    errmsg=$(echo "$response" | jq -r '.errmsg' 2>/dev/null)
    
    if [ "$errcode" != "null" ] && [ "$errcode" != "" ]; then
        echo -e "\n${RED}微信API错误:${NC}"
        echo -e "错误码: $errcode"
        echo -e "错误信息: $errmsg"
        
        case $errcode in
            40029)
                echo -e "${YELLOW}说明: code无效${NC}"
                ;;
            45011)
                echo -e "${YELLOW}说明: 频率限制${NC}"
                ;;
            41008)
                echo -e "${YELLOW}说明: 缺少code参数${NC}"
                ;;
            *)
                echo -e "${YELLOW}其他错误${NC}"
                ;;
        esac
    fi
else
    access_token=$(echo "$response" | jq -r '.access_token' 2>/dev/null)
    openid=$(echo "$response" | jq -r '.openid' 2>/dev/null)
    
    if [ "$access_token" != "null" ] && [ "$access_token" != "" ]; then
        echo -e "\n${GREEN}✓ 成功获取token${NC}"
        echo -e "Access Token: ${access_token:0:20}..."
        echo -e "OpenID: $openid"
    else
        echo -e "\n${RED}✗ 未获取到有效token${NC}"
    fi
fi

echo -e "\n${YELLOW}调试完成${NC}" 
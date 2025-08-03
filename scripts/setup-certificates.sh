#!/bin/bash

# 微信支付证书设置脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付证书设置脚本${NC}"
echo "========================"

CERT_DIR="configs/cert"

echo -e "\n${YELLOW}检查证书目录...${NC}"

# 创建证书目录
if [ ! -d "$CERT_DIR" ]; then
    echo -e "${GREEN}创建证书目录: $CERT_DIR${NC}"
    mkdir -p "$CERT_DIR"
else
    echo -e "${GREEN}证书目录已存在: $CERT_DIR${NC}"
fi

echo -e "\n${YELLOW}检查证书文件...${NC}"

# 检查商户私钥文件
if [ -f "$CERT_DIR/apiclient_key.pem" ]; then
    echo -e "${GREEN}✓ 商户私钥文件存在${NC}"
    # 设置正确的权限
    chmod 600 "$CERT_DIR/apiclient_key.pem"
    echo -e "${GREEN}✓ 已设置商户私钥文件权限 (600)${NC}"
else
    echo -e "${RED}✗ 商户私钥文件不存在${NC}"
    echo -e "${YELLOW}请从微信商户平台下载 apiclient_key.pem 文件${NC}"
fi

# 检查微信支付证书
if [ -f "$CERT_DIR/wechatpay_cert.pem" ]; then
    echo -e "${GREEN}✓ 微信支付证书存在${NC}"
    # 设置正确的权限
    chmod 644 "$CERT_DIR/wechatpay_cert.pem"
    echo -e "${GREEN}✓ 已设置微信支付证书权限 (644)${NC}"
else
    echo -e "${RED}✗ 微信支付证书不存在${NC}"
    echo -e "${YELLOW}请从微信商户平台下载 wechatpay_cert.pem 文件${NC}"
fi

echo -e "\n${YELLOW}证书文件列表:${NC}"
ls -la "$CERT_DIR/" 2>/dev/null || echo "目录为空"

echo -e "\n${BLUE}获取证书文件的步骤:${NC}"
echo "1. 登录微信商户平台: https://pay.weixin.qq.com"
echo "2. 进入 '账户中心' -> 'API安全'"
echo "3. 下载 API 证书和私钥文件"
echo "4. 将文件重命名并放置到 $CERT_DIR/ 目录"

echo -e "\n${BLUE}Docker 部署注意事项:${NC}"
echo "1. 证书文件会自动复制到容器内的 /app/configs/cert/ 目录"
echo "2. 容器内路径:"
echo "   - /app/configs/cert/apiclient_key.pem"
echo "   - /app/configs/cert/wechatpay_cert.pem"
echo "3. 确保证书文件权限正确"

echo -e "\n${GREEN}设置完成${NC}" 
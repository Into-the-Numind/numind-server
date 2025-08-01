#!/bin/bash

# 服务器端证书设置脚本
# 用于在服务器上设置微信支付证书

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}服务器端微信支付证书设置脚本${NC}"
echo "=================================="

CERT_DIR="/opt/numind/config/cert"

echo -e "\n${YELLOW}检查证书目录...${NC}"

# 创建证书目录
if [ ! -d "$CERT_DIR" ]; then
    echo -e "${GREEN}创建证书目录: $CERT_DIR${NC}"
    sudo mkdir -p "$CERT_DIR"
    sudo chown $USER:$USER "$CERT_DIR"
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
    echo -e "${BLUE}获取步骤:${NC}"
    echo "1. 登录微信商户平台: https://pay.weixin.qq.com"
    echo "2. 进入 '账户中心' -> 'API安全'"
    echo "3. 下载 API 私钥文件"
    echo "4. 将文件重命名为 apiclient_key.pem"
    echo "5. 放置到 $CERT_DIR/ 目录"
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
    echo -e "${BLUE}获取步骤:${NC}"
    echo "1. 登录微信商户平台: https://pay.weixin.qq.com"
    echo "2. 进入 '账户中心' -> 'API安全'"
    echo "3. 下载 API 证书文件"
    echo "4. 将文件重命名为 wechatpay_cert.pem"
    echo "5. 放置到 $CERT_DIR/ 目录"
fi

echo -e "\n${YELLOW}证书文件列表:${NC}"
ls -la "$CERT_DIR/" 2>/dev/null || echo "目录为空"

echo -e "\n${BLUE}目录权限检查:${NC}"
ls -ld "$CERT_DIR" 2>/dev/null || echo "目录不存在"

echo -e "\n${BLUE}Docker 容器映射说明:${NC}"
echo "容器启动时会自动映射 /opt/numind 目录:"
echo "  -v /opt/numind:/opt/numind:ro"
echo ""
echo "容器内证书文件路径:"
echo "  - /opt/numind/config/cert/apiclient_key.pem"
echo "  - /opt/numind/config/cert/wechatpay_cert.pem"

echo -e "\n${BLUE}CI/CD 部署说明:${NC}"
echo "1. 开发环境: 自动映射 /opt/numind 目录"
echo "2. QA 环境: 自动创建 /opt/numind/config/cert 目录"
echo "3. 生产环境: 自动创建 /opt/numind/config/cert 目录"

echo -e "\n${GREEN}设置完成${NC}" 
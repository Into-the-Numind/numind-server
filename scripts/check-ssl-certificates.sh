#!/bin/bash

# SSL 证书检查脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}SSL 证书检查脚本${NC}"
echo "=================="

SSL_DIR="/etc/ssl/certimate/youshu.asia"
CERT_FILE="$SSL_DIR/cert.crt"
KEY_FILE="$SSL_DIR/cert.key"

echo -e "\n${YELLOW}检查 SSL 证书目录...${NC}"

# 检查 SSL 目录是否存在
if [ -d "$SSL_DIR" ]; then
    echo -e "${GREEN}✓ SSL 证书目录存在: $SSL_DIR${NC}"
else
    echo -e "${RED}✗ SSL 证书目录不存在: $SSL_DIR${NC}"
    echo -e "${YELLOW}请创建目录:${NC}"
    echo "  sudo mkdir -p $SSL_DIR"
    exit 1
fi

echo -e "\n${YELLOW}检查证书文件...${NC}"

# 检查证书文件
if [ -f "$CERT_FILE" ]; then
    echo -e "${GREEN}✓ SSL 证书文件存在${NC}"
    # 显示证书信息
    echo -e "${BLUE}证书信息:${NC}"
    openssl x509 -in "$CERT_FILE" -text -noout | grep -E "(Subject:|Issuer:|Not Before:|Not After:)" || echo "无法读取证书信息"
else
    echo -e "${RED}✗ SSL 证书文件不存在: $CERT_FILE${NC}"
fi

# 检查私钥文件
if [ -f "$KEY_FILE" ]; then
    echo -e "${GREEN}✓ SSL 私钥文件存在${NC}"
    # 检查私钥权限
    if [ "$(stat -c %a "$KEY_FILE" 2>/dev/null || stat -f %Lp "$KEY_FILE" 2>/dev/null)" = "600" ]; then
        echo -e "${GREEN}✓ 私钥文件权限正确 (600)${NC}"
    else
        echo -e "${YELLOW}⚠ 私钥文件权限不正确，建议设置为 600${NC}"
        echo "  sudo chmod 600 $KEY_FILE"
    fi
else
    echo -e "${RED}✗ SSL 私钥文件不存在: $KEY_FILE${NC}"
fi

echo -e "\n${YELLOW}文件列表:${NC}"
ls -la "$SSL_DIR/" 2>/dev/null || echo "目录为空"

echo -e "\n${YELLOW}证书验证...${NC}"

# 验证证书和私钥是否匹配
if [ -f "$CERT_FILE" ] && [ -f "$KEY_FILE" ]; then
    CERT_MODULUS=$(openssl x509 -noout -modulus -in "$CERT_FILE" 2>/dev/null | openssl md5)
    KEY_MODULUS=$(openssl rsa -noout -modulus -in "$KEY_FILE" 2>/dev/null | openssl md5)
    
    if [ "$CERT_MODULUS" = "$KEY_MODULUS" ]; then
        echo -e "${GREEN}✓ 证书和私钥匹配${NC}"
    else
        echo -e "${RED}✗ 证书和私钥不匹配${NC}"
    fi
else
    echo -e "${YELLOW}⚠ 无法验证证书匹配性，缺少证书或私钥文件${NC}"
fi

echo -e "\n${BLUE}Docker 容器映射说明:${NC}"
echo "容器启动时会自动映射 SSL 证书目录:"
echo "  -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro"
echo ""
echo "容器内证书文件路径:"
echo "  - /etc/ssl/certimate/youshu.asia/cert.crt"
echo "  - /etc/ssl/certimate/youshu.asia/cert.key"

echo -e "\n${BLUE}端口说明:${NC}"
echo "  - 9091: HTTP 端口"
echo "  - 9092: HTTPS 端口"

echo -e "\n${GREEN}检查完成${NC}" 
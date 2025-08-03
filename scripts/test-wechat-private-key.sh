#!/bin/bash

# 微信支付私钥测试脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付私钥测试脚本${NC}"
echo "========================"

PRIVATE_KEY_FILE="/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/configs/cert/apiclient_cert.pem"

echo -e "\n${YELLOW}检查私钥文件...${NC}"

# 检查文件是否存在
if [ ! -f "$PRIVATE_KEY_FILE" ]; then
    echo -e "${RED}✗ 私钥文件不存在: $PRIVATE_KEY_FILE${NC}"
    exit 1
fi

echo -e "${GREEN}✓ 私钥文件存在${NC}"

# 显示文件前几行
echo -e "\n${BLUE}私钥文件内容 (前10行):${NC}"
head -10 "$PRIVATE_KEY_FILE"

# 检查文件格式
echo -e "\n${BLUE}检查私钥格式:${NC}"

if grep -q "BEGIN PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 检测到 PKCS#8 格式私钥${NC}"
    KEY_TYPE="PKCS8"
elif grep -q "BEGIN RSA PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 检测到 RSA 格式私钥${NC}"
    KEY_TYPE="RSA"
elif grep -q "BEGIN EC PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 检测到 EC 格式私钥${NC}"
    KEY_TYPE="EC"
else
    echo -e "${RED}✗ 未检测到标准私钥格式${NC}"
    echo -e "${YELLOW}文件可能不是有效的 PEM 格式私钥${NC}"
    exit 1
fi

# 验证私钥
echo -e "\n${BLUE}验证私钥...${NC}"

if [ "$KEY_TYPE" = "RSA" ]; then
    if openssl rsa -in "$PRIVATE_KEY_FILE" -check -noout 2>/dev/null; then
        echo -e "${GREEN}✓ RSA 私钥验证通过${NC}"
    else
        echo -e "${RED}✗ RSA 私钥验证失败${NC}"
        exit 1
    fi
elif [ "$KEY_TYPE" = "EC" ]; then
    if openssl ec -in "$PRIVATE_KEY_FILE" -check -noout 2>/dev/null; then
        echo -e "${GREEN}✓ EC 私钥验证通过${NC}"
    else
        echo -e "${RED}✗ EC 私钥验证失败${NC}"
        exit 1
    fi
else
    if openssl pkcs8 -in "$PRIVATE_KEY_FILE" -noout 2>/dev/null; then
        echo -e "${GREEN}✓ PKCS#8 私钥验证通过${NC}"
    else
        echo -e "${RED}✗ PKCS#8 私钥验证失败${NC}"
        exit 1
    fi
fi

# 显示私钥信息
echo -e "\n${BLUE}私钥详细信息:${NC}"
if [ "$KEY_TYPE" = "RSA" ]; then
    openssl rsa -in "$PRIVATE_KEY_FILE" -text -noout | head -20
elif [ "$KEY_TYPE" = "EC" ]; then
    openssl ec -in "$PRIVATE_KEY_FILE" -text -noout | head -20
else
    openssl pkcs8 -in "$PRIVATE_KEY_FILE" -text -noout | head -20
fi

# 检查文件权限
echo -e "\n${BLUE}文件权限:${NC}"
ls -la "$PRIVATE_KEY_FILE"

# 检查文件大小
echo -e "\n${BLUE}文件大小:${NC}"
wc -c "$PRIVATE_KEY_FILE"

echo -e "\n${GREEN}测试完成${NC}"
echo -e "${YELLOW}如果私钥格式不正确，请运行修复脚本:${NC}"
echo "  ./scripts/fix-wechat-private-key.sh" 
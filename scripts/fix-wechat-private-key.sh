#!/bin/bash

# 微信支付私钥修复脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付私钥修复脚本${NC}"
echo "========================"

PRIVATE_KEY_FILE="/opt/numind/config/cert/apiclient_key.pem"
BACKUP_FILE="${PRIVATE_KEY_FILE}.backup.$(date +%Y%m%d_%H%M%S)"

echo -e "\n${YELLOW}检查私钥文件...${NC}"

# 检查私钥文件是否存在
if [ ! -f "$PRIVATE_KEY_FILE" ]; then
    echo -e "${RED}✗ 私钥文件不存在: $PRIVATE_KEY_FILE${NC}"
    echo -e "${YELLOW}请从微信商户平台下载私钥文件${NC}"
    exit 1
fi

echo -e "${GREEN}✓ 私钥文件存在${NC}"

# 备份原文件
echo -e "\n${YELLOW}备份原文件...${NC}"
cp "$PRIVATE_KEY_FILE" "$BACKUP_FILE"
echo -e "${GREEN}✓ 已备份到: $BACKUP_FILE${NC}"

# 检查私钥格式
echo -e "\n${YELLOW}检查私钥格式...${NC}"

# 检查是否包含正确的头部
if grep -q "BEGIN PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 私钥格式正确 (PKCS#8)${NC}"
    KEY_TYPE="PKCS8"
elif grep -q "BEGIN RSA PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 私钥格式正确 (RSA)${NC}"
    KEY_TYPE="RSA"
elif grep -q "BEGIN EC PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
    echo -e "${GREEN}✓ 私钥格式正确 (EC)${NC}"
    KEY_TYPE="EC"
else
    echo -e "${RED}✗ 私钥格式不正确${NC}"
    echo -e "${YELLOW}尝试修复私钥格式...${NC}"
    
    # 尝试转换为正确的格式
    if openssl rsa -in "$PRIVATE_KEY_FILE" -out "${PRIVATE_KEY_FILE}.tmp" 2>/dev/null; then
        mv "${PRIVATE_KEY_FILE}.tmp" "$PRIVATE_KEY_FILE"
        echo -e "${GREEN}✓ 已修复为 RSA 格式${NC}"
        KEY_TYPE="RSA"
    elif openssl pkcs8 -in "$PRIVATE_KEY_FILE" -out "${PRIVATE_KEY_FILE}.tmp" -nocrypt 2>/dev/null; then
        mv "${PRIVATE_KEY_FILE}.tmp" "$PRIVATE_KEY_FILE"
        echo -e "${GREEN}✓ 已修复为 PKCS#8 格式${NC}"
        KEY_TYPE="PKCS8"
    else
        echo -e "${RED}✗ 无法修复私钥格式${NC}"
        echo -e "${YELLOW}请检查私钥文件内容是否正确${NC}"
        echo -e "${BLUE}正确的私钥文件应该包含以下内容之一:${NC}"
        echo "  -----BEGIN PRIVATE KEY-----"
        echo "  -----BEGIN RSA PRIVATE KEY-----"
        echo "  -----BEGIN EC PRIVATE KEY-----"
        exit 1
    fi
fi

# 设置正确的权限
echo -e "\n${YELLOW}设置文件权限...${NC}"
chmod 600 "$PRIVATE_KEY_FILE"
echo -e "${GREEN}✓ 已设置权限为 600${NC}"

# 验证私钥
echo -e "\n${YELLOW}验证私钥...${NC}"
if openssl rsa -in "$PRIVATE_KEY_FILE" -check -noout 2>/dev/null; then
    echo -e "${GREEN}✓ 私钥验证通过${NC}"
elif openssl ec -in "$PRIVATE_KEY_FILE" -check -noout 2>/dev/null; then
    echo -e "${GREEN}✓ 私钥验证通过${NC}"
else
    echo -e "${RED}✗ 私钥验证失败${NC}"
    echo -e "${YELLOW}请检查私钥文件是否正确${NC}"
    exit 1
fi

# 显示私钥信息
echo -e "\n${BLUE}私钥信息:${NC}"
if [ "$KEY_TYPE" = "RSA" ]; then
    openssl rsa -in "$PRIVATE_KEY_FILE" -text -noout | grep -E "(RSA Private-Key|Modulus|Exponent)" || echo "无法读取私钥信息"
elif [ "$KEY_TYPE" = "EC" ]; then
    openssl ec -in "$PRIVATE_KEY_FILE" -text -noout | grep -E "(Private-Key|ASN1 OID)" || echo "无法读取私钥信息"
else
    openssl pkcs8 -in "$PRIVATE_KEY_FILE" -text -noout | grep -E "(Private-Key|Modulus)" || echo "无法读取私钥信息"
fi

echo -e "\n${BLUE}文件信息:${NC}"
ls -la "$PRIVATE_KEY_FILE"

echo -e "\n${GREEN}修复完成${NC}"
echo -e "${YELLOW}如果问题仍然存在，请检查:${NC}"
echo "1. 私钥文件是否从微信商户平台正确下载"
echo "2. 私钥文件是否完整且未损坏"
echo "3. 私钥是否与商户号匹配" 
#!/bin/bash

# 微信支付证书设置脚本
# 将证书文件复制到全局路径 /opt/numind/config/cert/

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付证书设置脚本${NC}"
echo "=========================="

# 配置路径
LOCAL_CERT_DIR="configs/cert"
GLOBAL_CERT_DIR="/opt/numind/config/cert"
LOCAL_CERT_FILE="$LOCAL_CERT_DIR/apiclient_cert.pem"
LOCAL_KEY_FILE="$LOCAL_CERT_DIR/apiclient_key.pem"
GLOBAL_CERT_FILE="$GLOBAL_CERT_DIR/apiclient_cert.pem"
GLOBAL_KEY_FILE="$GLOBAL_CERT_DIR/apiclient_key.pem"

echo -e "\n${YELLOW}检查本地证书文件...${NC}"

# 检查本地证书文件是否存在
if [ ! -f "$LOCAL_CERT_FILE" ]; then
    echo -e "${RED}✗ 本地证书文件不存在: $LOCAL_CERT_FILE${NC}"
    echo -e "${YELLOW}请从微信商户平台下载证书文件${NC}"
    exit 1
fi

if [ ! -f "$LOCAL_KEY_FILE" ]; then
    echo -e "${RED}✗ 本地私钥文件不存在: $LOCAL_KEY_FILE${NC}"
    echo -e "${YELLOW}请从微信商户平台下载私钥文件${NC}"
    exit 1
fi

echo -e "${GREEN}✓ 本地证书文件存在${NC}"

# 创建全局证书目录
echo -e "\n${YELLOW}创建全局证书目录...${NC}"
sudo mkdir -p "$GLOBAL_CERT_DIR"
sudo chown $USER:$USER "$GLOBAL_CERT_DIR"
echo -e "${GREEN}✓ 全局证书目录已创建: $GLOBAL_CERT_DIR${NC}"

# 复制证书文件
echo -e "\n${YELLOW}复制证书文件...${NC}"

# 备份现有文件（如果存在）
if [ -f "$GLOBAL_CERT_FILE" ]; then
    echo -e "${BLUE}备份现有证书文件...${NC}"
    sudo cp "$GLOBAL_CERT_FILE" "${GLOBAL_CERT_FILE}.backup.$(date +%Y%m%d_%H%M%S)"
fi

if [ -f "$GLOBAL_KEY_FILE" ]; then
    echo -e "${BLUE}备份现有私钥文件...${NC}"
    sudo cp "$GLOBAL_KEY_FILE" "${GLOBAL_KEY_FILE}.backup.$(date +%Y%m%d_%H%M%S)"
fi

# 复制文件
sudo cp "$LOCAL_CERT_FILE" "$GLOBAL_CERT_FILE"
sudo cp "$LOCAL_KEY_FILE" "$GLOBAL_KEY_FILE"

# 设置正确的文件权限
sudo chmod 644 "$GLOBAL_CERT_FILE"
sudo chmod 600 "$GLOBAL_KEY_FILE"
sudo chown $USER:$USER "$GLOBAL_CERT_FILE" "$GLOBAL_KEY_FILE"

echo -e "${GREEN}✓ 证书文件已复制到全局路径${NC}"

# 验证证书
echo -e "\n${YELLOW}验证证书...${NC}"

# 检查证书序列号
CERT_SERIAL=$(openssl x509 -in "$GLOBAL_CERT_FILE" -noout -serial 2>/dev/null | cut -d'=' -f2)
if [ -n "$CERT_SERIAL" ]; then
    echo -e "${GREEN}✓ 证书序列号: $CERT_SERIAL${NC}"
else
    echo -e "${RED}✗ 无法获取证书序列号${NC}"
fi

# 检查证书有效期
CERT_VALID_TO=$(openssl x509 -in "$GLOBAL_CERT_FILE" -noout -enddate 2>/dev/null | cut -d'=' -f2)
if [ -n "$CERT_VALID_TO" ]; then
    echo -e "${GREEN}✓ 证书有效期至: $CERT_VALID_TO${NC}"
else
    echo -e "${RED}✗ 无法获取证书有效期${NC}"
fi

# 验证私钥
if openssl rsa -in "$GLOBAL_KEY_FILE" -check -noout 2>/dev/null; then
    echo -e "${GREEN}✓ 私钥验证通过${NC}"
else
    echo -e "${RED}✗ 私钥验证失败${NC}"
fi

# 显示文件信息
echo -e "\n${BLUE}证书文件信息:${NC}"
ls -la "$GLOBAL_CERT_DIR/"

echo -e "\n${GREEN}证书设置完成！${NC}"
echo -e "${BLUE}证书路径: $GLOBAL_CERT_DIR${NC}"
echo -e "${BLUE}证书文件: $GLOBAL_CERT_FILE${NC}"
echo -e "${BLUE}私钥文件: $GLOBAL_KEY_FILE${NC}"

echo -e "\n${YELLOW}注意事项:${NC}"
echo "1. 确保证书文件权限正确（证书: 644, 私钥: 600）"
echo "2. 重启Docker容器以加载新的证书配置"
echo "3. 检查配置文件中的证书路径是否正确"
echo "4. 定期更新证书以避免过期问题"

echo -e "\n${BLUE}重启容器命令:${NC}"
echo "docker restart numind-server" 
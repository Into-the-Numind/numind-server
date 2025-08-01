#!/bin/bash

# 微信支付证书诊断脚本
# 用于诊断和解决证书序列号问题

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}微信支付证书诊断脚本${NC}"
echo "=========================="

# 配置路径
CERT_DIR="/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/configs/cert"
PRIVATE_KEY_FILE="$CERT_DIR/apiclient_key.pem"
WECHAT_CERT_FILE="$CERT_DIR/apiclient_cert.pem"

# 从配置文件读取证书序列号
CONFIG_FILE="config_local.yaml"
if [ -f "$CONFIG_FILE" ]; then
    CONFIG_SERIAL_NO=$(grep "mch_cert_serial_no:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
    echo -e "${BLUE}配置文件中的证书序列号: ${CONFIG_SERIAL_NO}${NC}"
else
    echo -e "${RED}配置文件不存在: $CONFIG_FILE${NC}"
    CONFIG_SERIAL_NO=""
fi

echo -e "\n${YELLOW}检查证书文件...${NC}"

# 检查私钥文件
if [ -f "$PRIVATE_KEY_FILE" ]; then
    echo -e "${GREEN}✓ 商户私钥文件存在: $PRIVATE_KEY_FILE${NC}"
    
    # 检查私钥格式
    if grep -q "BEGIN PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
        echo -e "${GREEN}✓ 检测到 PKCS#8 格式私钥${NC}"
        KEY_TYPE="PKCS8"
    elif grep -q "BEGIN RSA PRIVATE KEY" "$PRIVATE_KEY_FILE"; then
        echo -e "${GREEN}✓ 检测到 RSA 格式私钥${NC}"
        KEY_TYPE="RSA"
    else
        echo -e "${RED}✗ 私钥格式不正确${NC}"
        echo -e "${YELLOW}请确保私钥文件是有效的 PEM 格式${NC}"
    fi
    
    # 验证私钥
    if [ "$KEY_TYPE" = "RSA" ]; then
        if openssl rsa -in "$PRIVATE_KEY_FILE" -check -noout 2>/dev/null; then
            echo -e "${GREEN}✓ RSA 私钥验证通过${NC}"
        else
            echo -e "${RED}✗ RSA 私钥验证失败${NC}"
        fi
    fi
    
    # 获取私钥对应的证书序列号
    echo -e "\n${BLUE}从私钥获取证书信息...${NC}"
    if [ "$KEY_TYPE" = "RSA" ]; then
        # 尝试从私钥生成公钥，然后获取证书信息
        TEMP_PUBKEY=$(mktemp)
        if openssl rsa -in "$PRIVATE_KEY_FILE" -pubout -out "$TEMP_PUBKEY" 2>/dev/null; then
            echo -e "${GREEN}✓ 成功从私钥生成公钥${NC}"
            rm -f "$TEMP_PUBKEY"
        else
            echo -e "${RED}✗ 无法从私钥生成公钥${NC}"
        fi
    fi
else
    echo -e "${RED}✗ 商户私钥文件不存在: $PRIVATE_KEY_FILE${NC}"
    echo -e "${YELLOW}请从微信商户平台下载私钥文件${NC}"
fi

# 检查微信支付证书
if [ -f "$WECHAT_CERT_FILE" ]; then
    echo -e "\n${GREEN}✓ 微信支付证书存在: $WECHAT_CERT_FILE${NC}"
    
    # 获取证书序列号
    echo -e "\n${BLUE}从微信支付证书获取序列号...${NC}"
    CERT_SERIAL_NO=$(openssl x509 -in "$WECHAT_CERT_FILE" -noout -serial 2>/dev/null | cut -d'=' -f2)
    if [ -n "$CERT_SERIAL_NO" ]; then
        echo -e "${GREEN}✓ 证书序列号: $CERT_SERIAL_NO${NC}"
        
        # 比较配置文件中的序列号
        if [ -n "$CONFIG_SERIAL_NO" ]; then
            if [ "$CONFIG_SERIAL_NO" = "$CERT_SERIAL_NO" ]; then
                echo -e "${GREEN}✓ 配置文件中的序列号与证书序列号匹配${NC}"
            else
                echo -e "${RED}✗ 配置文件中的序列号与证书序列号不匹配${NC}"
                echo -e "${YELLOW}配置文件中的序列号: $CONFIG_SERIAL_NO${NC}"
                echo -e "${YELLOW}证书中的序列号: $CERT_SERIAL_NO${NC}"
                echo -e "${BLUE}建议更新配置文件中的序列号${NC}"
            fi
        fi
    else
        echo -e "${RED}✗ 无法从证书获取序列号${NC}"
    fi
    
    # 显示证书详细信息
    echo -e "\n${BLUE}证书详细信息:${NC}"
    openssl x509 -in "$WECHAT_CERT_FILE" -text -noout | head -30
else
    echo -e "\n${RED}✗ 微信支付证书不存在: $WECHAT_CERT_FILE${NC}"
    echo -e "${YELLOW}请从微信商户平台下载证书文件${NC}"
fi

# 检查证书文件权限
echo -e "\n${BLUE}证书文件权限:${NC}"
if [ -f "$PRIVATE_KEY_FILE" ]; then
    ls -la "$PRIVATE_KEY_FILE"
fi
if [ -f "$WECHAT_CERT_FILE" ]; then
    ls -la "$WECHAT_CERT_FILE"
fi

# 提供解决方案
echo -e "\n${YELLOW}解决方案:${NC}"
echo "1. 确保从微信商户平台下载了正确的证书文件"
echo "2. 检查证书序列号是否与配置文件中的一致"
echo "3. 如果序列号不匹配，请更新配置文件中的 mch_cert_serial_no"
echo "4. 确保证书文件权限正确 (私钥: 600, 证书: 644)"
echo "5. 重启应用程序以加载新的配置"

echo -e "\n${BLUE}获取证书的步骤:${NC}"
echo "1. 登录微信商户平台: https://pay.weixin.qq.com"
echo "2. 进入 '账户中心' -> 'API安全'"
echo "3. 下载 API 证书和私钥文件"
echo "4. 将文件放置到 $CERT_DIR/ 目录"
echo "5. 设置正确的文件权限"

echo -e "\n${GREEN}诊断完成${NC}" 
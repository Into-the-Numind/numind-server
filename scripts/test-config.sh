#!/bin/bash

# 测试配置加载脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}配置加载测试脚本${NC}"
echo "=================="

# 编译一个简单的测试程序来验证配置
cat > test_config.go << 'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
)

func main() {
	// 设置配置文件
	viper.SetConfigFile("config_local.yaml")
	
	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Error reading config file: %v\n", err)
		os.Exit(1)
	}

	// 读取微信支付配置
	appID := viper.GetString("wechat.app_id")
	mchID := viper.GetString("wechat.mch_id")
	mchCertSerialNo := viper.GetString("wechat.mch_cert_serial_no")
	mchAPIv3Key := viper.GetString("wechat.mch_api_v3_key")
	mchPrivateKeyPath := viper.GetString("wechat.mch_private_key_path")
	wechatPayCertPath := viper.GetString("wechat.wechatpay_cert_path")

	fmt.Printf("App ID: %s\n", appID)
	fmt.Printf("Mch ID: %s\n", mchID)
	fmt.Printf("Mch Cert Serial No: %s\n", mchCertSerialNo)
	fmt.Printf("Mch API v3 Key: %s\n", mchAPIv3Key)
	fmt.Printf("Mch Private Key Path: %s\n", mchPrivateKeyPath)
	fmt.Printf("Wechat Pay Cert Path: %s\n", wechatPayCertPath)
}
EOF

echo -e "\n${BLUE}编译测试程序...${NC}"
go build -o test_config test_config.go

echo -e "\n${BLUE}运行配置测试...${NC}"
./test_config

echo -e "\n${BLUE}清理测试文件...${NC}"
rm -f test_config.go test_config

echo -e "\n${GREEN}配置测试完成${NC}" 
package main

import (
	"fmt"
	"numind-server/internal/numind/biz/wechat"

	"github.com/spf13/viper"
)

func main() {
	fmt.Println("=== 测试微信支付配置 ===")

	// 加载配置文件
	viper.SetConfigFile("config_local.yaml")
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("读取配置文件失败: %v\n", err)
		return
	}

	// 获取配置
	cfg := map[string]string{
		"app_id":               viper.GetString("wechat.app_id"),
		"mch_id":               viper.GetString("wechat.mch_id"),
		"mch_cert_serial_no":   viper.GetString("wechat.mch_cert_serial_no"),
		"mch_api_v3_key":       viper.GetString("wechat.mch_api_v3_key"),
		"mch_private_key_path": viper.GetString("wechat.mch_private_key_path"),
		"wechatpay_cert_path":  viper.GetString("wechat.wechatpay_cert_path"),
		"notify_url":           viper.GetString("wechat.notify_url"),
	}

	fmt.Printf("配置中的证书序列号: %s\n", cfg["mch_cert_serial_no"])
	fmt.Printf("商户ID: %s\n", cfg["mch_id"])
	fmt.Printf("私钥路径: %s\n", cfg["mch_private_key_path"])
	fmt.Printf("证书路径: %s\n", cfg["wechatpay_cert_path"])

	// 尝试创建支付客户端
	fmt.Println("\n=== 测试创建支付客户端 ===")
	_, err := wechat.NewPayClientFromMap(cfg)
	if err != nil {
		fmt.Printf("创建支付客户端失败: %v\n", err)
		return
	}

	fmt.Println("支付客户端创建成功")

	// 尝试创建小程序支付订单
	fmt.Println("\n=== 测试创建小程序支付订单 ===")
	resp, err := wechat.CreateMiniProgramOrder(cfg, "TEST_ORDER_001", "测试商品", 100, "o-OeX7Uo8E2NQ-J86Os1yZ1BrLIM")
	if err != nil {
		fmt.Printf("创建小程序支付订单失败: %v\n", err)
		return
	}

	fmt.Printf("小程序支付订单创建成功: %+v\n", resp)
}

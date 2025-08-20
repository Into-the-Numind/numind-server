package main

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/volc"

	"github.com/spf13/viper"
)

func main() {
	fmt.Println("🔧 API模型配置修复验证")
	fmt.Println("==================================================")

	// 初始化配置
	viper.SetConfigName("config_local")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("读取配置文件失败: %v\n", err)
		return
	}

	ctx := context.Background()

	fmt.Println("📋 当前配置检查:")
	fmt.Printf("✅ 阿里千问模型: %s\n", viper.GetString("ali.text.model"))
	fmt.Printf("✅ 火山引擎模型: %s\n", viper.GetString("volc.model"))
	fmt.Printf("✅ 阿里千问API Key: %s...\n", viper.GetString("ali.text.api_key")[:10])
	fmt.Printf("✅ 火山引擎API Key: %s...\n", viper.GetString("volc.api_key")[:10])

	fmt.Println("\n🧪 测试API调用...")

	// 测试消息
	messages := []map[string]string{
		{"role": "system", "content": "你是一个专业的文本处理助手。"},
		{"role": "user", "content": "请简单回复'测试成功'三个字。"},
	}

	// 测试阿里千问API
	fmt.Println("\n🔍 测试阿里千问API...")
	aliBiz := ali.NewAliBiz(nil)

	startTime := time.Now()
	result, err := aliBiz.QianwenTextStream(messages, 100, 0.5)
	duration := time.Since(startTime)

	if err != nil {
		fmt.Printf("❌ 阿里千问API调用失败: %v\n", err)
		fmt.Printf("⏱️  耗时: %v\n", duration)
	} else {
		fmt.Printf("✅ 阿里千问API调用成功\n")
		fmt.Printf("📝 响应内容: %s\n", result)
		fmt.Printf("⏱️  耗时: %v\n", duration)
	}

	// 测试火山引擎API
	fmt.Println("\n🔍 测试火山引擎API...")
	volcBiz := volc.NewVolcBiz(nil)

	startTime = time.Now()
	result, err = volcBiz.VolcTextStream(ctx, messages, 100, 0.5)
	duration = time.Since(startTime)

	if err != nil {
		fmt.Printf("❌ 火山引擎API调用失败: %v\n", err)
		fmt.Printf("⏱️  耗时: %v\n", duration)
	} else {
		fmt.Printf("✅ 火山引擎API调用成功\n")
		fmt.Printf("📝 响应内容: %s\n", result)
		fmt.Printf("⏱️  耗时: %v\n", duration)
	}

	fmt.Println("\n==================================================")
	fmt.Println("�� API模型配置修复验证完成")
}

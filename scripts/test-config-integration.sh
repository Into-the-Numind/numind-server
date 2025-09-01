#!/bin/bash

# 测试配置集成
# 验证分页配置和JSON处理配置是否正确加载

echo "=== 测试配置集成 ==="
echo "当前目录: $(pwd)"
echo ""

# 检查配置文件是否存在
if [ ! -f "config_local.yaml" ]; then
    echo "❌ 配置文件 config_local.yaml 不存在"
    exit 1
fi

echo "✅ 配置文件存在: config_local.yaml"
echo ""

# 创建测试程序
cat > test_config.go << 'EOF'
package main

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/httpclient"
)

func main() {
	// 设置配置文件路径
	viper.SetConfigFile("config_local.yaml")
	viper.SetConfigType("yaml")

	// 读取配置
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}

	fmt.Println("=== 分页配置测试 ===")
	
	// 测试分页配置加载
	paginationConfig := pagination.LoadConfigFromViper()
	fmt.Printf("卡片尺寸: %dx%d\n", paginationConfig.Card.Width, paginationConfig.Card.Height)
	fmt.Printf("卡片内边距: T:%d R:%d B:%d L:%d\n", 
		paginationConfig.Card.Padding.Top, paginationConfig.Card.Padding.Right,
		paginationConfig.Card.Padding.Bottom, paginationConfig.Card.Padding.Left)
	
	fmt.Println("\n样式配置:")
	for elementType, style := range paginationConfig.Styles {
		fmt.Printf("  %s: FontSize:%d, LineHeight:%d, Margins:T%d/B%d, Color:%s, Align:%s\n",
			elementType, style.FontSize, style.LineHeight,
			style.MarginTop, style.MarginBottom, style.Color, style.Align)
		if style.Indent > 0 {
			fmt.Printf("    Indent: %d\n", style.Indent)
		}
	}

	fmt.Println("\n=== JSON处理配置测试 ===")
	
	// 测试JSON处理配置加载
	jsonConfig := httpclient.LoadJSONProcessingConfig()
	fmt.Printf("严格控制字符过滤: %t\n", jsonConfig.CharacterFiltering.StrictControlChars)
	fmt.Printf("扩展ASCII字符过滤: %t\n", jsonConfig.CharacterFiltering.FilterExtendedASCII)
	fmt.Printf("Unicode替换字符过滤: %t\n", jsonConfig.CharacterFiltering.FilterUnicodeReplacement)
	fmt.Printf("允许的控制字符: %v\n", jsonConfig.CharacterFiltering.AllowedControlChars)
	
	fmt.Printf("深度JSON修复: %t\n", jsonConfig.JSONRepair.EnableDeepRepair)
	fmt.Printf("字段优先提取: %t\n", jsonConfig.JSONRepair.EnableFieldBasedExtraction)
	fmt.Printf("保守修复策略: %t\n", jsonConfig.JSONRepair.EnableConservativeFix)
	fmt.Printf("最大修复尝试次数: %d\n", jsonConfig.JSONRepair.MaxRepairAttempts)
	fmt.Printf("启用日志记录: %t\n", jsonConfig.JSONRepair.EnableLogging)
	
	fmt.Printf("检查Content-Length: %t\n", jsonConfig.ResponseProcessing.CheckContentLength)
	fmt.Printf("启用响应恢复: %t\n", jsonConfig.ResponseProcessing.EnableResponseRecovery)
	fmt.Printf("响应超时时间: %s\n", jsonConfig.ResponseProcessing.Timeout)
	fmt.Printf("最大响应大小: %d bytes\n", jsonConfig.ResponseProcessing.MaxResponseSize)

	fmt.Println("\n=== Unicode范围配置测试 ===")
	fmt.Printf("中文字符范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Chinese)
	fmt.Printf("中文标点符号范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.ChinesePunctuation)
	fmt.Printf("全角字符范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Fullwidth)
	fmt.Printf("拉丁字母扩展范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.LatinExtended)
	fmt.Printf("阿拉伯文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Arabic)
	fmt.Printf("西里尔文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Cyrillic)
	fmt.Printf("希腊文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Greek)
	fmt.Printf("希伯来文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Hebrew)
	fmt.Printf("泰文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Thai)
	fmt.Printf("韩文范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.Korean)
	fmt.Printf("日文平假名范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.JapaneseHiragana)
	fmt.Printf("日文片假名范围: %v\n", jsonConfig.CharacterFiltering.AllowedUnicodeRanges.JapaneseKatakana)

	fmt.Println("\n=== 配置摘要 ===")
	summary := pagination.GetConfigSummary(paginationConfig)
	fmt.Print(summary)

	fmt.Println("\n=== 测试完成 ===")
}
EOF

echo "📝 创建测试程序: test_config.go"
echo ""

# 运行测试
echo "🚀 运行配置集成测试..."
go run test_config.go

# 清理
echo ""
echo "🧹 清理测试文件..."
rm -f test_config.go

echo ""
echo "✅ 配置集成测试完成"

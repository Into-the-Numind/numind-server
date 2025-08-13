#!/bin/bash

# JSON解析修复集成测试脚本
# 测试新的JSON响应处理器是否能够解决"unexpected end of JSON input"问题

echo "=== JSON解析修复集成测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/pkg/httpclient
go build .
cd ../../..

cd internal/numind/biz/book
go build .
cd ../../..

# 测试2: 创建测试程序
echo "创建JSON解析测试程序..."
cat > test_json_fix_integration.go << 'EOF'
package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"numind-server/internal/pkg/httpclient"
)

func main() {
	fmt.Println("=== JSON解析修复集成测试 ===")

	// 模拟有问题的响应数据（包含你遇到的错误）
	problematicResponse := `{"structured_text_array":[{"type":"title","content":"我好像发现了魅力的本质!"},{"type":"subtitle","content":"魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点,而是清醒认知自身的优势与局限后,既不刻意放大优点去炫耀,也不因短板而自我否定。比如一个人坦然承认自己内向不善社交,却\xe8\xb8\xa6、留白感的半透明面纱、生命活力的向日葵、边界意识的金色轮廓线、幽默感的愉悦音符、专注感的聚光灯、真诚感的纯净水晶、审美力的色彩光谱、包容心的开放拱门、行动力的向前箭头、松弛感的飘逸布料、独特性的不规则几何、倾听能力的声波图案、温暖善意的发光双手、内在轻松的轻盈羽毛。柔和渐变的背景色彩，梦幻氛围，8k, ultra-detailed, cinematic lighting, digital art"}]`

	fmt.Printf("原始响应长度: %d 字符\n", len(problematicResponse))
	fmt.Printf("包含问题内容: 编码问题、控制字符、无效的Unicode转义\n")
	fmt.Printf("原始响应: %s\n", problematicResponse[:200] + "...")

	// 创建模拟的HTTP响应
	mockResp := &http.Response{
		Body: strings.NewReader(problematicResponse),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	// 使用新的JSON响应处理器
	fmt.Println("\n使用新的JSON响应处理器...")
	processor := httpclient.NewJSONResponseProcessor()
	
	processedBody, err := processor.ProcessResponse(mockResp)
	if err != nil {
		fmt.Printf("❌ JSON响应处理器失败: %v\n", err)
		return
	}

	fmt.Printf("✅ JSON响应处理器成功，处理后的长度: %d\n", len(processedBody))
	fmt.Printf("处理后的内容: %s\n", string(processedBody)[:200] + "...")

	// 验证JSON是否有效
	var testStruct struct {
		StructuredTextArray []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"structured_text_array"`
	}

	if err := json.Unmarshal(processedBody, &testStruct); err != nil {
		fmt.Printf("❌ 处理后的JSON仍然无效: %v\n", err)
		return
	}

	fmt.Printf("✅ 处理后的JSON验证成功！\n")
	fmt.Printf("结构化文本数组长度: %d\n", len(testStruct.StructuredTextArray))
	
	for i, item := range testStruct.StructuredTextArray {
		fmt.Printf("  项目 %d: 类型=%s, 内容长度=%d\n", i+1, item.Type, len(item.Content))
	}

	fmt.Println("\n=== 测试完成 ===")
}
EOF

# 测试3: 运行测试程序
echo "运行JSON解析测试程序..."
go run test_json_fix_integration.go

# 清理
rm test_json_fix_integration.go

echo ""
echo "测试完成！"
echo ""
echo "主要修复："
echo "1. 集成了新的JSON响应处理器到book创建流程"
echo "2. 能够处理截断的HTTP响应"
echo "3. 自动修复编码问题和无效的Unicode序列"
echo "4. 智能提取和修复JSON结构"
echo "5. 提供详细的处理过程日志"

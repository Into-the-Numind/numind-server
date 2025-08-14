#!/bin/bash

# JSON解析修复集成测试脚本
# 测试修复后的JSON响应处理器是否能正确处理包含structured_text_array的响应

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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"numind-server/internal/pkg/httpclient"
)

func main() {
	fmt.Println("=== JSON解析修复集成测试 ===")

	// 模拟有问题的响应数据（包含你遇到的错误）
	problematicResponse := `{"structured_text_array":[{"type":"title","content":"我好像发现了魅力的本质!"},{"type":"subtitle","content":"魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点,而是清醒认知自身的优势与局限后,既不刻意放大优点去炫耀,也不因短板而自我否定。比如一个人坦然承认自己内向不善社交,却能在独处时找到内心的平静与力量。这种真实的自我接纳,反而会散发出一种独特的魅力。2. 稳定的情绪内核有魅力的人往往拥有稳定的情绪内核。他们不会因为外界的一点风吹草动就情绪起伏,也不会把自己的情绪垃圾随意倾倒给他人。这种情绪稳定性不是冷漠,而是一种内在的成熟与智慧。比如面对挫折时,他们能够冷静分析问题,寻找解决方案,而不是一味地抱怨或逃避。这种稳定的情绪状态,会让人感到安心和可靠。3. 流动的内在丰富性魅力往往来自于内在的丰富性。这种丰富性不是知识的堆砌,而是对生活的深度思考和感悟。有魅力的人通常有着丰富的内心世界,他们能够从不同的角度看待问题,能够理解他人的感受,能够给出有见地的建议。这种内在的丰富性,会让人感到与他们交流是一种享受。4. 敏锐的共情能力有魅力的人往往具有敏锐的共情能力。他们能够准确地理解他人的情感和需求,能够站在他人的角度思考问题。这种共情能力不是简单的同情,而是一种深度的理解和连接。比如当朋友遇到困难时,他们能够真正理解朋友的感受,给出恰到好处的安慰和支持。5. 恰到好处的留白感（克制）魅力往往来自于恰到好处的留白感。这种留白感不是冷漠或疏离,而是一种优雅的克制。有魅力的人知道什么时候该说话,什么时候该沉默,什么时候该主动,什么时候该退让。这种恰到好处的留白感,会让人感到他们很有分寸,很有修养。6. 蓬勃的生命力有魅力的人往往具有蓬勃的生命力。他们对生活充满热情,对未来充满期待,对未知充满好奇。这种生命力不是盲目的乐观,而是一种积极向上的生活态度。比如他们能够从日常的小事中发现乐趣,能够从困难中找到机会,能够从失败中汲取教训。这种蓬勃的生命力,会让人感到与他们在一起很有活力。7. 清晰的边界意识有魅力的人懂得守住自己的底线,尊重他人的空间。比如面对不合理的请求,能温和而坚定地说"这个我可能帮不了你",既不委屈自己,也不伤害对方。这种边界意识不是冷漠,而是一种健康的自我保护。有魅力的人知道什么时候该说"不",什么时候该坚持原则,什么时候该妥协。这种清晰的边界意识,会让人感到他们很有原则,很有担当。魅力的本质,其实是一种内在品质的外在体现。它不是刻意营造的人设,而是经过时间沉淀后的自然流露。当我们真正接纳自己,稳定情绪,丰富内心,培养共情,学会克制,保持活力,守住边界时,魅力就会自然而然地散发出来。就像向日葵永远朝着阳光,充满活力的人也会让人觉得靠近他,生活就多了点奔头。"}]`

	fmt.Printf("原始响应长度: %d 字符\n", len(problematicResponse))
	fmt.Printf("包含问题内容: 编码问题、控制字符、无效的Unicode转义\n")
	fmt.Printf("原始响应预览: %s...\n", problematicResponse[:200])

	// 诊断编码问题
	fmt.Println("\n=== 编码问题诊断 ===")
	for i, char := range problematicResponse {
		if char == utf8.RuneError || char == 0xFFFD || (char < 32 && char != '\n' && char != '\t') {
			fmt.Printf("位置 %d: 无效字符 0x%02x\n", i, char)
		}
	}

	// 创建模拟的HTTP响应
	mockResp := &http.Response{
		Body: io.NopCloser(strings.NewReader(problematicResponse)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	// 使用修复后的JSON响应处理器
	processor := httpclient.NewJSONResponseProcessor()
	
	fmt.Println("\n=== 使用修复后的JSON响应处理器 ===")
	
	processedBody, err := processor.ProcessResponse(mockResp)
	if err != nil {
		fmt.Printf("❌ 处理失败: %v\n", err)
		return
	}
	
	fmt.Printf("✅ 处理成功，长度: %d 字符\n", len(processedBody))
	
	// 安全地显示处理后的JSON预览
	if len(processedBody) > 200 {
		fmt.Printf("处理后的JSON: %s...\n", string(processedBody)[:200])
	} else {
		fmt.Printf("处理后的JSON: %s\n", string(processedBody))
	}
	
	// 诊断处理后的编码问题
	fmt.Println("\n=== 处理后编码问题诊断 ===")
	for i, char := range string(processedBody) {
		if char == utf8.RuneError || char == 0xFFFD || (char < 32 && char != '\n' && char != '\t') {
			fmt.Printf("位置 %d: 无效字符 0x%02x\n", i, char)
		}
	}
	
	// 验证处理后的JSON是否有效
	var testStruct struct {
		StructuredTextArray []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"structured_text_array"`
	}
	
	if err := json.Unmarshal(processedBody, &testStruct); err != nil {
		fmt.Printf("❌ JSON解析失败: %v\n", err)
		
		// 尝试定位具体的错误位置
		if jsonErr, ok := err.(*json.SyntaxError); ok {
			offset := jsonErr.Offset
			start := int(offset) - 50
			if start < 0 {
				start = 0
			}
			end := int(offset) + 50
			if end > len(processedBody) {
				end = len(processedBody)
			}
			fmt.Printf("错误位置附近的内容: %s\n", string(processedBody[start:end]))
		}
		
		return
	}
	
	fmt.Printf("✅ JSON解析成功，包含 %d 个文本元素\n", len(testStruct.StructuredTextArray))
	
	// 检查第一个元素
	if len(testStruct.StructuredTextArray) > 0 {
		first := testStruct.StructuredTextArray[0]
		fmt.Printf("第一个元素: type=%s, content长度=%d\n", first.Type, len(first.Content))
	}
	
	fmt.Println("\n=== 测试完成 ===")
}
EOF

# 测试3: 运行测试
echo "运行JSON解析测试..."
go run test_json_fix_integration.go

# 测试4: 清理
echo "清理测试文件..."
rm -f test_json_fix_integration.go

echo "=== 测试完成 ==="

#!/bin/bash

# JSON编码问题调试脚本
# 精确定位JSON中的编码问题

echo "=== JSON编码问题调试 ==="

cat > debug_json_encoding.go << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func main() {
	fmt.Println("=== JSON编码问题调试 ===")

	// 模拟有问题的响应数据
	problematicResponse := `{"structured_text_array":[{"type":"title","content":"我好像发现了魅力的本质!"},{"type":"subtitle","content":"魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点,而是清醒认知自身的优势与局限后,既不刻意放大优点去炫耀,也不因短板而自我否定。比如一个人坦然承认自己内向不善社交,却能在独处时找到内心的平静与力量。这种真实的自我接纳,反而会散发出一种独特的魅力。2. 稳定的情绪内核有魅力的人往往拥有稳定的情绪内核。他们不会因为外界的一点风吹草动就情绪起伏,也不会把自己的情绪垃圾随意倾倒给他人。这种情绪稳定性不是冷漠,而是一种内在的成熟与智慧。比如面对挫折时,他们能够冷静分析问题,寻找解决方案,而不是一味地抱怨或逃避。这种稳定的情绪状态,会让人感到安心和可靠。3. 流动的内在丰富性魅力往往来自于内在的丰富性。这种丰富性不是知识的堆砌,而是对生活的深度思考和感悟。有魅力的人通常有着丰富的内心世界,他们能够从不同的角度看待问题,能够理解他人的感受,能够给出有见地的建议。这种内在的丰富性,会让人感到与他们交流是一种享受。4. 敏锐的共情能力有魅力的人往往具有敏锐的共情能力。他们能够准确地理解他人的情感和需求,能够站在他人的角度思考问题。这种共情能力不是简单的同情,而是一种深度的理解和连接。比如当朋友遇到困难时,他们能够真正理解朋友的感受,给出恰到好处的安慰和支持。5. 恰到好处的留白感（克制）魅力往往来自于恰到好处的留白感。这种留白感不是冷漠或疏离,而是一种优雅的克制。有魅力的人知道什么时候该说话,什么时候该沉默,什么时候该主动,什么时候该退让。这种恰到好处的留白感,会让人感到他们很有分寸,很有修养。6. 蓬勃的生命力有魅力的人往往具有蓬勃的生命力。他们对生活充满热情,对未来充满期待,对未知充满好奇。这种生命力不是盲目的乐观,而是一种积极向上的生活态度。比如他们能够从日常的小事中发现乐趣,能够从困难中找到机会,能够从失败中汲取教训。这种蓬勃的生命力,会让人感到与他们在一起很有活力。7. 清晰的边界意识有魅力的人懂得守住自己的底线,尊重他人的空间。比如面对不合理的请求,能温和而坚定地说"这个我可能帮不了你",既不委屈自己,也不伤害对方。这种边界意识不是冷漠,而是一种健康的自我保护。有魅力的人知道什么时候该说"不",什么时候该坚持原则,什么时候该妥协。这种清晰的边界意识,会让人感到他们很有原则,很有担当。魅力的本质,其实是一种内在品质的外在体现。它不是刻意营造的人设,而是经过时间沉淀后的自然流露。当我们真正接纳自己,稳定情绪,丰富内心,培养共情,学会克制,保持活力,守住边界时,魅力就会自然而然地散发出来。就像向日葵永远朝着阳光,充满活力的人也会让人觉得靠近他,生活就多了点奔头。"}]`

	fmt.Printf("原始响应长度: %d 字符\n", len(problematicResponse))

	// 查找第一个 { 和最后一个 }
	start := strings.Index(problematicResponse, "{")
	end := strings.LastIndex(problematicResponse, "}")
	
	if start != -1 && end != -1 && end > start {
		extracted := problematicResponse[start : end+1]
		fmt.Printf("提取的JSON长度: %d 字符\n", len(extracted))
		
		// 精确定位编码问题
		fmt.Println("\n=== 编码问题精确定位 ===")
		for i, char := range extracted {
			if char == utf8.RuneError || char == 0xFFFD || (char < 32 && char != '\n' && char != '\t') {
				fmt.Printf("位置 %d: 无效字符 0x%02x\n", i, char)
			}
		}
		
		// 查找"这个我可能帮不了你"附近的问题
		problemText := "这个我可能帮不了你"
		problemIndex := strings.Index(extracted, problemText)
		if problemIndex != -1 {
			fmt.Printf("\n=== 问题文本附近分析 ===")
			fmt.Printf("问题文本位置: %d\n", problemIndex)
			
			// 显示问题文本前后的内容
			start := problemIndex - 100
			if start < 0 {
				start = 0
			}
			end := problemIndex + 100
			if end > len(extracted) {
				end = len(extracted)
			}
			
			context := extracted[start:end]
			fmt.Printf("上下文内容: %s\n", context)
			
			// 分析上下文中的每个字符
			fmt.Printf("\n字符分析:\n")
			for i, char := range context {
				if char < 32 && char != '\n' && char != '\t' {
					fmt.Printf("位置 %d: 控制字符 0x%02x\n", i, char)
				} else if char == utf8.RuneError || char == 0xFFFD {
					fmt.Printf("位置 %d: 无效Unicode 0x%02x\n", i, char)
				}
			}
			
			// 详细分析问题文本附近的字符
			fmt.Printf("\n=== 问题文本附近详细字符分析 ===")
			detailStart := problemIndex - 50
			if detailStart < 0 {
				detailStart = 0
			}
			detailEnd := problemIndex + 50
			if detailEnd > len(extracted) {
				detailEnd = len(extracted)
			}
			
			detailContext := extracted[detailStart:detailEnd]
			fmt.Printf("详细上下文: %s\n", detailContext)
			
			for i, char := range detailContext {
				actualPos := detailStart + i
				fmt.Printf("位置 %d: '%c' (0x%02x, %d)\n", actualPos, char, char, char)
			}
		}
		
		// 尝试清理编码问题
		fmt.Println("\n=== 编码清理测试 ===")
		cleaned := cleanEncoding(extracted)
		fmt.Printf("清理后长度: %d 字符\n", len(cleaned))
		
		// 测试清理后的JSON是否有效
		var testStruct struct {
			StructuredTextArray []struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			} `json:"structured_text_array"`
		}
		
		if err := json.Unmarshal([]byte(cleaned), &testStruct); err != nil {
			fmt.Printf("❌ JSON解析仍然失败: %v\n", err)
			
			// 尝试定位具体的错误位置
			if jsonErr, ok := err.(*json.SyntaxError); ok {
				offset := jsonErr.Offset
				start := int(offset) - 50
				if start < 0 {
					start = 0
				}
				end := int(offset) + 50
				if end > len(cleaned) {
					end = len(cleaned)
				}
				fmt.Printf("错误位置附近的内容: %s\n", cleaned[start:end])
				
				// 分析错误位置的字符
				fmt.Printf("\n=== 错误位置字符分析 ===")
				for i := start; i < end; i++ {
					if i < len(cleaned) {
						char := cleaned[i]
						fmt.Printf("位置 %d: '%c' (0x%02x, %d)\n", i, char, char, char)
					}
				}
			}
		} else {
			fmt.Printf("✅ JSON解析成功！\n")
		}
	}
}

// cleanEncoding 清理编码问题
func cleanEncoding(content string) string {
	var result strings.Builder
	
	for i, char := range content {
		// 只保留可打印字符、换行符和制表符
		if char >= 32 || char == '\n' || char == '\t' {
			result.WriteRune(char)
		} else {
			fmt.Printf("清理时移除位置 %d 的字符: 0x%02x\n", i, char)
		}
	}
	
	return result.String()
}
EOF

echo "运行编码问题调试..."
go run debug_json_encoding.go

echo "清理调试文件..."
rm -f debug_json_encoding.go

echo "=== 调试完成 ==="

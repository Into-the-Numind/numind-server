// +build ignore

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"numind-server/internal/numind/biz/salesrag/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run scripts/analyze_splitting.go <text_file>")
		fmt.Println("或者: echo '你的文本' | go run scripts/analyze_splitting.go -")
		os.Exit(1)
	}

	var text string
	if os.Args[1] == "-" {
		// 从标准输入读取
		data, err := os.ReadAll(os.Stdin)
		if err != nil {
			log.Fatal(err)
		}
		text = string(data)
	} else {
		// 从文件读取
		data, err := os.ReadFile(os.Args[1])
		if err != nil {
			log.Fatal(err)
		}
		text = string(data)
	}

	fmt.Println("========================================")
	fmt.Println("       切分策略分析工具")
	fmt.Println("========================================")
	fmt.Printf("\n文本长度: %d 字符\n", len(text))
	fmt.Printf("文本预览: %s...\n", truncate(text, 100))

	// 1. 检查语义切分器状态
	fmt.Println("\n----------------------------------------")
	fmt.Println("1. 语义切分器状态检查")
	fmt.Println("----------------------------------------")
	
	semanticSplitter := service.NewEmbeddingSplitter(service.EmbeddingSplitterConfig{
		Threshold:    0.6,
		MinChunkSize: 100,
		MaxChunkSize: 1000,
		OverlapSize:  100,
	})
	
	if semanticSplitter.IsAvailable() {
		fmt.Println("✅ 语义切分器可用")
	} else {
		fmt.Println("❌ 语义切分器不可用（将使用规则切分）")
	}

	// 2. 测试不同的切分策略
	fmt.Println("\n----------------------------------------")
	fmt.Println("2. 不同策略的切分结果")
	fmt.Println("----------------------------------------")

	// 策略 A: 仅规则切分
	fmt.Println("\n📋 策略 A: 仅规则切分 (RuleOnly)")
	ruleSplitter := service.NewEnhancedMarkdownSplitter(service.EnhancedSplitterConfig{
		MaxChunkSize:    1000,
		MinChunkSize:    200,
		OverlapSize:     100,
		EnableJieba:     true,
		ProtectMarkdown: true,
	})
	analyzeSplitting(ruleSplitter, text, "规则切分")

	// 策略 B: 自动选择
	fmt.Println("\n📋 策略 B: 自动选择 (Auto)")
	hybridSplitter := service.NewHybridSplitter(service.HybridSplitterConfig{
		RuleConfig: service.EnhancedSplitterConfig{
			MaxChunkSize:    1000,
			MinChunkSize:    200,
			OverlapSize:     100,
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: service.EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 100,
			MaxChunkSize: 1000,
			OverlapSize:  100,
		},
		Strategy:          service.StrategyAuto,
		SemanticMinLength: 2000,
	})
	
	chunks, details, err := hybridSplitter.SplitWithDetails(text)
	if err != nil {
		log.Printf("切分失败: %v", err)
	} else {
		fmt.Printf("   实际使用策略: %s\n", details["strategy"])
		fmt.Printf("   语义切分可用: %v\n", details["semantic_available"])
		if autoSelected, ok := details["auto_selected"].(string); ok {
			fmt.Printf("   自动选择结果: %s\n", autoSelected)
		}
		fmt.Printf("   切分块数: %d\n", len(chunks))
		
		for i, chunk := range chunks {
			fmt.Printf("\n   Chunk %d (%d 字符):\n", i+1, len(chunk.Content))
			fmt.Printf("      内容: %s\n", truncate(chunk.Content, 80))
			if len(chunk.Headers) > 0 {
				fmt.Printf("      标题: %v\n", chunk.Headers)
			}
			
			// 检查切分边界
			checkBoundary(chunk.Content)
		}
	}

	// 3. 详细分析
	fmt.Println("\n----------------------------------------")
	fmt.Println("3. 切分边界分析")
	fmt.Println("----------------------------------------")
	analyzeBoundaries(chunks)

	fmt.Println("\n========================================")
	fmt.Println("            分析完成")
	fmt.Println("========================================")
}

func analyzeSplitting(splitter interface{ Split(string) ([]service.EnhancedSplitChunk, error) }, text string, name string) {
	// 这里需要根据实际类型调整
}

func analyzeBoundaries(chunks []service.SplitChunk) {
	if len(chunks) <= 1 {
		fmt.Println("只有 1 个切片，无需分析边界")
		return
	}

	for i := 0; i < len(chunks)-1; i++ {
		currentEnd := chunks[i].Content
		nextStart := chunks[i+1].Content
		
		// 获取当前 chunk 的最后 20 个字符
		currentEndTrim := currentEnd
		if len(currentEndTrim) > 20 {
			currentEndTrim = currentEndTrim[len(currentEndTrim)-20:]
		}
		
		// 获取下一个 chunk 的前 20 个字符
		nextStartTrim := nextStart
		if len(nextStartTrim) > 20 {
			nextStartTrim = nextStartTrim[:20]
		}

		fmt.Printf("\n边界 %d → %d:\n", i+1, i+2)
		fmt.Printf("   Chunk %d 结尾: ...%q\n", i+1, currentEndTrim)
		fmt.Printf("   Chunk %d 开头: %q...\n", i+2, nextStartTrim)
		
		// 检查是否在句子边界切分
		if isSentenceBoundary(currentEnd, nextStart) {
			fmt.Printf("   ✅ 在句子边界切分\n")
		} else {
			fmt.Printf("   ⚠️  不在句子边界（可能有问题）\n")
		}
	}
}

func isSentenceBoundary(prevText, nextText string) bool {
	// 检查前一个文本是否以句子结束符结尾
	trimmed := strings.TrimSpace(prevText)
	if len(trimmed) == 0 {
		return false
	}
	
	lastChar := trimmed[len(trimmed)-1:]
	sentenceEndings := []string{".", "!", "?", "。", "！", "？", "；", ";", "\n"}
	
	for _, ending := range sentenceEndings {
		if lastChar == ending {
			return true
		}
	}
	
	// 检查下一个文本是否以大写字母或中文开头
	nextTrimmed := strings.TrimSpace(nextText)
	if len(nextTrimmed) == 0 {
		return false
	}
	
	firstChar := nextTrimmed[0:1]
	// 大写字母
	if firstChar >= "A" && firstChar <= "Z" {
		return true
	}
	// 中文汉字（粗略检查）
	if firstChar >= "\u4e00" && firstChar <= "\u9fff" {
		return true
	}
	
	return false
}

func checkBoundary(content string) {
	// 检查内容是否在句子中间切断
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return
	}
	
	firstLine := strings.TrimSpace(lines[0])
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	
	// 检查第一行是否以句子开始
	if len(firstLine) > 0 {
		firstChar := firstLine[0:1]
		if firstChar >= "a" && firstChar <= "z" {
			fmt.Printf("      ⚠️  可能从句子中间开始（小写字母开头）\n")
		}
	}
	
	// 检查最后一行是否以句子结束
	if len(lastLine) > 0 {
		lastChar := lastLine[len(lastLine)-1:]
		sentenceEndings := []string{".", "!", "?", "。", "！", "？"}
		isEnding := false
		for _, ending := range sentenceEndings {
			if lastChar == ending {
				isEnding = true
				break
			}
		}
		if !isEnding {
			fmt.Printf("      ⚠️  可能从句子中间结束（无句子结束符）\n")
		}
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

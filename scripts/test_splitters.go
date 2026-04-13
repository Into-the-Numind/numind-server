//go:build ignore
// +build ignore

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"numind-server/internal/numind/biz/salesrag/service"
)

func main() {
	fmt.Println("========================================")
	fmt.Println("Splitter Test Tool")
	fmt.Println("========================================")

	// 测试文本 - 模拟销售文档
	testText := `我们的产品是一款智能销售助手。
它可以帮助销售人员更好地管理客户关系。
通过分析客户行为，我们提供个性化建议。

价格是每月99元，包含所有功能。
如果您选择年付，可以享受8折优惠。
企业版支持自定义集成。

技术架构基于大语言模型。
我们使用RAG技术来增强回答质量。
向量数据库支持高效检索。
客服支持7x24小时在线。
有任何问题可以随时联系我们。`

	// 1. 测试规则切分器
	fmt.Println("\n📋 Testing Rule-Based Splitter...")
	ruleSplitter := service.NewEnhancedMarkdownSplitter(service.EnhancedSplitterConfig{
		MaxChunkSize:    300,
		MinChunkSize:    50,
		OverlapSize:     30,
		EnableJieba:     true,
		ProtectMarkdown: true,
	})

	start := time.Now()
	ruleChunks, err := ruleSplitter.Split(testText)
	ruleDuration := time.Since(start)

	if err != nil {
		log.Printf("❌ Rule splitter failed: %v", err)
	} else {
		fmt.Printf("✅ Rule splitter: %d chunks in %v\n", len(ruleChunks), ruleDuration)
		for i, c := range ruleChunks {
			fmt.Printf("   Chunk %d: %d chars\n", i+1, len(c.Content))
		}
	}

	// 2. 测试语义切分器
	fmt.Println("\n🧠 Testing Semantic Splitter...")
	semanticSplitter := service.NewEmbeddingSplitter(service.EmbeddingSplitterConfig{
		Threshold:    0.6,
		MinChunkSize: 50,
		MaxChunkSize: 300,
		OverlapSize:  30,
	})

	// 检查可用性
	if !semanticSplitter.IsAvailable() {
		fmt.Println("⚠️  Semantic splitter not available (Python/sentence-transformers not installed)")
		fmt.Println("   Install with: pip3 install sentence-transformers")
	} else {
		start = time.Now()
		semanticChunks, err := semanticSplitter.Split(testText)
		semanticDuration := time.Since(start)

		if err != nil {
			log.Printf("❌ Semantic splitter failed: %v", err)
		} else {
			fmt.Printf("✅ Semantic splitter: %d chunks in %v\n", len(semanticChunks), semanticDuration)
			for i, c := range semanticChunks {
				fmt.Printf("   Chunk %d: %d chars\n", i+1, len(c.Content))
			}
		}
	}

	// 3. 测试混合切分器
	fmt.Println("\n🔀 Testing Hybrid Splitter...")
	hybridSplitter := service.NewHybridSplitter(service.HybridSplitterConfig{
		RuleConfig: service.EnhancedSplitterConfig{
			MaxChunkSize:    300,
			MinChunkSize:    50,
			OverlapSize:     30,
			EnableJieba:     true,
			ProtectMarkdown: true,
		},
		SemanticConfig: service.EmbeddingSplitterConfig{
			Threshold:    0.6,
			MinChunkSize: 50,
			MaxChunkSize: 300,
			OverlapSize:  30,
		},
		Strategy:          service.StrategyAuto,
		SemanticMinLength: 200,
	})

	fmt.Printf("ℹ️  Semantic available: %v\n", hybridSplitter.IsSemanticAvailable())

	start = time.Now()
	hybridChunks, details, err := hybridSplitter.SplitWithDetails(testText)
	hybridDuration := time.Since(start)

	if err != nil {
		log.Printf("❌ Hybrid splitter failed: %v", err)
	} else {
		detailsJSON, _ := json.MarshalIndent(details, "", "  ")
		fmt.Printf("✅ Hybrid splitter: %d chunks in %v\n", len(hybridChunks), hybridDuration)
		fmt.Printf("   Details: %s\n", string(detailsJSON))
		for i, c := range hybridChunks {
			preview := c.Content
			if len(preview) > 50 {
				preview = preview[:50] + "..."
			}
			fmt.Printf("   Chunk %d: %d chars - %s\n", i+1, len(c.Content), preview)
		}
	}

	// 4. 测试适配器
	fmt.Println("\n🔌 Testing Splitter Adapter...")
	adapter := service.NewSplitterAdapter()

	start = time.Now()
	adapterChunks, err := adapter.Split(testText)
	adapterDuration := time.Since(start)

	if err != nil {
		log.Printf("❌ Adapter failed: %v", err)
	} else {
		fmt.Printf("✅ Adapter: %d chunks in %v\n", len(adapterChunks), adapterDuration)
	}

	// 总结
	fmt.Println("\n========================================")
	fmt.Println("Summary")
	fmt.Println("========================================")
	fmt.Printf("Rule-based:  %d chunks, %v\n", len(ruleChunks), ruleDuration)
	if semanticSplitter.IsAvailable() {
		// 重新获取语义切分结果用于显示
		semanticChunks, _ := semanticSplitter.Split(testText)
		fmt.Printf("Semantic:    %d chunks (available)\n", len(semanticChunks))
	} else {
		fmt.Println("Semantic:    not available")
	}
	fmt.Printf("Hybrid:      %d chunks, %v\n", len(hybridChunks), hybridDuration)
	fmt.Printf("Adapter:     %d chunks, %v\n", len(adapterChunks), adapterDuration)

	// 检查状态
	available, info, _ := service.CheckSemanticSplitterStatus()
	fmt.Printf("\nSemantic Splitter Status: %v\n", available)
	if !available {
		fmt.Println(info)
	}

	os.Exit(0)
}

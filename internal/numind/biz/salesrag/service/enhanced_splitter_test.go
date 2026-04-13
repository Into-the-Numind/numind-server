package service

import (
	"strings"
	"testing"
)

func TestEnhancedMarkdownSplitter_Split(t *testing.T) {
	markdown := `# 销售手册

## 第一章：客户开发
客户开发是销售的第一步。
你需要了解客户的需求。

### 开发渠道
1. 线上推广
2. 线下活动
3. 转介绍

## 第二章：产品介绍
产品是我们的核心竞争力。
产品特点：
- 高性能
- 易用性
- 低成本

这是第三章的开头但是内容很长需要被切分...（省略大量内容）
`

	cfg := EnhancedSplitterConfig{
		MaxChunkSize:    200,
		MinChunkSize:    50,
		OverlapSize:     30,
		EnableJieba:     true,
		ProtectMarkdown: true,
	}

	splitter := NewEnhancedMarkdownSplitter(cfg)
	chunks, err := splitter.Split(markdown)

	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("No chunks generated")
	}

	t.Logf("Generated %d chunks:", len(chunks))
	for i, chunk := range chunks {
		t.Logf("\n=== Chunk %d ===", i)
		t.Logf("Headers: %v", chunk.Headers)
		t.Logf("Content length: %d", len(chunk.Content))
		t.Logf("Core: %d-%d", chunk.CoreStart, chunk.CoreEnd)
		t.Logf("Preview: %s...", chunk.Content[:min(100, len(chunk.Content))])
	}
}

func TestEnhancedMarkdownSplitter_WithOverlap(t *testing.T) {
	// 测试重叠功能
	text := "第一段内容。这是第一段的主要信息。\n\n第二段内容。这是第二段的主要信息。\n\n第三段内容。这是第三段的主要信息。"

	cfg := EnhancedSplitterConfig{
		MaxChunkSize: 100,
		MinChunkSize: 20,
		OverlapSize:  20,
		EnableJieba:  false, // 简化测试
	}

	splitter := NewEnhancedMarkdownSplitter(cfg)
	chunks, err := splitter.Split(text)

	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	// 验证重叠
	for i := 1; i < len(chunks); i++ {
		curr := chunks[i].Content

		// 当前 chunk 应该包含前一 chunk 的尾部
		if !strings.Contains(curr, "上下文衔接") {
			t.Errorf("Chunk %d should contain overlap marker", i)
		}

		t.Logf("Chunk %d length: %d", i, len(curr))
		t.Logf("Chunk %d content preview: %s...", i, curr[:min(50, len(curr))])
	}
}

func TestEnhancedMarkdownSplitter_CodeBlockProtection(t *testing.T) {
	markdown := `# 代码文档

## 示例代码

` + "```go" + `
func main() {
    fmt.Println("Hello")
    fmt.Println("World")
}
` + "```" + `

## 说明
这是代码说明。
`

	cfg := EnhancedSplitterConfig{
		MaxChunkSize: 100,
		MinChunkSize: 20,
		OverlapSize:  10,
		EnableJieba:  false,
	}

	splitter := NewEnhancedMarkdownSplitter(cfg)
	chunks, err := splitter.Split(markdown)

	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	// 验证代码块没有被错误切分
	for i, chunk := range chunks {
		// 检查是否只有一个 ``` 开头或结尾（这表示被切断了）
		backticks := strings.Count(chunk.Content, "```")
		if backticks == 1 {
			t.Errorf("Chunk %d might have cut code block: %s", i, chunk.Content)
		}
	}
}

func TestCompatibilitySplitter(t *testing.T) {
	// 测试兼容性切分器
	cfg := SplitterConfig{
		MaxChunkSize: 200,
		MinChunkSize: 50,
	}

	splitter := NewCompatibilitySplitter(cfg)
	defer splitter.Close()

	markdown := `# 标题
第一段内容。

第二段内容。
`

	chunks, err := splitter.Split(markdown)
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("No chunks generated")
	}

	t.Logf("CompatibilitySplitter generated %d chunks", len(chunks))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestEnhancedMarkdownSplitter_ForceSplit(t *testing.T) {
	cfg := EnhancedSplitterConfig{
		MaxChunkSize: 1000,
		MinChunkSize: 200,
		OverlapSize:  100,
		EnableJieba:  false,
	}
	s := NewEnhancedMarkdownSplitter(cfg)

	// Construct a long string without any boundaries (no spaces, no newlines, no punctuation)
	// Total length 2500, should split into at least 3 chunks
	text := ""
	for i := 0; i < 2500; i++ {
		text += "A"
	}

	chunks, err := s.Split(text)
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	// Should generate at least 3 chunks (250 / 100 = 2.5)
	if len(chunks) < 3 {
		t.Errorf("Expected at least 3 chunks, got %d", len(chunks))
	}

	// Verify all chunks are within MaxChunkSize
	for i, c := range chunks {
		if len(c.Content) > cfg.MaxChunkSize {
			t.Errorf("Chunk %d exceeds MaxChunkSize: %d", i, len(c.Content))
		}
	}
}

func TestEnhancedMarkdownSplitter_ChineseSentence(t *testing.T) {
	cfg := EnhancedSplitterConfig{
		MaxChunkSize: 50,
		MinChunkSize: 10,
		OverlapSize:  0,
		EnableJieba:  false,
	}
	s := NewEnhancedMarkdownSplitter(cfg)

	// Chinese text where sentences are NOT followed by spaces
	text := "这是第一句话。这是第二句话。这是第三句话。这是第四句话。"
	// Each sentence is roughly 21 bytes in UTF-8.
	// Total length is ~84 bytes.

	chunks, err := s.Split(text)
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	// With improved sentence detection, it should split at the period
	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks split by Chinese periods, got %d", len(chunks))
	}
}

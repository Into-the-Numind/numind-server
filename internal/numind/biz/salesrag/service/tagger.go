package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/billing"
)

type ContentTagger struct {
	dmxClient *adapter.DMXAPIClient
}

func NewContentTagger() *ContentTagger {
	return &ContentTagger{dmxClient: adapter.NewDMXAPIClient()}
}

// TaggingResult structure matching JSON output from LLM
type TaggingResult struct {
	Tags    []string `json:"tags"`
	Summary string   `json:"summary"`
}

// TagChunks processes a slice of KnowledgeChunks in parallel, enriching them with metadata
func (t *ContentTagger) TagChunks(ctx context.Context, chunks []*domain.KnowledgeChunk) error {
	// 1. Concurrency Control (e.g., max 10 concurrent requests)
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for i := range chunks {
		wg.Add(1)
		go func(chunk *domain.KnowledgeChunk) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 2. Call LLM (Now DMXAPI)
			res, err := t.analyze(ctx, chunk.Content)
			if err != nil {
				// Fallback or Log
				// defaulting to basic tags if failure
				chunk.Tags = []string{"general"}
				fmt.Printf("Tagging failed for chunk: %v\n", err)
				return
			}

			// 3. Map Result
			t.mapResult(chunk, res)
		}(chunks[i])
	}
	wg.Wait()
	return nil
}

// TagChunk 单个切片打标 (实现 port.ContentTagger 接口)
func (t *ContentTagger) TagChunk(ctx context.Context, content string) ([]string, string, error) {
	res, err := t.analyze(ctx, content)
	if err != nil {
		return nil, "", err
	}

	return res.Tags, res.Summary, nil
}

func (t *ContentTagger) analyze(ctx context.Context, text string) (*TaggingResult, error) {
	// Construct Prompt
	prompt := fmt.Sprintf(`角色：销售知识专家
任务：分析文本内容并提取元数据，输出严格合法的 JSON 格式。
输入文本："""%s"""

输出格式：
{
 "tags": ["关键词1", "关键词2"],
 "summary": "简短摘要（不超过一句话）"
}

要求：
1. 提取 3-5 个最能代表内容的中文关键词（例如：产品功能、具体参数、常见问题）。
2. 生成一个非常简短的中文摘要，用于搜索结果预览。
3. 仅输出 JSON 字符串，不要包含 Markdown 格式（如 '''json ... '''）。`, text)

	var lastErr error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		// 设置计费上下文（如果尚未设置）
		tagCtx := ctx
		if billing.FromContext(ctx) == nil {
			tagCtx = billing.WithBilling(ctx, 0, "salesrag_tagging")
		}
		messages := []adapter.ChatMessage{{Role: "user", Content: prompt}}
		respStr, _, err := t.dmxClient.ChatCompletion(tagCtx, "qwen-turbo-latest", messages, 0.1, 1024)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Parse JSON
		cleaned := cleanJSON(respStr)
		var result TaggingResult
		if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
			lastErr = fmt.Errorf("json parse error: %w, raw: %s", err, respStr)
			// If parse failed, retry
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Success
		return &result, nil
	}

	return nil, fmt.Errorf("tagging failed after %d attempts: %w", maxRetries, lastErr)
}

func (t *ContentTagger) mapResult(chunk *domain.KnowledgeChunk, res *TaggingResult) {
	chunk.Tags = res.Tags
	chunk.Summary = res.Summary
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

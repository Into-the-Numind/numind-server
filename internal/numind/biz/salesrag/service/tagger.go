package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/salesrag/domain"
)

type ContentTagger struct {
	llm ali.AliBiz
}

func NewContentTagger(llm ali.AliBiz) *ContentTagger {
	return &ContentTagger{llm: llm}
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
		// Call DMXAPI directly
		respStr, err := t.callDMXAPI(prompt)
		if err != nil {
			// Network error, might be worth retrying or failing fast.
			// Let's retry on network error too.
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

// callDMXAPI invokes the Doubao-1.5-lite-32k model via DMXAPI
func (t *ContentTagger) callDMXAPI(prompt string) (string, error) {
	url := "https://www.dmxapi.cn/v1/chat/completions"
	apiKey := "sk-XgINDoE22MHQfcSZnToYICS4rNnoknIrXhZHZYs3VQM9DP25" // User provided key
	model := "Doubao-lite-32k"

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
		"max_tokens":  1024,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("DMXAPI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("DMXAPI error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response failed: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices from DMXAPI")
	}

	return result.Choices[0].Message.Content, nil
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

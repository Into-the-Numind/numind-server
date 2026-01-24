package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	SalesStage []string `json:"sales_stage"`
	Tags       []string `json:"tags"`
	Summary    string   `json:"summary"`
}

// TagChunks processes a slice of KnowledgeChunks in parallel, enriching them with metadata
func (t *ContentTagger) TagChunks(ctx context.Context, chunks []*domain.KnowledgeChunk) error {
	// 1. Concurrency Control (e.g., max 5 concurrent requests)
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for i := range chunks {
		wg.Add(1)
		go func(chunk *domain.KnowledgeChunk) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 2. Call LLM
			// Avoid processing too small chunks if necessary, but for now process all.
			// Truncate content if too long for prompt context window?
			// Assume chunks are < 2000 chars (from Splitter config).

			res, err := t.analyze(ctx, chunk.Content)
			if err != nil {
				// Fallback or Log
				// defaulting to DISCOVERY if failure
				chunk.SalesStage = []domain.SalesStage{domain.StageDiscovery}
				// TODO: Add logging
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
func (t *ContentTagger) TagChunk(ctx context.Context, content string) ([]domain.SalesStage, []string, error) {
	res, err := t.analyze(ctx, content)
	if err != nil {
		return nil, nil, err
	}

	// Convert strings to SalesStage
	stages := make([]domain.SalesStage, 0)
	for _, s := range res.SalesStage {
		st := domain.SalesStage(strings.ToUpper(s))
		if st == domain.StageDiscovery || st == domain.StageNegotiation || st == domain.StageClosing {
			stages = append(stages, st)
		}
	}
	if len(stages) == 0 {
		stages = []domain.SalesStage{domain.StageDiscovery}
	}
	return stages, res.Tags, nil
}

func (t *ContentTagger) analyze(ctx context.Context, text string) (*TaggingResult, error) {
	// Construct Prompt
	prompt := fmt.Sprintf(`Role: Sales Knowledge Expert.
Task: Analyze the text and assign metadata in strictly valid JSON format.
Input Text: """%s"""

Definitions:
- sales_stage: DISCOVERY, NEGOTIATION, CLOSING.

Output Schema:
{
 "sales_stage": ["DISCOVERY"],
 "tags": ["keyword1", "keyword2"],
 "summary": "brief summary"
}

Requirement: Output ONLY the JSON string. Do not use markdown blocks.`, text)

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	// Call AliBiz
	// Using generic params: maxTokens=500, temp=0.1 (deterministic)
	respStr, err := t.llm.QianwenTextStream(messages, 500, 0.1)
	if err != nil {
		return nil, err
	}

	// Parse JSON
	// Clean up potential markdown code blocks ```json ... ```
	cleaned := cleanJSON(respStr)
	var result TaggingResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("json parse error: %w, raw: %s", err, respStr)
	}

	return &result, nil
}

func (t *ContentTagger) mapResult(chunk *domain.KnowledgeChunk, res *TaggingResult) {

	// SalesStage
	chunk.SalesStage = make([]domain.SalesStage, 0)
	for _, s := range res.SalesStage {
		st := domain.SalesStage(strings.ToUpper(s))
		if st == domain.StageDiscovery || st == domain.StageNegotiation || st == domain.StageClosing {
			chunk.SalesStage = append(chunk.SalesStage, st)
		}
	}
	if len(chunk.SalesStage) == 0 {
		chunk.SalesStage = []domain.SalesStage{domain.StageDiscovery}
	}

	// Tags
	chunk.Tags = res.Tags
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

package service

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/salesrag/domain"

	"github.com/stretchr/testify/assert"
)

// MockAliBiz implementing only QianwenTextStream
type MockAliBiz struct {
	ali.AliBiz
	MockResponse string
	MockError    error
}

func (m *MockAliBiz) QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error) {
	return m.MockResponse, m.MockError
}

func TestContentTagger_TagChunks(t *testing.T) {
	// Mock successful JSON response
	mockJSON := "```json\n" +
		`{
		"sales_stage": ["NEGOTIATION"],
		"tags": ["price", "discount"],
		"summary": "Negotiation techniques"
	}` + "\n```"
	mockBiz := &MockAliBiz{
		MockResponse: mockJSON,
		MockError:    nil,
	}

	tagger := NewContentTagger(mockBiz)

	chunks := []*domain.KnowledgeChunk{
		{Content: "Some content about price negotiation..."},
		{Content: "Another chunk..."},
	} // 2 chunks

	err := tagger.TagChunks(context.Background(), chunks)
	assert.NoError(t, err)

	// Verify enrichment
	for _, c := range chunks {
		assert.Contains(t, c.SalesStage, domain.StageNegotiation)
		assert.Contains(t, c.Tags, "price")
	}
}

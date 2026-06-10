package service

import (
	"context"
	"testing"

	"numind-server/internal/pkg/retrieval/domain"

	"github.com/stretchr/testify/assert"
)

func TestContentTagger_TagChunks(t *testing.T) {
	// Note: This test now requires actual DMXAPI connectivity
	// Consider mocking HTTP client for proper unit testing
	t.Skip("Skipping test - requires DMXAPI connectivity or HTTP client mocking")

	tagger := NewContentTagger()

	chunks := []*domain.KnowledgeChunk{
		{Content: "Some content about price negotiation..."},
		{Content: "Another chunk..."},
	} // 2 chunks

	err := tagger.TagChunks(context.Background(), chunks)
	assert.NoError(t, err)

	// Verify enrichment
	for _, c := range chunks {
		// SalesStage is no longer enriched by tagger
		assert.Contains(t, c.Tags, "price")
	}
}

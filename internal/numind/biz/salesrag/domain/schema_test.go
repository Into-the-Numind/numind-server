package domain_test

import (
	"testing"
	"time"

	"numind-server/internal/numind/biz/salesrag/domain"

	"github.com/stretchr/testify/assert"
)

func TestKnowledgeChunkValidation(t *testing.T) {
	// Valid Chunk
	chunk := domain.KnowledgeChunk{
		ID:         "chunk_1",
		Content:    "产品价格是100元。",
		DocumentID: 1,
	}
	assert.Nil(t, chunk.Validate())

	// Invalid Chunk (Missing Content)
	invalidChunk := domain.KnowledgeChunk{
		DocumentID: 1,
	}
	assert.Error(t, invalidChunk.Validate())

	// Invalid Chunk (Missing DocumentID)
	invalidChunk2 := domain.KnowledgeChunk{
		Content: "Valid content",
	}
	assert.Error(t, invalidChunk2.Validate())
}

func TestKnowledgeDocumentValidation(t *testing.T) {
	doc := domain.KnowledgeDocument{
		ID:        1,
		Name:      "Sales Manual.pdf",
		Status:    domain.DocStatusPending,
		CreatedAt: time.Now(),
		UserID:    1,
	}
	assert.Nil(t, doc.Validate())

	invalidDoc := domain.KnowledgeDocument{
		Name:   "",
		UserID: 1,
	}
	assert.Error(t, invalidDoc.Validate())
}

package adapter_test

import (
	"context"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMemoryStore_UpsertAndSearch(t *testing.T) {
	// Initialize Mock Store
	store := adapter.NewMemoryStore()
	ctx := context.Background()

	// 1. Prepare Data
	chunk1 := domain.KnowledgeChunk{
		ID:         "c1",
		DocumentID: 1,
		Content:    "Product A is $100.",
		SalesStage: []domain.SalesStage{domain.StageNegotiation},
	}
	chunk2 := domain.KnowledgeChunk{
		ID:         "c2",
		DocumentID: 1,
		Content:    "Don't lower the price too early.",
		SalesStage: []domain.SalesStage{domain.StageNegotiation},
	}
	chunk3 := domain.KnowledgeChunk{ // Different doc
		ID:         "c3",
		DocumentID: 2,
		Content:    "Competitor is cheap.",
	}

	// 2. Test Upsert
	err := store.Upsert(ctx, []domain.KnowledgeChunk{chunk1, chunk2, chunk3})
	assert.Nil(t, err)

	// 3. Test Search (Exact match simulation for memory store)
	// Query: "Price" -> Should match chunk1
	filter := port.SearchFilter{}
	results, err := store.Search(ctx, "Product", filter, 10)
	assert.Nil(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, chunk1.ID, results[0].ID)

	// 4. Test Filter by SalesStage
	filterStrategy := port.SearchFilter{
		SalesStages: []domain.SalesStage{domain.StageNegotiation},
	}
	results, err = store.Search(ctx, "price", filterStrategy, 10)
	assert.Nil(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, chunk2.ID, results[0].ID)

	// 5. Test DeleteByDocumentID
	err = store.DeleteByDocumentID(ctx, 1)
	assert.Nil(t, err)

	// Should only find chunk3 now
	results, _ = store.Search(ctx, "Competitor", port.SearchFilter{}, 10)
	assert.Len(t, results, 1)
	assert.Equal(t, chunk3.ID, results[0].ID)
}

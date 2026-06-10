package adapter_test

import (
	"context"
	"numind-server/internal/pkg/retrieval/adapter"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
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
		UserID:     1,
		Content:    "Product A is $100.",
		Tags:       []string{"pricing", "product"},
	}
	chunk2 := domain.KnowledgeChunk{
		ID:         "c2",
		DocumentID: 1,
		UserID:     1,
		Content:    "Don't lower the price too early.",
		Tags:       []string{"pricing", "strategy"},
	}
	chunk3 := domain.KnowledgeChunk{ // Different doc
		ID:         "c3",
		DocumentID: 2,
		UserID:     1,
		Content:    "Competitor is cheap.",
		Tags:       []string{"competitor"},
	}

	// 2. Test Upsert
	err := store.Upsert(ctx, []domain.KnowledgeChunk{chunk1, chunk2, chunk3})
	assert.Nil(t, err)

	// 3. Test Search (Exact match simulation for memory store)
	// Query: "Product" -> Should match chunk1
	filter := port.SearchFilter{UserID: 1, DocumentIDs: []uint{1}}
	results, err := store.Search(ctx, "Product", filter, 10)
	assert.Nil(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, chunk1.ID, results[0].ID)

	// 4. Test Search with query
	results, err = store.Search(ctx, "price", port.SearchFilter{UserID: 1, DocumentIDs: []uint{1}}, 10)
	assert.Nil(t, err)
	assert.NotEmpty(t, results)

	// 5. Test DeleteByDocumentID
	err = store.DeleteByDocumentID(ctx, 1)
	assert.Nil(t, err)

	// Should only find chunk3 now
	results, _ = store.Search(ctx, "Competitor", port.SearchFilter{UserID: 1, DocumentIDs: []uint{2}}, 10)
	assert.Len(t, results, 1)
	assert.Equal(t, chunk3.ID, results[0].ID)
}

func TestMemoryStore_StrictFiltering(t *testing.T) {
	store := adapter.NewMemoryStore()
	ctx := context.Background()

	store.Upsert(ctx, []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 1, Content: "Info 1"},
		{ID: "c2", DocumentID: 2, UserID: 2, Content: "Info 2"},
	})

	// Case 1: Empty DocumentIDs -> Should return none
	res, _ := store.Search(ctx, "Info", port.SearchFilter{UserID: 1, DocumentIDs: nil}, 10)
	assert.Empty(t, res)

	// Case 2: Wrong UserID -> Should return none
	res, _ = store.Search(ctx, "Info", port.SearchFilter{UserID: 2, DocumentIDs: []uint{1}}, 10)
	assert.Empty(t, res)

	// Case 3: Correct match
	res, _ = store.Search(ctx, "Info", port.SearchFilter{UserID: 2, DocumentIDs: []uint{2}}, 10)
	assert.Len(t, res, 1)
}

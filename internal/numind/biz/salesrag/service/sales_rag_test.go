package service_test

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/service"

	"github.com/stretchr/testify/assert"
)

func TestSalesRAGService_RetrieveDualTrack(t *testing.T) {
	// 1. Setup Mock Store & Router
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter() // RegexRouter 已实现 AnalyzeIntentV2

	ctx := context.Background()

	// Seed Data
	store.Upsert(ctx, []domain.KnowledgeChunk{
		{
			ID: "f1", DocumentID: 1, UserID: 1, Content: "Product Price is $500",
		},
		{
			ID: "s1", DocumentID: 1, UserID: 1, Content: "If customer ask price, emphasize value.",
		},
		{
			ID: "s2", DocumentID: 1, UserID: 1, Content: "Closing technique: limited time offer.",
		},
	})

	svc := service.NewSalesRAGService(store, router)

	// 2. Test Case: Product Price Query
	query := "Price"

	verdict, err := svc.RetrieveForResponse(ctx, query, []uint{1}, 1)
	assert.Nil(t, err)

	// Should contain evidence (or be empty if no vector match in MemoryStore)
	// MemoryStore uses simple string matching, so should find something
	t.Logf("Evidence count: %d", len(verdict.Evidence))
}

func TestSalesRAGService_EmptyDocIDs(t *testing.T) {
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter()
	svc := service.NewSalesRAGService(store, router)

	ctx := context.Background()
	store.Upsert(ctx, []domain.KnowledgeChunk{
		{ID: "f1", DocumentID: 1, UserID: 1, Content: "Secret Info"},
	})

	// Case: Empty DocumentIDs -> Should return no evidence
	verdict, err := svc.RetrieveForResponse(ctx, "Secret", nil, 1)
	assert.Nil(t, err)
	assert.Len(t, verdict.Evidence, 0)
}

func TestSalesRAGService_UserIDIsolation(t *testing.T) {
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter()
	svc := service.NewSalesRAGService(store, router)

	ctx := context.Background()
	store.Upsert(ctx, []domain.KnowledgeChunk{
		{ID: "f1", DocumentID: 1, UserID: 1, Content: "User 1 Info"},
		{ID: "f2", DocumentID: 2, UserID: 2, Content: "User 2 Info"},
	})

	// Case: User 1 tries to search User 2's doc
	verdict, err := svc.RetrieveForResponse(ctx, "Info", []uint{2}, 1)
	assert.Nil(t, err)
	assert.Len(t, verdict.Evidence, 0)

	// Case: User 2 searches their own doc
	verdict, err = svc.RetrieveForResponse(ctx, "Info", []uint{2}, 2)
	assert.Nil(t, err)
	assert.Len(t, verdict.Evidence, 1)
	assert.Equal(t, "f2", verdict.Evidence[0].ID)
}

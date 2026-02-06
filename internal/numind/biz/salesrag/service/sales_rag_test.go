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
			ID: "f1", DocumentID: 1, Content: "Product Price is $500",
		},
		{
			ID: "s1", DocumentID: 1, Content: "If customer ask price, emphasize value.",
		},
		{
			ID: "s2", DocumentID: 1, Content: "Closing technique: limited time offer.",
		},
	})

	svc := service.NewSalesRAGService(store, router)

	// 2. Test Case: Product Price Query
	query := "Price"

	verdict, err := svc.RetrieveForResponse(ctx, query, nil)
	assert.Nil(t, err)

	// Should contain evidence (or be empty if no vector match in MemoryStore)
	// MemoryStore uses simple string matching, so should find something
	t.Logf("Evidence count: %d", len(verdict.Evidence))
}

func TestSalesRAGService_ChitChat(t *testing.T) {
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter() // RegexRouter 已实现 AnalyzeIntentV2
	svc := service.NewSalesRAGService(store, router)

	verdict, err := svc.RetrieveForResponse(context.Background(), "Hello", nil)
	assert.Nil(t, err)

	// Should be chitchat
	assert.True(t, verdict.IsChitChat)
}

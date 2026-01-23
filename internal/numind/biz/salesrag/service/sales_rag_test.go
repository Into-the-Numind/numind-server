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
	router := adapter.NewRegexRouter()

	ctx := context.Background()

	// Seed Data
	store.Upsert(ctx, []domain.KnowledgeChunk{
		{
			ID: "f1", DocumentID: 1, Content: "Product Price is $500",
			DocType: domain.DocTypeFact,
		},
		{
			ID: "s1", DocumentID: 1, Content: "If customer ask price, emphasize value.",
			DocType:    domain.DocTypeStrategy,
			SalesStage: []domain.SalesStage{domain.StageNegotiation},
		},
		{
			ID: "s2", DocumentID: 1, Content: "Closing technique: limited time offer.",
			DocType:    domain.DocTypeStrategy,
			SalesStage: []domain.SalesStage{domain.StageClosing}, // Different stage
		},
	})

	svc := service.NewSalesRAGService(store, router)

	// 2. Test Case: Negotiation Stage
	// Intent -> Direct "Product Price"
	query := "Price"
	stage := domain.StageNegotiation

	verdict, err := svc.RetrieveForResponse(ctx, query, stage, nil)
	assert.Nil(t, err)

	// Should allow Facts
	assert.NotEmpty(t, verdict.Facts)
	assert.Equal(t, "Product Price is $500", verdict.Facts[0].Content)

	// Should allow Strategy for Negotiation
	assert.NotEmpty(t, verdict.Strategies)
	assert.Equal(t, "If customer ask price, emphasize value.", verdict.Strategies[0].Content)

	// Should NOT include Closing strategy
	for _, s := range verdict.Strategies {
		assert.NotEqual(t, "Closing technique: limited time offer.", s.Content)
	}
}

func TestSalesRAGService_ChitChat(t *testing.T) {
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter()
	svc := service.NewSalesRAGService(store, router)

	verdict, err := svc.RetrieveForResponse(context.Background(), "Hello", domain.StageDiscovery, nil)
	assert.Nil(t, err)

	// Should be empty for chitchat
	assert.Empty(t, verdict.Facts)
	assert.Empty(t, verdict.Strategies)
	assert.Equal(t, true, verdict.IsChitChat)
}

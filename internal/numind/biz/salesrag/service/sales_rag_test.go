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
		},
		{
			ID: "s1", DocumentID: 1, Content: "If customer ask price, emphasize value.",
			SalesStage: []domain.SalesStage{domain.StageNegotiation},
		},
		{
			ID: "s2", DocumentID: 1, Content: "Closing technique: limited time offer.",
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

	// Should contain evidence
	assert.NotEmpty(t, verdict.Evidence)

	// We expect relevant chunks to be retrieved (simple string match in mock)
	found := false
	for _, e := range verdict.Evidence {
		if e.Content == "Product Price is $500" {
			found = true
			break
		}
	}
	assert.True(t, found, "Should retrieve product price fact")
}

func TestSalesRAGService_ChitChat(t *testing.T) {
	store := adapter.NewMemoryStore()
	router := adapter.NewRegexRouter()
	svc := service.NewSalesRAGService(store, router)

	verdict, err := svc.RetrieveForResponse(context.Background(), "Hello", domain.StageDiscovery, nil)
	assert.Nil(t, err)

	// Should be empty for chitchat
	assert.Empty(t, verdict.Evidence)
	assert.Equal(t, true, verdict.IsChitChat)
}

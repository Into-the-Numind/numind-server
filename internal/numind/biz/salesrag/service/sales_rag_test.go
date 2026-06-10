package service_test

import (
	"context"
	"os"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/service"
	aiservice "numind-server/internal/pkg/aiservice"
	radapter "numind-server/internal/pkg/retrieval/adapter"
	"numind-server/internal/pkg/retrieval/domain"

	"github.com/stretchr/testify/assert"
)

// TestMain initialises a minimal aiservice singleton so that tests which
// exercise code paths reaching aiservice.Rerank() do not panic on Default().
// The gateway has no registry and no providers, so Rerank calls return an
// error rather than making real network requests — which is acceptable for
// unit tests that only exercise the retrieval path (not the rerank path).
func TestMain(m *testing.M) {
	gw := aiservice.Build(aiservice.Deps{}) // no DB, no providers; Rerank returns error, not panic
	aiservice.SetDefault(gw)
	os.Exit(m.Run())
}

func TestSalesRAGService_RetrieveDualTrack(t *testing.T) {
	// 1. Setup Mock Store & Router
	store := radapter.NewMemoryStore()
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
	store := radapter.NewMemoryStore()
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
	store := radapter.NewMemoryStore()
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

// TestSalesRAGService_OpinionOnly_MainEmpty 回归测试（T1.6 spec-compliance review P1）：
// 主通道 docIDs 为空（用户只配了观点库、未配产品/案例/FAQ）时，opinion 通道仍须独立
// 检索——不能因主通道 ErrEmptyScope 被整体跳过（否则只配观点库的 session 丢失全部观点证据）。
func TestSalesRAGService_OpinionOnly_MainEmpty(t *testing.T) {
	store := radapter.NewMemoryStore()
	router := adapter.NewRegexRouter()
	svc := service.NewSalesRAGService(store, router)

	ctx := context.Background()
	store.Upsert(ctx, []domain.KnowledgeChunk{
		{ID: "op1", DocumentID: 2, UserID: 1, Content: "Opinion: emphasize price value over discount"},
	})

	// 主通道 docIDs=nil（空）、opinion docIDs=[2] 非空 → opinion 必须仍被检索。
	verdict, err := svc.RetrieveForResponseV2(ctx, "Price", nil, []uint{2}, nil, "free", 1, nil)
	assert.Nil(t, err)
	assert.Len(t, verdict.Evidence, 0, "主通道空应无常规 Evidence")
	assert.NotEmpty(t, verdict.OpinionEvidence, "主通道空时 opinion 仍须检索到证据（T1.6 P1 回归保护）")
	if len(verdict.OpinionEvidence) > 0 {
		assert.Equal(t, "op1", verdict.OpinionEvidence[0].ID)
	}
}

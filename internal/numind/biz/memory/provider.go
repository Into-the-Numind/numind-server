package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// MemoryProvider is the top-level interface for the two-layer memory subsystem.
// It is wired into AgentRunner via WithMemoryProvider and called at key points
// in the run lifecycle.
type MemoryProvider interface {
	// SystemPromptBlock returns the fully-rendered <memory-context> segment for
	// injection into the system prompt. Returns "" when there is no memory to
	// inject (empty stores or userID==0).
	SystemPromptBlock(ctx context.Context, userID uint, agentDefID uint64, sessionID string) (string, error)

	// Prefetch returns structured MemoryItems before a turn begins.
	// v1: semantically equivalent to calling RetrieveL1 + RetrieveL2 and
	// combining results; callers may use the items for UI preview or re-ranking.
	Prefetch(ctx context.Context, userID uint, agentDefID uint64, query string) ([]MemoryItem, error)

	// SyncTurn is called after each conversation turn to persist turn-level
	// memories. v1: no-op — returns nil.
	SyncTurn(ctx context.Context, userID uint, agentDefID uint64, sessionID string, userMsg, assistantMsg Message) error

	// OnPreCompress is called before context compaction to allow the memory
	// subsystem to harvest summaries from the about-to-be-compressed messages.
	// v1: no-op — returns nil.
	OnPreCompress(ctx context.Context, userID uint, agentDefID uint64, msgs []Message) error

	// Clear deletes all L1 and L2 memory for the given user.
	Clear(ctx context.Context, userID uint) error
}

// compositeProvider combines L1 + L2 stores through a Retriever and FenceRenderer
// to implement MemoryProvider.
type compositeProvider struct {
	l1Store   store.IAgentSessionMemoryStore
	l2Store   store.IUserGlobalMemoryStore
	retriever Retriever
	fence     *FenceRenderer
}

// NewProvider constructs a MemoryProvider backed by the given L1 and L2 stores.
// v1 uses NewRetriever() (SQL LIKE + recency boost) and NewFenceRenderer().
// Pass RetrieverOption (e.g. WithEmbedder) to override defaults.
// Agent Mode #14/14 A2: biz.go wires NewProvider(l1, l2, memory.WithEmbedder(memory.NewAIServiceEmbedder()))
// to enable real aiservice.Embed call (was mockEmbedder zero-vector).
func NewProvider(l1 store.IAgentSessionMemoryStore, l2 store.IUserGlobalMemoryStore, retrieverOpts ...RetrieverOption) MemoryProvider {
	return &compositeProvider{
		l1Store:   l1,
		l2Store:   l2,
		retriever: NewRetriever(retrieverOpts...),
		fence:     NewFenceRenderer(),
	}
}

// SystemPromptBlock assembles the <memory-context> segment for system prompt injection.
//
// Behaviour:
//   - userID == 0 → return "", nil (unauthenticated / no-op context)
//   - L1 failure → log warn + degrade to L2 only
//   - L2 failure → log warn + degrade to L1 only
//   - Both empty or both failed → return "", nil
//   - Otherwise → delegate to fence.RenderMemoryBlock(l1Items, l2Items)
func (p *compositeProvider) SystemPromptBlock(ctx context.Context, userID uint, agentDefID uint64, sessionID string) (string, error) {
	if userID == 0 {
		return "", nil
	}

	// L1: top-K=5, ordered by recency + BM25 boost (query="" in SystemPromptBlock path).
	l1Items, l1Err := p.retriever.RetrieveL1(ctx, p.l1Store, userID, agentDefID, "", 5)
	if l1Err != nil {
		log.Warnw("memory.SystemPromptBlock L1 failed; degrading to L2 only",
			"user_id", userID, "agent_def_id", agentDefID, "error", l1Err)
		l1Items = nil
	}

	// L2: top-K=3 per kind (fact + preference).
	l2Items, l2Err := p.retriever.RetrieveL2(ctx, p.l2Store, userID, 3)
	if l2Err != nil {
		log.Warnw("memory.SystemPromptBlock L2 failed; degrading to L1 only",
			"user_id", userID, "error", l2Err)
		l2Items = nil
	}

	if len(l1Items) == 0 && len(l2Items) == 0 {
		return "", nil
	}

	return p.fence.RenderMemoryBlock(l1Items, l2Items), nil
}

// Prefetch returns structured MemoryItems for pre-turn consumption.
// v1: runs a single L1 retrieval using query as the keyword hint plus L2 items,
// combined into a single slice (L2 first, then L1 — mirrors SystemPromptBlock order).
func (p *compositeProvider) Prefetch(ctx context.Context, userID uint, agentDefID uint64, query string) ([]MemoryItem, error) {
	if userID == 0 {
		return nil, nil
	}

	l1Items, err := p.retriever.RetrieveL1(ctx, p.l1Store, userID, agentDefID, query, 5)
	if err != nil {
		return nil, err
	}
	l2Items, err := p.retriever.RetrieveL2(ctx, p.l2Store, userID, 3)
	if err != nil {
		return nil, err
	}

	out := make([]MemoryItem, 0, len(l2Items)+len(l1Items))
	out = append(out, l2Items...)
	out = append(out, l1Items...)
	return out, nil
}

// SyncTurn runs an async LLM extraction over (userMsg, asstMsg) and writes
// 0-3 extracted facts/preferences into L1 memory. Designed to never block the
// caller — all errors are logged + swallowed.
//
// Agent Mode #14/A3: replaces v1 stub `return nil` with real aiservice.Chat call.
func (p *compositeProvider) SyncTurn(ctx context.Context, userID uint, agentDefID uint64, sessionID string, userMsg, assistantMsg Message) error {
	content := fmt.Sprintf("用户：%s\n助手：%s", userMsg.Content, assistantMsg.Content)
	resp, err := chatFn(ctx, profile.AgentSyncTurn, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: SyncTurnSystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: content}},
		},
		ResponseFormat: aiservice.ResponseFormatJSONObject,
		MaxTokens:      300,
		Temperature:    0.2,
	})
	if err != nil {
		log.Warnw("memory SyncTurn LLM call failed", "userID", userID, "agentDefID", agentDefID, "error", err)
		return nil // silent fail
	}
	var result SyncTurnResult
	if uerr := json.Unmarshal([]byte(resp.Content), &result); uerr != nil {
		log.Warnw("memory SyncTurn JSON unmarshal failed", "userID", userID, "raw_len", len(resp.Content))
		return nil
	}
	for _, item := range result.Items {
		if item.Confidence < 0.5 {
			continue
		}
		// P1-1 fix (S3 reviewer): use l1Store.Create, NOT notepad.AppendL1 (method does not exist).
		// EscapeForStorage is a package-level function (not a FenceRenderer method).
		escaped := EscapeForStorage(item.Content)
		row := &model.AgentSessionMemory{
			UserID:            userID,
			AgentDefinitionID: agentDefID,
			Kind:              item.Kind,
			Content:           escaped,
			Score:             item.Confidence,
			SourceType:        "sync_turn",
			RecencyAt:         time.Now(),
		}
		if cerr := p.l1Store.Create(ctx, row); cerr != nil {
			log.Warnw("memory SyncTurn L1 Create failed", "error", cerr)
			continue
		}
	}
	return nil
}

// OnPreCompress is a v1 no-op. The #14 compaction feature will harvest summaries here.
func (p *compositeProvider) OnPreCompress(_ context.Context, _ uint, _ uint64, _ []Message) error {
	return nil
}

// Clear deletes all L1 and L2 memory rows for the given user.
// Both deletions are attempted; errors from either are returned (L1 takes priority).
func (p *compositeProvider) Clear(ctx context.Context, userID uint) error {
	l1Err := p.l1Store.DeleteByUser(ctx, userID)
	l2Err := p.l2Store.DeleteByUser(ctx, userID)
	if l1Err != nil {
		return l1Err
	}
	return l2Err
}

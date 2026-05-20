package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"numind-server/internal/numind/store"
)

// BM25Searcher abstracts keyword-based search over a table.
// v1: inline SQL LIKE logic inside retrieverImpl.
// v2 swap point: inject a proper BM25 implementation here.
type BM25Searcher interface {
	// Search returns (ids, scores, error) for the given query over the specified
	// table and fields, capped at limit results.
	Search(ctx context.Context, table string, fields []string, query string, limit int) ([]uint64, []float64, error)
}

// VectorStore abstracts ANN (approximate nearest-neighbour) vector search.
// v1: placeholder — always returns empty results.
// v2 swap point: inject a real vector DB (e.g. Milvus, pgvector) implementation here.
type VectorStore interface {
	// Query returns (ids, scores, error) for the top-K nearest neighbours of embedding.
	Query(ctx context.Context, collection string, embedding []float32, topK int) ([]uint64, []float64, error)
}

// Retriever fetches the most relevant MemoryItems from L1 and L2 stores.
type Retriever interface {
	// RetrieveL1 returns up to topK scored MemoryItems from the L1 store for
	// the given (userID, agentDefID), optionally boosted by query keyword match
	// and decayed by recency.
	RetrieveL1(ctx context.Context, s store.IAgentSessionMemoryStore, userID uint, agentDefID uint64, query string, topK int) ([]MemoryItem, error)

	// RetrieveL2 returns up to topKPerKind MemoryItems per kind (fact +
	// preference) from the L2 store for the given userID.
	RetrieveL2(ctx context.Context, s store.IUserGlobalMemoryStore, userID uint, topKPerKind int) ([]MemoryItem, error)
}

// retrieverImpl is the v1 implementation of Retriever.
// bm25 and vector are v2 swap points; embedder is v1 mockEmbedder.
// v1 inline logic (SQL LIKE + recency boost) is used directly in RetrieveL1 —
// per P2-6 decision, these are not exposed as separate named struct impls.
type retrieverImpl struct {
	bm25     BM25Searcher // v2 swap point
	vector   VectorStore  // v2 swap point
	embedder Embedder     // v1 mockEmbedder
}

// NewRetriever constructs a v1 Retriever using mockEmbedder and nil bm25/vector
// (inline SQL LIKE + recency boost, no external search service).
func NewRetriever() Retriever {
	return &retrieverImpl{
		embedder: NewMockEmbedder(),
	}
}

// RetrieveL1 fetches alive L1 records for (userID, agentDefID), applies
// BM25 keyword boost and recency decay, then returns top-K by score.
//
// Scoring pipeline (spec §4.6, P2-1 ordering decision):
//  1. Pull alive records from store (limit 50, recency_at desc).
//  2. BM25 boost first: if query is non-empty and item.Content contains the
//     query (case-insensitive), score *= 1.5.
//  3. Recency decay: score *= exp(-age_days / 30).
//  4. Sort by score desc, take top-K.
//
// The BM25-then-decay ordering ensures a keyword-matching record aged ~12 days
// (ln(1.5)*30 ≈ 12) can still beat a fresh but irrelevant record
// (0.37 boost = 1.5 * exp(-12/30) ≈ 1.0).
func (r *retrieverImpl) RetrieveL1(
	ctx context.Context,
	s store.IAgentSessionMemoryStore,
	userID uint,
	agentDefID uint64,
	query string,
	topK int,
) ([]MemoryItem, error) {
	rows, err := s.ListByUserAgent(ctx, userID, agentDefID, store.ListOpts{
		AliveOnly: true,
		Limit:     50,
		OrderBy:   "recency_at desc",
	})
	if err != nil {
		return nil, fmt.Errorf("RetrieveL1: %w", err)
	}

	items := make([]MemoryItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, MemoryItem{
			ID:                      row.ID,
			Kind:                    MemoryKind(row.Kind),
			Content:                 row.Content,
			Score:                   row.Score,
			SourceType:              SourceType(row.SourceType),
			SourceAgentDefinitionID: row.SourceAgentDefinitionID,
			CreatedAt:               row.CreatedAt,
			UpdatedAt:               row.UpdatedAt,
			RecencyAt:               row.RecencyAt,
			AgentDefinitionID:       row.AgentDefinitionID,
		})
	}

	// Step 1: BM25 boost (applied before recency decay — spec §4.6 P2-1).
	if query != "" {
		lq := strings.ToLower(query)
		for i := range items {
			if strings.Contains(strings.ToLower(items[i].Content), lq) {
				items[i].Score *= 1.5
			}
		}
	}

	// Step 2: Recency decay — score *= exp(-age_days / 30).
	now := time.Now()
	for i := range items {
		ageDays := now.Sub(items[i].RecencyAt).Hours() / 24
		items[i].Score *= math.Exp(-ageDays / 30)
	}

	// Step 3: Sort by score desc, take top-K.
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	if topK > 0 && len(items) > topK {
		items = items[:topK]
	}

	return items, nil
}

// RetrieveL2 fetches up to topKPerKind entries for each of the two L2 kinds
// (fact and preference) and returns the combined slice.
//
// v1 simplification (spec §4.7): only fact + preference are fetched as they
// represent the user's baseline profile injected at agent startup.
// learning / decision / issue kinds are retrieved lazily (v2).
func (r *retrieverImpl) RetrieveL2(
	ctx context.Context,
	s store.IUserGlobalMemoryStore,
	userID uint,
	topKPerKind int,
) ([]MemoryItem, error) {
	out := make([]MemoryItem, 0, topKPerKind*2)

	for _, kind := range []string{"fact", "preference"} {
		rows, err := s.ListByUserKind(ctx, userID, kind, topKPerKind)
		if err != nil {
			return nil, fmt.Errorf("RetrieveL2 kind=%s: %w", kind, err)
		}
		for _, row := range rows {
			out = append(out, MemoryItem{
				ID:                      row.ID,
				Kind:                    MemoryKind(row.Kind),
				Content:                 row.Value, // L2 field mapping: value → Content
				KeyName:                 row.KeyName,
				Confidence:              row.Confidence,
				SourceType:              SourceType(row.SourceType),
				SourceAgentDefinitionID: row.SourceAgentDefinitionID,
				CreatedAt:               row.CreatedAt,
				UpdatedAt:               row.UpdatedAt,
			})
		}
	}

	return out, nil
}

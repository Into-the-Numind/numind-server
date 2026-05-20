package memory

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestRetriever returns a Retriever and the backing SQLite stores for L1/L2.
func newTestRetriever(t *testing.T) (Retriever, store.IAgentSessionMemoryStore, store.IUserGlobalMemoryStore) {
	t.Helper()
	db := newTestDB(t, &model.AgentSessionMemory{}, &model.UserGlobalMemory{})
	l1 := store.NewAgentSessionMemoryStore(db)
	l2 := store.NewUserGlobalMemoryStore(db)
	r := NewRetriever()
	return r, l1, l2
}

// seedL1 creates one AgentSessionMemory row with given fields and returns its ID.
func seedL1(t *testing.T, s store.IAgentSessionMemoryStore, userID uint, agentDefID uint64, content string, score float64, recencyAt time.Time, expiresAt *time.Time) uint64 {
	t.Helper()
	ctx := context.Background()
	m := &model.AgentSessionMemory{
		UserID:            userID,
		AgentDefinitionID: agentDefID,
		Kind:              string(KindFact),
		Content:           content,
		Score:             score,
		SourceType:        string(SourceAgent),
		RecencyAt:         recencyAt,
		ExpiresAt:         expiresAt,
	}
	require.NoError(t, s.Create(ctx, m))
	return m.ID
}

// seedL2 creates one UserGlobalMemory row and returns its ID.
func seedL2(t *testing.T, s store.IUserGlobalMemoryStore, userID uint, kind, key, value string) uint64 {
	t.Helper()
	ctx := context.Background()
	m := &model.UserGlobalMemory{
		UserID:     userID,
		Kind:       kind,
		KeyName:    key,
		Value:      value,
		Confidence: 1.0,
		SourceType: string(SourceAgentTool),
	}
	require.NoError(t, s.Upsert(ctx, m))
	return m.ID
}

// ---------------------------------------------------------------------------
// RetrieveL1 tests
// ---------------------------------------------------------------------------

// TestRetrieveL1_RecencyBoost verifies that a record created 30 days ago has its
// score decayed to score * exp(-1) ≈ 0.368.
func TestRetrieveL1_RecencyBoost(t *testing.T) {
	r, l1, _ := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID     uint   = 1
		agentDefID uint64 = 10
	)

	// Seed one record with score=1.0, recency_at = 30 days ago.
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
	seedL1(t, l1, userID, agentDefID, "old fact", 1.0, thirtyDaysAgo, nil)

	items, err := r.RetrieveL1(ctx, l1, userID, agentDefID, "", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// After 30 days decay: score ≈ 1.0 * exp(-30/30) = exp(-1) ≈ 0.3679
	expected := 1.0 * math.Exp(-1)
	assert.InDelta(t, expected, items[0].Score, 0.01,
		"30-day old record should have score ≈ exp(-1) after recency decay")
}

// TestRetrieveL1_BM25Boost verifies that a record whose content matches the query
// gets its score multiplied by 1.5 (applied before recency decay).
func TestRetrieveL1_BM25Boost(t *testing.T) {
	r, l1, _ := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID     uint   = 2
		agentDefID uint64 = 20
	)

	// Seed two records: one matches query, one does not. Both created just now.
	now := time.Now()
	seedL1(t, l1, userID, agentDefID, "loves golang programming", 1.0, now, nil)
	seedL1(t, l1, userID, agentDefID, "enjoys coffee in the morning", 1.0, now, nil)

	items, err := r.RetrieveL1(ctx, l1, userID, agentDefID, "golang", 10)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// Both records are from now, so decay is negligible (~exp(0) = 1.0).
	// The "golang" match should be ranked first with score ≈ 1.5 * decay.
	assert.Contains(t, items[0].Content, "golang",
		"BM25-matching record should be ranked first")

	// Score of first item should be larger than second (keyword boost applied).
	assert.Greater(t, items[0].Score, items[1].Score,
		"keyword-matching record should have higher score than non-matching")
}

// TestRetrieveL1_BothBoosts verifies that when a record matches the query AND is
// fresh, it outranks an older record that matches the query with heavier decay.
func TestRetrieveL1_BothBoosts(t *testing.T) {
	r, l1, _ := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID     uint   = 3
		agentDefID uint64 = 30
	)

	// Record A: matches query "golang", score=1.0, created just now.
	// Record B: matches query "golang", score=1.0, created 30 days ago.
	now := time.Now()
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	seedL1(t, l1, userID, agentDefID, "loves golang", 1.0, now, nil)
	seedL1(t, l1, userID, agentDefID, "golang expert", 1.0, thirtyDaysAgo, nil)

	items, err := r.RetrieveL1(ctx, l1, userID, agentDefID, "golang", 10)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// Record A: 1.0 * 1.5 (boost) * exp(≈0) ≈ 1.5
	// Record B: 1.0 * 1.5 (boost) * exp(-1) ≈ 0.55
	// A should be ranked first.
	assert.Greater(t, items[0].Score, items[1].Score,
		"fresher keyword-matching record should outrank older one")

	// Verify the order: A (fresh) first, B (older) second.
	assert.InDelta(t, 1.5, items[0].Score, 0.05,
		"fresh+keyword record: score ≈ 1.5 * 1.0 (negligible decay)")
	expectedOld := 1.5 * math.Exp(-1)
	assert.InDelta(t, expectedOld, items[1].Score, 0.05,
		"old+keyword record: score ≈ 1.5 * exp(-1) ≈ 0.55")
}

// TestRetrieveL1_AliveFilter verifies that records with expires_at in the past
// are not returned by RetrieveL1.
func TestRetrieveL1_AliveFilter(t *testing.T) {
	r, l1, _ := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID     uint   = 4
		agentDefID uint64 = 40
	)

	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(24 * time.Hour)

	// Expired record — should NOT appear in results.
	seedL1(t, l1, userID, agentDefID, "expired memory", 1.0, now, &past)
	// Still-alive record — should appear.
	seedL1(t, l1, userID, agentDefID, "alive memory", 1.0, now, &future)
	// No expires_at (NULL) — should appear (permanent).
	seedL1(t, l1, userID, agentDefID, "permanent memory", 1.0, now, nil)

	items, err := r.RetrieveL1(ctx, l1, userID, agentDefID, "", 10)
	require.NoError(t, err)

	// Only the alive and permanent records should be returned.
	require.Len(t, items, 2, "expired record must be filtered out")

	contents := make([]string, len(items))
	for i, item := range items {
		contents[i] = item.Content
	}
	assert.NotContains(t, contents, "expired memory",
		"expired record must not appear in results")
	assert.Contains(t, contents, "alive memory")
	assert.Contains(t, contents, "permanent memory")
}

// TestRetrieveL1_TopK verifies that when there are 50 alive records and topK=5,
// exactly 5 items are returned ordered by score desc.
func TestRetrieveL1_TopK(t *testing.T) {
	r, l1, _ := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID     uint   = 5
		agentDefID uint64 = 50
		numRecords        = 50
		topK              = 5
	)

	now := time.Now()
	for i := 0; i < numRecords; i++ {
		// Vary score slightly so we have a deterministic ordering.
		score := float64(i+1) * 0.01
		recency := now.Add(-time.Duration(i) * time.Minute) // i-th minute in the past
		seedL1(t, l1, userID, agentDefID, "item content", score, recency, nil)
	}

	items, err := r.RetrieveL1(ctx, l1, userID, agentDefID, "", topK)
	require.NoError(t, err)
	assert.Len(t, items, topK, "exactly topK items should be returned")

	// Verify ordering: each item's score >= the next.
	for i := 1; i < len(items); i++ {
		assert.GreaterOrEqual(t, items[i-1].Score, items[i].Score,
			"items should be sorted by score desc")
	}
}

// ---------------------------------------------------------------------------
// RetrieveL2 tests
// ---------------------------------------------------------------------------

// TestRetrieveL2_FactAndPreference verifies that RetrieveL2 only returns
// fact and preference records, even when learning records exist.
func TestRetrieveL2_FactAndPreference(t *testing.T) {
	r, _, l2 := newTestRetriever(t)
	ctx := context.Background()

	const userID uint = 6

	// Seed records of three kinds.
	seedL2(t, l2, userID, "fact", "lang", "Go")
	seedL2(t, l2, userID, "preference", "theme", "dark")
	seedL2(t, l2, userID, "learning", "lesson", "channels rock") // should NOT appear

	items, err := r.RetrieveL2(ctx, l2, userID, 10)
	require.NoError(t, err)

	// Only fact and preference should be present.
	for _, item := range items {
		assert.True(t,
			item.Kind == KindFact || item.Kind == KindPreference,
			"RetrieveL2 must only return fact and preference, got: %s", item.Kind,
		)
	}
	assert.Len(t, items, 2, "exactly fact + preference entries should be returned")
}

// TestRetrieveL2_TopKPerKind verifies that when each kind has 5 records and
// topKPerKind=3, the result contains exactly 6 items (3 per kind).
func TestRetrieveL2_TopKPerKind(t *testing.T) {
	r, _, l2 := newTestRetriever(t)
	ctx := context.Background()

	const (
		userID      uint = 7
		topKPerKind int  = 3
	)

	// Seed 5 fact entries and 5 preference entries.
	for i := 0; i < 5; i++ {
		seedL2(t, l2, userID, "fact", "fact-key-"+string(rune('A'+i)), "fact value")
		seedL2(t, l2, userID, "preference", "pref-key-"+string(rune('A'+i)), "pref value")
	}

	items, err := r.RetrieveL2(ctx, l2, userID, topKPerKind)
	require.NoError(t, err)
	assert.Len(t, items, topKPerKind*2,
		"should return exactly topKPerKind entries per kind (fact + preference)")

	// Count by kind.
	factCount, prefCount := 0, 0
	for _, item := range items {
		switch item.Kind {
		case KindFact:
			factCount++
		case KindPreference:
			prefCount++
		}
	}
	assert.Equal(t, topKPerKind, factCount, "should have topKPerKind fact entries")
	assert.Equal(t, topKPerKind, prefCount, "should have topKPerKind preference entries")
}

// ---------------------------------------------------------------------------
// mockEmbedder tests
// ---------------------------------------------------------------------------

// TestMockEmbedder_ZeroVector verifies that mockEmbedder returns 1024-dimensional
// zero vectors for each input text.
func TestMockEmbedder_ZeroVector(t *testing.T) {
	e := NewMockEmbedder()
	ctx := context.Background()

	texts := []string{"hello", "world", "go"}
	vecs, err := e.Embed(ctx, texts)
	require.NoError(t, err)
	require.Len(t, vecs, len(texts), "one vector per input text")

	for i, vec := range vecs {
		assert.Len(t, vec, 1024, "vector[%d] must be 1024-dimensional", i)
		for j, v := range vec {
			assert.Equal(t, float32(0), v,
				"vector[%d][%d] must be zero", i, j)
		}
	}
}

// TestMockEmbedder_EmptyInput verifies that an empty text slice returns an empty result.
func TestMockEmbedder_EmptyInput(t *testing.T) {
	e := NewMockEmbedder()
	ctx := context.Background()

	vecs, err := e.Embed(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, vecs, "empty input should produce empty output")
}

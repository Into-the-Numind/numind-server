package memory

// NOTE: This file covers both unit tests (helper-table tests for
// parseSelectorResponse / pickByIDs / hashInput / buildCacheKey) and
// integration tests (SQLite-backed end-to-end SelectTop5 flow via
// newSelectorTestStore + mockSelectorChat). Spec §Step 7's
// "selector_integration_test.go" structural requirement is satisfied by the
// SQLite-backed test cases below; not split into a separate file to keep
// test setup centralised (one SQLite helper, one mock chat, one place for
// new cases to land).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ─── Test helpers ──────────────────────────────────────────────────────────────

// newSelectorTestStore builds in-memory SQLite + AutoMigrate'd memory tables
// and returns a wired fact store.
func newSelectorTestStore(t *testing.T) (store.IUserMemoryFactStore, *gorm.DB) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserMemoryProfile{}, &model.UserMemoryFact{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return store.NewUserMemoryFactStore(db), db
}

// seedFacts inserts n facts for the given user with confidence decreasing
// from 0.95 down. Returns the created facts (already with assigned IDs).
func seedFacts(t *testing.T, factStore store.IUserMemoryFactStore, userID uint, n int) []model.UserMemoryFact {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		// Confidence decreases by 0.01 per fact; index 0 = 0.95, index 49 = 0.46
		conf := 0.95 - float64(i)*0.01
		if conf < 0.70 {
			conf = 0.70
		}
		f := &model.UserMemoryFact{
			UUID:              fmt.Sprintf("uuid-%d-%d", userID, i),
			UserID:            userID,
			SubjectID:         nil,
			Content:           fmt.Sprintf("fact-%d-content for user %d (idx=%d)", i, userID, i),
			Category:          model.MemoryFactCategoryContext,
			Confidence:        conf,
			Importance:        0.50,
			SourceSessionID:   "sess-test",
			SourceMessageUUID: fmt.Sprintf("msg-%d", i),
			SourceExtractedAt: time.Now(),
			EmbeddingHash:     fmt.Sprintf("hash-%d-%d", userID, i),
			IsArchived:        false,
		}
		require.NoError(t, factStore.Create(ctx, f))
	}
	rows, err := factStore.List(ctx, userID, store.ListFactOpts{OrderBy: "confidence", Limit: n})
	require.NoError(t, err)
	require.Len(t, rows, n)
	return rows
}

// mockSelectorChat counts calls and returns a programmable response.
type mockSelectorChat struct {
	mu       sync.Mutex
	calls    int
	respFn   func(callIdx int, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)
	lastReq  aiservice.ChatRequest
	lastTask string
}

func newMockSelectorChat(fn func(int, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) *mockSelectorChat {
	return &mockSelectorChat{respFn: fn}
}

func (m *mockSelectorChat) fn() selectorChatFn {
	return func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		m.mu.Lock()
		idx := m.calls
		m.calls++
		m.lastReq = req
		m.lastTask = taskID
		m.mu.Unlock()
		return m.respFn(idx, req)
	}
}

func (m *mockSelectorChat) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// staticSelectorResp returns a mock that always replies with the given JSON.
func staticSelectorResp(jsonContent string) *mockSelectorChat {
	return newMockSelectorChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: jsonContent,
			Model:   "mock-qwen-turbo",
			Usage:   aiservice.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
		}, nil
	})
}

// errSelectorResp returns a mock that always errors.
func errSelectorResp(err error) *mockSelectorChat {
	return newMockSelectorChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, err
	})
}

// jsonArrayOf builds `[id1, id2, ...]` from a slice of IDs.
func jsonArrayOf(ids ...uint64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ─── Case 1: 0 facts → returns nil + empty section ───────────────────────────

func TestSelector_NoFacts_ReturnsNilAndEmptySection(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	mc := staticSelectorResp("[]")
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))

	ctx := context.Background()
	facts, err := svc.SelectTop5(ctx, 1, "随便问一句")
	require.NoError(t, err)
	assert.Nil(t, facts, "no candidates should return nil slice")
	assert.Equal(t, 0, mc.callCount(), "no LLM call when 0 facts exist")

	section := svc.BuildMemorySection(facts)
	assert.Equal(t, "", section, "section must be empty when no facts")
}

// ─── Case 2: ≤5 facts → shortcircuit (skip LLM + all returned + UpdateUsage) ─

func TestSelector_ShortcircuitForFewFacts(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 42, 3)

	mc := staticSelectorResp("[]")
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))

	ctx := context.Background()
	facts, err := svc.SelectTop5(ctx, 42, "我的工作背景是什么")
	require.NoError(t, err)
	assert.Len(t, facts, 3, "≤5 candidates → all returned")
	assert.Equal(t, 0, mc.callCount(), "shortcircuit skips LLM")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectShortcircuit])
	assert.Equal(t, int64(3), snap.SelectFactsInjected)

	_ = seeded
	// Re-list and check use_count incremented.
	listed, err := factStore.List(ctx, 42, store.ListFactOpts{OrderBy: "confidence", Limit: 10})
	require.NoError(t, err)
	for _, f := range listed {
		assert.Equal(t, 1, f.UseCount, "fact %d use_count should be 1", f.ID)
		require.NotNil(t, f.LastUsedAt, "fact %d LastUsedAt should be set", f.ID)
	}

	// Section content: 3 numbered items, wrapped in <personal_context ...>.
	section := svc.BuildMemorySection(facts)
	assert.Contains(t, section, `<personal_context data-internal="true">`)
	assert.Contains(t, section, "</personal_context>")
	assert.Contains(t, section, "1. [context]")
	assert.Contains(t, section, "2. [context]")
	assert.Contains(t, section, "3. [context]")
}

// ─── Case 3: 50 facts + LLM returns 5 ids → choose those 5 + UpdateUsage + cache write ─

func TestSelector_50Facts_LLMReturns5IDs(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 7, 50)

	// Pick five IDs from the middle of the list (so we know LLM-ranked them).
	pickedIDs := []uint64{seeded[10].ID, seeded[20].ID, seeded[5].ID, seeded[30].ID, seeded[45].ID}
	mc := staticSelectorResp(jsonArrayOf(pickedIDs...))

	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 7, "如何处理客户跟进")
	require.NoError(t, err)
	require.Len(t, facts, 5)
	gotIDs := make([]uint64, 5)
	for i, f := range facts {
		gotIDs[i] = f.ID
	}
	assert.Equal(t, pickedIDs, gotIDs, "facts should match LLM-returned IDs in order")
	assert.Equal(t, 1, mc.callCount(), "exactly 1 LLM call expected")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectLLMSuccess])
	assert.Equal(t, int64(5), snap.SelectFactsInjected)

	// UpdateUsage hit only the chosen 5 IDs.
	listed, err := factStore.List(ctx, 7, store.ListFactOpts{OrderBy: "confidence", Limit: 50})
	require.NoError(t, err)
	hitSet := make(map[uint64]bool)
	for _, id := range pickedIDs {
		hitSet[id] = true
	}
	usedCount := 0
	for _, f := range listed {
		if hitSet[f.ID] {
			assert.Equal(t, 1, f.UseCount, "chosen fact %d should have use_count=1", f.ID)
			usedCount++
		} else {
			assert.Equal(t, 0, f.UseCount, "non-chosen fact %d should have use_count=0", f.ID)
		}
	}
	assert.Equal(t, 5, usedCount)

	// LLM request: verify task_id was profile.AgentMemorySelect.
	assert.Equal(t, "agent.memory_select", mc.lastTask)
	require.NotEmpty(t, mc.lastReq.Messages)
	assert.Contains(t, mc.lastReq.Messages[0].Content.Text, "使用者本人", "prompt must emphasise Layer A")
}

// ─── Case 4: LLM error → fallback to confidence top-5 ────────────────────────

func TestSelector_LLMError_FallbacksToConfidenceTop(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 8, 20)

	mc := errSelectorResp(errors.New("model timeout"))
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 8, "随便问")
	require.NoError(t, err, "LLM error must not propagate — caller fallback contract")
	require.Len(t, facts, 5, "fallback returns confidence top-5")

	// Top-5 by confidence DESC == first 5 of seeded (already conf-desc sorted by store.List).
	for i := 0; i < 5; i++ {
		assert.Equal(t, seeded[i].ID, facts[i].ID, "fallback fact %d should match seeded[%d]", i, i)
	}

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectLLMFailure])
	assert.Equal(t, int64(5), snap.SelectFactsInjected)
}

// ─── Case 5: LLM returns invalid JSON → fallback to confidence top-5 ─────────

func TestSelector_LLMInvalidJSON_FallbacksToConfidenceTop(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 9, 15)

	mc := staticSelectorResp("this is not JSON at all")
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 9, "随便问")
	require.NoError(t, err)
	require.Len(t, facts, 5)
	for i := 0; i < 5; i++ {
		assert.Equal(t, seeded[i].ID, facts[i].ID)
	}

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectParseFailure])
	assert.Equal(t, int64(5), snap.SelectFactsInjected)
}

// ─── Case 6: LLM returns 3 ids → accept fewer than max ───────────────────────

func TestSelector_LLMReturnsFewerThan5_Accepts(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 10, 20)

	// LLM returns only 3 IDs that exist in candidates.
	picked := []uint64{seeded[15].ID, seeded[8].ID, seeded[2].ID}
	mc := staticSelectorResp(jsonArrayOf(picked...))
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 10, "请告诉我")
	require.NoError(t, err)
	require.Len(t, facts, 5, "selector backfills from candidate top when LLM under-returns")

	// First 3 should be the LLM picks, in order.
	for i, want := range picked {
		assert.Equal(t, want, facts[i].ID, "facts[%d] should match LLM pick", i)
	}
	// Last 2 should be backfilled from candidate top, skipping already-chosen IDs.
	pickedSet := map[uint64]bool{}
	for _, id := range picked {
		pickedSet[id] = true
	}
	for i := 3; i < 5; i++ {
		assert.False(t, pickedSet[facts[i].ID], "backfill fact %d must not duplicate LLM picks", facts[i].ID)
	}

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectLLMSuccess])
}

// ─── Case 7: LLM returns 7 ids → truncate to 5 ───────────────────────────────

func TestSelector_LLMReturnsMoreThan5_Truncates(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 11, 30)

	// LLM returns 7 IDs.
	picked := []uint64{
		seeded[3].ID, seeded[10].ID, seeded[15].ID, seeded[20].ID,
		seeded[25].ID, seeded[28].ID, seeded[5].ID,
	}
	mc := staticSelectorResp(jsonArrayOf(picked...))
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 11, "Q")
	require.NoError(t, err)
	require.Len(t, facts, 5, "must truncate at maxFacts=5")
	for i := 0; i < 5; i++ {
		assert.Equal(t, picked[i], facts[i].ID)
	}
}

// ─── Case 8: LLM returns unknown ids → drop them + backfill to 5 ─────────────

func TestSelector_LLMReturnsUnknownIDs_DropsAndBackfills(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 12, 20)

	// LLM returns 2 known + 3 unknown IDs (very high IDs).
	picked := []uint64{seeded[5].ID, 99999, seeded[10].ID, 99998, 99997}
	mc := staticSelectorResp(jsonArrayOf(picked...))
	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()

	facts, err := svc.SelectTop5(ctx, 12, "Q")
	require.NoError(t, err)
	require.Len(t, facts, 5, "unknown IDs dropped + backfill to 5")
	// First two should be the known LLM picks (in order).
	assert.Equal(t, seeded[5].ID, facts[0].ID)
	assert.Equal(t, seeded[10].ID, facts[1].ID)
	// Last 3 should be backfilled from candidate top (skipping the two already chosen).
	for i := 2; i < 5; i++ {
		assert.NotEqual(t, uint64(99999), facts[i].ID, "no unknown IDs leaked through")
		assert.NotEqual(t, uint64(99998), facts[i].ID)
		assert.NotEqual(t, uint64(99997), facts[i].ID)
	}
}

// ─── Case 9: cache hit (same user + same input within 30s) skips LLM ─────────

func TestSelector_CacheHit_SameInputSkipsLLM(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 13, 20)
	picked := []uint64{seeded[0].ID, seeded[1].ID, seeded[2].ID, seeded[3].ID, seeded[4].ID}
	mc := staticSelectorResp(jsonArrayOf(picked...))

	svc := NewSelectorService(factStore, WithSelectorChatFn(mc.fn()))
	ctx := context.Background()
	input := "客户 ABC 怎么跟进"

	// 1st call → LLM hit.
	facts1, err := svc.SelectTop5(ctx, 13, input)
	require.NoError(t, err)
	require.Len(t, facts1, 5)
	assert.Equal(t, 1, mc.callCount())

	// 2nd call (same user + same input, immediately) → cache hit, no new LLM call.
	facts2, err := svc.SelectTop5(ctx, 13, input)
	require.NoError(t, err)
	require.Len(t, facts2, 5)
	assert.Equal(t, 1, mc.callCount(), "2nd call must hit cache, not LLM")
	for i := range facts1 {
		assert.Equal(t, facts1[i].ID, facts2[i].ID, "cache should return same fact ordering")
	}

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectLLMSuccess], "1 LLM success")
	assert.Equal(t, int64(1), snap.SelectRuns[metrics.MemorySelectCacheHit], "1 cache hit")
	assert.Equal(t, int64(10), snap.SelectFactsInjected, "5 + 5 injected facts")

	// 3rd call with a *different* input → cache miss → LLM called again.
	facts3, err := svc.SelectTop5(ctx, 13, "完全不一样的问题")
	require.NoError(t, err)
	require.Len(t, facts3, 5)
	assert.Equal(t, 2, mc.callCount(), "different input bypasses cache")
}

// ─── Case 10: cache TTL expiry → re-fetch + re-call LLM ─────────────────────

// TestSelector_CacheExpiry_RefetchesAfterTTL covers the else-branch of the
// cache lookup (s.cache.Remove on expired entry). Uses a 10ms TTL via
// WithSelectorCacheTTL so the second call (after a short sleep) finds the
// entry expired, removes it, and runs the full LLM path again.
func TestSelector_CacheExpiry_RefetchesAfterTTL(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, _ := newSelectorTestStore(t)
	seeded := seedFacts(t, factStore, 14, 20)
	picked := []uint64{seeded[0].ID, seeded[1].ID, seeded[2].ID, seeded[3].ID, seeded[4].ID}
	mc := staticSelectorResp(jsonArrayOf(picked...))

	svc := NewSelectorService(
		factStore,
		WithSelectorChatFn(mc.fn()),
		WithSelectorCacheTTL(10*time.Millisecond),
	)
	ctx := context.Background()
	input := "缓存过期后应该重新调 LLM"

	// 1st call → LLM hit, cache write.
	facts1, err := svc.SelectTop5(ctx, 14, input)
	require.NoError(t, err)
	require.Len(t, facts1, 5)
	assert.Equal(t, 1, mc.callCount(), "1st call must invoke LLM")

	// Wait past TTL.
	time.Sleep(25 * time.Millisecond)

	// 2nd call (same input) → expired entry dropped, LLM re-invoked.
	facts2, err := svc.SelectTop5(ctx, 14, input)
	require.NoError(t, err)
	require.Len(t, facts2, 5)
	assert.Equal(t, 2, mc.callCount(), "2nd call after TTL must re-invoke LLM, not serve stale cache")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(2), snap.SelectRuns[metrics.MemorySelectLLMSuccess], "both calls were LLM successes")
	assert.Equal(t, int64(0), snap.SelectRuns[metrics.MemorySelectCacheHit], "no cache hits — both expired")
}

// ─── Extra: BuildMemorySection content sanity ────────────────────────────────

func TestSelector_BuildMemorySection_Format(t *testing.T) {
	svc := NewSelectorService(nil, WithSelectorChatFn(staticSelectorResp("[]").fn()))
	facts := []model.UserMemoryFact{
		{ID: 1, Content: "用户是数据分析师", Category: model.MemoryFactCategoryContext},
		{ID: 2, Content: "偏好简洁回答", Category: model.MemoryFactCategoryPreference},
	}
	out := svc.BuildMemorySection(facts)
	assert.True(t, strings.HasPrefix(out, `<personal_context data-internal="true">`),
		"section must open with the scrubber-protected tag")
	assert.True(t, strings.HasSuffix(strings.TrimRight(out, "\n"), "</personal_context>"),
		"section must close with </personal_context>")
	assert.Contains(t, out, "1. [context] 用户是数据分析师")
	assert.Contains(t, out, "2. [preference] 偏好简洁回答")
	assert.Contains(t, out, "【用户档案】")
}

// ─── Extra: parse helpers ────────────────────────────────────────────────────

func TestParseSelectorResponse_Variants(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{name: "plain int array", input: "[1, 2, 3]", want: []string{"1", "2", "3"}},
		{name: "string array", input: `["12", "f042", "5"]`, want: []string{"12", "f042", "5"}},
		{name: "markdown fence json", input: "```json\n[7, 8]\n```", want: []string{"7", "8"}},
		{name: "prose around array", input: "好的, 这是结果: [99, 100] 完毕", want: []string{"99", "100"}},
		{name: "empty array", input: "[]", want: []string{}},
		{name: "empty string", input: "", wantErr: true},
		{name: "no array", input: "什么都没返回", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSelectorResponse(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPickByIDs_Variants(t *testing.T) {
	candidates := []model.UserMemoryFact{
		{ID: 1, Content: "a"}, {ID: 2, Content: "b"}, {ID: 3, Content: "c"},
		{ID: 4, Content: "d"}, {ID: 5, Content: "e"}, {ID: 6, Content: "f"},
	}
	cases := []struct {
		name     string
		raw      []string
		wantIDs  []uint64
		maxFacts int
	}{
		{
			name:     "exact 5",
			raw:      []string{"3", "5", "1", "6", "2"},
			wantIDs:  []uint64{3, 5, 1, 6, 2},
			maxFacts: 5,
		},
		{
			name:     "f-prefix style",
			raw:      []string{"f002", "f005"},
			wantIDs:  []uint64{2, 5, 1, 3, 4}, // 2 picks + backfill from candidate top
			maxFacts: 5,
		},
		{
			name:     "unknown ids skipped + backfilled",
			raw:      []string{"99", "3", "100"},
			wantIDs:  []uint64{3, 1, 2, 4, 5},
			maxFacts: 5,
		},
		{
			name:     "duplicate ids deduped",
			raw:      []string{"1", "1", "2", "1", "3"},
			wantIDs:  []uint64{1, 2, 3, 4, 5},
			maxFacts: 5,
		},
		{
			name:     "truncate at maxFacts",
			raw:      []string{"1", "2", "3", "4", "5", "6"},
			wantIDs:  []uint64{1, 2, 3, 4, 5},
			maxFacts: 5,
		},
		{
			name:     "empty raw → backfill all",
			raw:      []string{},
			wantIDs:  []uint64{1, 2, 3, 4, 5},
			maxFacts: 5,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out := pickByIDs(candidates, tc.raw, tc.maxFacts)
			gotIDs := make([]uint64, len(out))
			for i, f := range out {
				gotIDs[i] = f.ID
			}
			assert.Equal(t, tc.wantIDs, gotIDs)
		})
	}
}

func TestHashInput_StableDifferent(t *testing.T) {
	a := hashInput("hello")
	b := hashInput("hello")
	c := hashInput("world")
	assert.Equal(t, a, b, "same input → same hash")
	assert.NotEqual(t, a, c, "different input → different hash")
	assert.Equal(t, 16, len(a), "first 16 hex chars")
}

func TestBuildCacheKey_Format(t *testing.T) {
	k1 := buildCacheKey(42, "hi")
	k2 := buildCacheKey(42, "hi")
	k3 := buildCacheKey(43, "hi")
	k4 := buildCacheKey(42, "different")
	assert.Equal(t, k1, k2)
	assert.NotEqual(t, k1, k3, "different user → different key")
	assert.NotEqual(t, k1, k4, "different input → different key")
	assert.True(t, strings.HasPrefix(k1, "42:"))
}
